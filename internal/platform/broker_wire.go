package platform

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// PrivilegedBrokerFrameLimit bounds one controller-to-broker JSON Lines frame.
// Broker requests are deliberately much smaller than manager API frames because
// they contain opaque identifiers only, never configuration content.
const PrivilegedBrokerFrameLimit = 16 << 10

var ErrBrokerFrameTooLarge = errors.New("privileged broker frame exceeds the maximum size")

// BrokerWireRequest is the untrusted wire representation sent by a controller.
// It deliberately has no UserID: only an authenticated broker transport may
// supply that value while constructing a BrokerRequest.
type BrokerWireRequest struct {
	Version         int             `json:"version"`
	Operation       BrokerOperation `json:"operation"`
	RequestID       string          `json:"request_id"`
	ConfigurationID string          `json:"configuration_id"`
	SnapshotID      string          `json:"snapshot_id,omitempty"`
}

// BrokerWireError is the bounded error representation that a future broker may
// return to its controller. It omits filesystem, command, process, and device
// implementation details.
type BrokerWireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BrokerWireResponse is a JSON Lines response to one broker request.
type BrokerWireResponse struct {
	RequestID     string               `json:"request_id"`
	Authorization *BrokerAuthorization `json:"authorization,omitempty"`
	Error         *BrokerWireError     `json:"error,omitempty"`
}

// NewBrokerFrameReader returns a reader sized to reject oversized frames before
// a controller can make the broker allocate unbounded request memory.
func NewBrokerFrameReader(reader io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(reader, PrivilegedBrokerFrameLimit+1)
}

// ReadBrokerFrame reads one JSON Lines frame. An EOF without a newline is not a
// complete broker request and is returned to the caller unchanged.
func ReadBrokerFrame(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > PrivilegedBrokerFrameLimit+1 {
		return nil, ErrBrokerFrameTooLarge
	}
	if err != nil {
		return nil, err
	}
	frame := bytes.TrimSuffix(line, []byte{'\n'})
	frame = bytes.TrimSuffix(frame, []byte{'\r'})
	if len(frame) == 0 {
		return nil, fmt.Errorf("%w: empty frame", ErrBrokerMalformedRequest)
	}
	return append([]byte(nil), frame...), nil
}

// ParseBrokerWireRequest decodes an untrusted controller request and binds it
// to the peer identity authenticated by the transport. Unknown fields are
// rejected so a future client cannot smuggle command, path, or content fields
// into the privileged protocol.
func ParseBrokerWireRequest(frame []byte, authenticatedUserID string) (BrokerRequest, error) {
	if !validBrokerID(authenticatedUserID) {
		return BrokerRequest{}, fmt.Errorf("%w: authenticated user identity", ErrBrokerUnauthorized)
	}
	var wire BrokerWireRequest
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return BrokerRequest{}, fmt.Errorf("%w: decode request: %v", ErrBrokerMalformedRequest, err)
	}
	if err := requireBrokerJSONEOF(decoder); err != nil {
		return BrokerRequest{}, err
	}
	request := BrokerRequest{
		Version:         wire.Version,
		Operation:       wire.Operation,
		RequestID:       wire.RequestID,
		UserID:          authenticatedUserID,
		ConfigurationID: wire.ConfigurationID,
		SnapshotID:      wire.SnapshotID,
	}
	if err := validateRequest(request); err != nil {
		return BrokerRequest{}, err
	}
	return request, nil
}

func requireBrokerJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: request contains multiple JSON values", ErrBrokerMalformedRequest)
		}
		return fmt.Errorf("%w: trailing request data: %v", ErrBrokerMalformedRequest, err)
	}
	return nil
}

// MarshalBrokerWireResponse encodes one bounded JSON Lines response. Callers
// should close a connection after a framing error rather than trying to recover
// from an untrusted oversized request.
func MarshalBrokerWireResponse(response BrokerWireResponse) ([]byte, error) {
	if !validBrokerID(response.RequestID) {
		return nil, fmt.Errorf("%w: response request ID", ErrBrokerMalformedRequest)
	}
	if (response.Authorization == nil) == (response.Error == nil) {
		return nil, fmt.Errorf("%w: response must contain exactly one result", ErrBrokerMalformedRequest)
	}
	data, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("marshal privileged broker response: %w", err)
	}
	if len(data)+1 > PrivilegedBrokerFrameLimit {
		return nil, ErrBrokerFrameTooLarge
	}
	return append(data, '\n'), nil
}
