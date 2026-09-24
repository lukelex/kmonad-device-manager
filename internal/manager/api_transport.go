package manager

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

const (
	apiFrameLimit      = 1 << 20
	apiMaxInFlight     = 32
	apiMaxClients      = 64
	apiDefaultDeadline = 30 * time.Second
)

var errAPIFrameTooLarge = errors.New("API frame exceeds the maximum size")

type apiServer struct {
	listener       platform.APIListener
	serverID       string
	managerVersion string
	owner          *manager

	mu        sync.Mutex
	closed    bool
	nextID    uint64
	clients   map[uint64]platform.APIConnection
	finished  chan struct{}
	closeOnce sync.Once
}

type apiRequest struct {
	Type           string          `json:"type"`
	ID             string          `json:"id"`
	Method         string          `json:"method"`
	Params         json.RawMessage `json:"params"`
	DeadlineMS     *int            `json:"deadline_ms,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiResponse struct {
	Type   string    `json:"type"`
	ID     string    `json:"id"`
	Result any       `json:"result,omitempty"`
	Error  *apiError `json:"error,omitempty"`
}

type apiResponseWriter struct {
	connection platform.APIConnection
	mu         sync.Mutex
}

func startAPIServer(socketPath, managerVersion string, owner *manager) (*apiServer, error) {
	listener, err := host.ListenAPISocket(socketPath)
	if err != nil {
		return nil, err
	}
	serverID, err := newAPIServerID()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	return &apiServer{
		listener: listener, serverID: serverID, managerVersion: managerVersion, owner: owner,
		clients: make(map[uint64]platform.APIConnection), finished: make(chan struct{}),
	}, nil
}

func newAPIServerID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate API server ID: %w", err)
	}
	return "srv_" + hex.EncodeToString(data), nil
}

// serveAPISocket is deliberately independent of reconciliation. Any listener,
// client, or request failure is logged and affects only this control plane.
func serveAPISocket(ctx context.Context, socketPath, managerVersion string, owner *manager) {
	server, err := startAPIServer(socketPath, managerVersion, owner)
	if err != nil {
		logf("API listener unavailable: %v", err)
		return
	}
	server.run(ctx)
}

func (server *apiServer) run(ctx context.Context) {
	defer close(server.finished)
	defer server.Close()
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-server.finished:
		}
	}()
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if !server.isClosed() {
				logf("API listener stopped unexpectedly: %v", err)
			}
			return
		}
		clientID, accepted := server.addClient(connection)
		if !accepted {
			_ = connection.Close()
			continue
		}
		go func() {
			defer server.removeClient(clientID)
			serveAPIClient(ctx, connection, server.serverID, server.managerVersion, server.owner)
		}()
	}
}

func (server *apiServer) addClient(connection platform.APIConnection) (uint64, bool) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed || len(server.clients) >= apiMaxClients {
		return 0, false
	}
	server.nextID++
	server.clients[server.nextID] = connection
	return server.nextID, true
}

func (server *apiServer) removeClient(clientID uint64) {
	server.mu.Lock()
	delete(server.clients, clientID)
	server.mu.Unlock()
}

func (server *apiServer) isClosed() bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.closed
}

func (server *apiServer) Close() error {
	var closeErr error
	server.closeOnce.Do(func() {
		server.mu.Lock()
		server.closed = true
		connections := make([]platform.APIConnection, 0, len(server.clients))
		for _, connection := range server.clients {
			connections = append(connections, connection)
		}
		server.clients = make(map[uint64]platform.APIConnection)
		server.mu.Unlock()
		for _, connection := range connections {
			_ = connection.Close()
		}
		closeErr = server.listener.Close()
	})
	return closeErr
}

func serveAPIClient(serverContext context.Context, connection platform.APIConnection, serverID, managerVersion string, owner *manager) {
	defer connection.Close()
	clientContext, cancel := context.WithCancel(serverContext)
	defer cancel()
	reader := bufio.NewReaderSize(connection, apiFrameLimit+1)
	writer := apiResponseWriter{connection: connection}
	helloComplete := false
	inFlight := make(map[string]bool)
	var inFlightMu sync.Mutex
	semaphore := make(chan struct{}, apiMaxInFlight)

	for {
		frame, err := readAPIFrame(reader)
		if err != nil {
			return
		}
		request, requestErr := parseAPIRequest(frame)
		if !helloComplete {
			if requestErr != nil {
				_ = writer.error(request.ID, *requestErr)
				return
			}
			if request.Method != "session.hello" {
				_ = writer.error(request.ID, apiError{Code: "invalid_request", Message: "session.hello must be the first request"})
				return
			}
			if err := handleSessionHello(request, &writer, serverID, managerVersion); err != nil {
				_ = writer.error(request.ID, *err)
				return
			}
			helloComplete = true
			continue
		}
		if requestErr != nil {
			_ = writer.error(request.ID, *requestErr)
			continue
		}
		select {
		case semaphore <- struct{}{}:
		default:
			_ = writer.error(request.ID, apiError{Code: "resource_exhausted", Message: "too many in-flight requests"})
			continue
		}
		inFlightMu.Lock()
		if inFlight[request.ID] {
			inFlightMu.Unlock()
			<-semaphore
			_ = writer.error(request.ID, apiError{Code: "invalid_request", Message: "request ID is already in flight"})
			continue
		}
		inFlight[request.ID] = true
		inFlightMu.Unlock()
		go func(request apiRequest) {
			defer func() {
				inFlightMu.Lock()
				delete(inFlight, request.ID)
				inFlightMu.Unlock()
				<-semaphore
			}()
			if request.Method == "session.hello" {
				_ = writer.error(request.ID, apiError{Code: "invalid_request", Message: "session.hello may only be sent once"})
				return
			}
			if !knownAPIMethod(request.Method) {
				_ = writer.error(request.ID, apiError{Code: "invalid_request", Message: "unknown API method"})
				return
			}
			requestContext, cancel := apiRequestContext(clientContext, request)
			defer cancel()
			if owner == nil {
				_ = writer.error(request.ID, apiError{Code: "internal", Message: "manager command owner is unavailable"})
				return
			}
			if request.Method == "validation.preview" {
				result := previewValidation(requestContext, owner, request.Params)
				if result.err != nil {
					_ = writer.error(request.ID, *result.err)
					return
				}
				_ = writer.result(request.ID, result.result)
				return
			}
			if request.Method == "configuration.apply" || request.Method == "configuration.create" || request.Method == "configuration.update" {
				params, apiErr := parseConfigurationApplyParams(request.Method, request.Params)
				if apiErr != nil {
					_ = writer.error(request.ID, *apiErr)
					return
				}
				result := runIdempotentMutation(requestContext, owner, request, func(mutationContext context.Context, operationID string) commandResult {
					params.operationID = operationID
					return applyManagedConfiguration(mutationContext, owner, params)
				})
				if result.err != nil {
					_ = writer.error(request.ID, *result.err)
					return
				}
				_ = writer.result(request.ID, result.result)
				return
			}
			if request.Method == "configuration.adopt" {
				var params configurationAdoptParams
				if err := json.Unmarshal(request.Params, &params); err != nil || params.ConfigurationID == "" {
					_ = writer.error(request.ID, apiError{Code: "invalid_request", Message: "configuration.adopt requires configuration_id"})
					return
				}
				result := runIdempotentMutation(requestContext, owner, request, func(mutationContext context.Context, operationID string) commandResult {
					params.operationID = operationID
					return adoptExternalConfiguration(mutationContext, owner, params)
				})
				if result.err != nil {
					_ = writer.error(request.ID, *result.err)
					return
				}
				_ = writer.result(request.ID, result.result)
				return
			}
			if request.Method == "events.subscribe" {
				var params eventsSubscribeParams
				if err := json.Unmarshal(request.Params, &params); err != nil {
					_ = writer.error(request.ID, apiError{Code: "invalid_request", Message: "invalid events.subscribe parameters"})
					return
				}
				if params.AfterEventID != nil && params.AfterServerID != "" && params.AfterServerID != serverID {
					params.forceResync = true
				}
				result := owner.submitCommand(requestContext, func(_ context.Context, m *manager) commandResult {
					return commandResult{result: m.subscribeEvents(params)}
				})
				if result.err != nil {
					_ = writer.error(request.ID, *result.err)
					return
				}
				subscription, ok := result.result.(eventSubscription)
				if !ok {
					_ = writer.error(request.ID, apiError{Code: "internal", Message: "event subscription failed"})
					return
				}
				if err := writer.result(request.ID, map[string]any{"subscription_id": subscription.ID, "server_id": serverID, "state_revision": subscription.StateRevision, "latest_event_id": subscription.LatestEventID}); err != nil {
					return
				}
				go forwardEventSubscription(clientContext, owner, &writer, subscription)
				return
			}
			if request.Method == "configuration.set_enabled" || request.Method == "configuration.delete" {
				result := runIdempotentMutation(requestContext, owner, request, func(mutationContext context.Context, operationID string) commandResult {
					return owner.submitCommand(mutationContext, func(commandContext context.Context, m *manager) commandResult {
						if request.Method == "configuration.set_enabled" {
							var params configurationSetEnabledParams
							if err := json.Unmarshal(request.Params, &params); err != nil {
								return commandResult{err: &apiError{Code: "invalid_request", Message: "invalid configuration.set_enabled parameters"}}
							}
							params.operationID = operationID
							return m.setManagedConfigurationEnabled(commandContext, params)
						}
						var params configurationDeleteParams
						if err := json.Unmarshal(request.Params, &params); err != nil {
							return commandResult{err: &apiError{Code: "invalid_request", Message: "invalid configuration.delete parameters"}}
						}
						params.operationID = operationID
						return m.deleteManagedConfiguration(commandContext, params)
					})
				})
				if result.err != nil {
					_ = writer.error(request.ID, *result.err)
				} else {
					_ = writer.result(request.ID, result.result)
				}
				return
			}
			result := owner.submitCommand(requestContext, func(_ context.Context, m *manager) commandResult {
				switch request.Method {
				case "manager.get":
					return commandResult{result: m.managerInfo()}
				case "snapshot.get":
					return commandResult{result: m.snapshot()}
				case "device.list":
					m.refreshDevices()
					return commandResult{result: map[string]any{"devices": m.deviceList()}}
				case "configuration.list":
					m.refreshDevices()
					return commandResult{result: map[string]any{"configurations": m.configurationList()}}
				case "device.identify.start":
					var params identifyStartParams
					if err := json.Unmarshal(request.Params, &params); err != nil {
						return commandResult{err: &apiError{Code: "invalid_request", Message: "invalid device.identify.start parameters"}}
					}
					return m.startIdentification(requestContext, params)
				case "device.identify.cancel":
					var params identifyCancelParams
					if err := json.Unmarshal(request.Params, &params); err != nil || params.OperationID == "" {
						return commandResult{err: &apiError{Code: "invalid_request", Message: "operation_id is required"}}
					}
					return m.cancelIdentificationOperation(params.OperationID)
				case "operation.get":
					var params identifyCancelParams
					if err := json.Unmarshal(request.Params, &params); err != nil || params.OperationID == "" {
						return commandResult{err: &apiError{Code: "invalid_request", Message: "operation_id is required"}}
					}
					return m.identificationOperation(params.OperationID)
				default:
					return commandResult{err: &apiError{Code: "unsupported_capability", Message: "method is not implemented by this manager"}}
				}
			})
			if result.err != nil {
				_ = writer.error(request.ID, *result.err)
				return
			}
			switch request.Method {
			case "manager.get":
				if info, ok := result.result.(ManagerInfo); ok {
					info.ManagerVersion = managerVersion
					info.ServerID = serverID
					info.EventCursor.ServerID = serverID
					result.result = info
				}
			case "snapshot.get":
				if snapshot, ok := result.result.(Snapshot); ok {
					snapshot.EventCursor.ServerID = serverID
					result.result = snapshot
				}
			}
			_ = writer.result(request.ID, result.result)
		}(request)
	}
}

func parseConfigurationApplyParams(method string, raw json.RawMessage) (configurationApplyParams, *apiError) {
	var params configurationApplyParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return configurationApplyParams{}, &apiError{Code: "invalid_request", Message: "invalid " + method + " parameters"}
	}
	switch method {
	case "configuration.create":
		if params.ConfigurationID != "" || params.ExpectedRevision != nil {
			return configurationApplyParams{}, &apiError{Code: "invalid_request", Message: "configuration.create accepts name and model only"}
		}
	case "configuration.update":
		if params.ConfigurationID == "" || params.ExpectedRevision == nil {
			return configurationApplyParams{}, &apiError{Code: "invalid_request", Message: "configuration.update requires configuration_id and expected_revision"}
		}
	}
	return params, nil
}

func apiRequestContext(parent context.Context, request apiRequest) (context.Context, context.CancelFunc) {
	deadline := apiDefaultDeadline
	if request.DeadlineMS != nil {
		deadline = time.Duration(*request.DeadlineMS) * time.Millisecond
	}
	return context.WithTimeout(parent, deadline)
}

func readAPIFrame(reader *bufio.Reader) ([]byte, error) {
	frame, err := reader.ReadSlice('\n')
	if len(frame) > apiFrameLimit+1 {
		return nil, errAPIFrameTooLarge
	}
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, errAPIFrameTooLarge
		}
		return nil, err
	}
	frame = frame[:len(frame)-1]
	if len(frame) > 0 && frame[len(frame)-1] == '\r' {
		frame = frame[:len(frame)-1]
	}
	if len(frame) == 0 {
		return nil, errors.New("empty API frame")
	}
	if len(frame) > apiFrameLimit {
		return nil, errAPIFrameTooLarge
	}
	return frame, nil
}

func parseAPIRequest(frame []byte) (apiRequest, *apiError) {
	var request apiRequest
	if err := json.Unmarshal(frame, &request); err != nil {
		return apiRequest{}, &apiError{Code: "invalid_request", Message: "request must be a JSON object"}
	}
	if request.Type != "request" || request.ID == "" || request.Method == "" || len(request.Params) == 0 || !isJSONObject(request.Params) {
		return request, &apiError{Code: "invalid_request", Message: "request requires type, id, method, and object params"}
	}
	if request.DeadlineMS != nil && (*request.DeadlineMS <= 0 || *request.DeadlineMS > int(apiDefaultDeadline/time.Millisecond)) {
		return request, &apiError{Code: "invalid_request", Message: "deadline_ms must be positive and no more than 30000"}
	}
	return request, nil
}

func isJSONObject(data json.RawMessage) bool {
	data = json.RawMessage(bytesTrimSpace(data))
	return len(data) >= 2 && data[0] == '{' && data[len(data)-1] == '}'
}

func bytesTrimSpace(data []byte) []byte {
	for len(data) > 0 && (data[0] == ' ' || data[0] == '\t' || data[0] == '\r' || data[0] == '\n') {
		data = data[1:]
	}
	for len(data) > 0 && (data[len(data)-1] == ' ' || data[len(data)-1] == '\t' || data[len(data)-1] == '\r' || data[len(data)-1] == '\n') {
		data = data[:len(data)-1]
	}
	return data
}

func handleSessionHello(request apiRequest, writer *apiResponseWriter, serverID, managerVersion string) *apiError {
	var params struct {
		SupportedVersions []int `json:"supported_versions"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return &apiError{Code: "invalid_request", Message: "invalid session.hello parameters"}
	}
	for _, version := range params.SupportedVersions {
		if version == 1 {
			return writeAPIResult(writer, request.ID, map[string]any{
				"selected_version": 1,
				"server_id":        serverID,
				"manager_version":  managerVersion,
				"state_revision":   0,
			})
		}
	}
	return &apiError{Code: "unsupported_version", Message: "no supported API version was offered"}
}

func knownAPIMethod(method string) bool {
	switch method {
	case "manager.get", "snapshot.get", "device.list", "configuration.list", "device.identify.start", "device.identify.cancel",
		"validation.preview", "configuration.apply", "configuration.create", "configuration.update", "configuration.set_enabled",
		"configuration.delete", "configuration.adopt", "operation.get", "events.subscribe":
		return true
	default:
		return false
	}
}

func writeAPIResult(writer *apiResponseWriter, id string, result any) *apiError {
	if err := writer.result(id, result); err != nil {
		return &apiError{Code: "internal", Message: "failed to write API response"}
	}
	return nil
}

func (writer *apiResponseWriter) result(id string, result any) error {
	return writer.write(apiResponse{Type: "response", ID: id, Result: result})
}

func (writer *apiResponseWriter) error(id string, apiErr apiError) error {
	return writer.write(apiResponse{Type: "response", ID: id, Error: &apiErr})
}

func (writer *apiResponseWriter) event(event Event) error {
	return writer.writeValue(struct {
		Type string `json:"type"`
		Event
	}{Type: "event", Event: event})
}

func (writer *apiResponseWriter) write(response apiResponse) error {
	return writer.writeValue(response)
}

func (writer *apiResponseWriter) writeValue(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	_, err = writer.connection.Write(append(data, '\n'))
	return err
}

func forwardEventSubscription(ctx context.Context, owner *manager, writer *apiResponseWriter, subscription eventSubscription) {
	defer func() {
		if owner == nil || subscription.ID == 0 {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = owner.submitCommand(cleanup, func(_ context.Context, m *manager) commandResult {
			m.unsubscribeEvents(subscription.ID)
			return commandResult{}
		})
	}()
	if subscription.ResyncNeeded {
		_ = writer.event(subscription.ResyncEvent)
		return
	}
	for _, event := range subscription.Replay {
		if writer.event(event) != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-subscription.Resync:
			_ = writer.event(event)
			return
		case event, open := <-subscription.Events:
			if !open {
				return
			}
			if writer.event(event) != nil {
				return
			}
		}
	}
}

var _ io.Closer = (*apiServer)(nil)
