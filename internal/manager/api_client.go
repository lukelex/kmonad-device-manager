package manager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func identifyCLI(arguments []string, jsonOutput bool) int {
	if len(arguments) == 0 {
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "identify requires start, status, or cancel")
		return 2
	}
	var method string
	var params any
	switch arguments[0] {
	case "start":
		if len(arguments) != 2 && len(arguments) != 4 {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "identify start requires DEVICE_ID and an optional --timeout SECONDS")
			return 2
		}
		start := identifyStartParams{DeviceID: arguments[1]}
		if len(arguments) == 4 {
			if arguments[2] != "--timeout" {
				writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "identify start accepts only --timeout SECONDS")
				return 2
			}
			seconds, err := strconv.Atoi(arguments[3])
			if err != nil || seconds < 1 || seconds > 30 {
				writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "--timeout must be an integer from 1 through 30")
				return 2
			}
			start.TimeoutMS = seconds * 1000
		}
		method, params = "device.identify.start", start
	case "status":
		if len(arguments) != 2 {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "identify status requires OPERATION_ID")
			return 2
		}
		method, params = "operation.get", identifyCancelParams{OperationID: arguments[1]}
	case "cancel":
		if len(arguments) != 2 {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "identify cancel requires OPERATION_ID")
			return 2
		}
		method, params = "device.identify.cancel", identifyCancelParams{OperationID: arguments[1]}
	default:
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "identify requires start, status, or cancel")
		return 2
	}
	operation, apiErr, err := requestIdentificationOperation(method, params)
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "manager_unavailable", err.Error())
		return 1
	}
	if apiErr != nil {
		writeCLIError(os.Stderr, jsonOutput, apiErr.Code, apiErr.Message)
		return 1
	}
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(map[string]Operation{"operation": operation}); err != nil {
			writeCLIError(os.Stderr, true, "output_failed", err.Error())
			return 1
		}
		return 0
	}
	fmt.Printf("ID: %s\nSTATE: %s\nREASON CODE: %s\nREASON: %s\n", operation.ID, operation.State, operation.ReasonCode, operation.Reason)
	return 0
}

func requestIdentificationOperation(method string, params any) (Operation, *apiError, error) {
	data, apiErr, err := requestManagerAPI(method, params)
	if err != nil || apiErr != nil {
		return Operation{}, apiErr, err
	}
	var result struct {
		Operation Operation `json:"operation"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return Operation{}, nil, fmt.Errorf("decode API operation: %w", err)
	}
	if result.Operation.ID == "" {
		return Operation{}, nil, fmt.Errorf("manager API returned no operation")
	}
	return result.Operation, nil, nil
}

func requestManagerAPI(method string, params any) (json.RawMessage, *apiError, error) {
	path, err := host.APISocketPath()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot locate manager API: %w", err)
	}
	connection, err := host.DialAPISocket(path)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot connect to manager API: %w", err)
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	if err := writeAPIClientRequest(connection, apiRequest{
		Type: "request", ID: "hello", Method: "session.hello", Params: json.RawMessage(`{"supported_versions":[1]}`),
	}); err != nil {
		return nil, nil, err
	}
	if response, err := readAPIClientResponse(reader); err != nil {
		return nil, nil, err
	} else if response.Error != nil {
		return nil, response.Error, nil
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, nil, fmt.Errorf("encode API parameters: %w", err)
	}
	if err := writeAPIClientRequest(connection, apiRequest{Type: "request", ID: "operation", Method: method, Params: encoded}); err != nil {
		return nil, nil, err
	}
	response, err := readAPIClientResponse(reader)
	if err != nil {
		return nil, nil, err
	}
	if response.Error != nil {
		return nil, response.Error, nil
	}
	data, err := json.Marshal(response.Result)
	if err != nil {
		return nil, nil, fmt.Errorf("decode manager API response: %w", err)
	}
	return data, nil, nil
}

func validateCLI(arguments []string, jsonOutput bool) int {
	if len(arguments) != 2 || (arguments[0] != "model" && arguments[0] != "file") {
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "validate requires model MODEL_FILE or file KBD_FILE")
		return 2
	}
	limit := loadSettings().maxConfigBytes
	if limit <= 0 {
		limit = defaultMaxConfigBytes
	}
	data, err := readFileLimited(arguments[1], limit)
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "candidate_unreadable", err.Error())
		return 1
	}
	params := validationPreviewParams{}
	if arguments[0] == "model" {
		var model ManagedConfigurationModel
		if err := json.Unmarshal(data, &model); err != nil {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "MODEL_FILE must contain a managed configuration JSON object")
			return 2
		}
		params.Model = &model
	} else {
		content := string(data)
		params.Content = &content
	}
	data, apiErr, err := requestManagerAPI("validation.preview", params)
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "manager_unavailable", err.Error())
		return 1
	}
	if apiErr != nil {
		writeCLIError(os.Stderr, jsonOutput, apiErr.Code, apiErr.Message)
		return 1
	}
	var result struct {
		Validation ValidationResult `json:"validation"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "invalid_response", "manager returned an invalid validation result")
		return 1
	}
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(map[string]ValidationResult{"validation": result.Validation}); err != nil {
			writeCLIError(os.Stderr, true, "output_failed", err.Error())
			return 1
		}
		return 0
	}
	fmt.Printf("OUTCOME: %s\nREASON CODE: %s\nREASON: %s\n", result.Validation.Outcome, result.Validation.ReasonCode, result.Validation.Reason)
	for _, diagnostic := range result.Validation.Diagnostics {
		fmt.Printf("%s: %s\n", diagnostic.Severity, diagnostic.Summary)
	}
	return 0
}

func writeAPIClientRequest(writer interface{ Write([]byte) (int, error) }, request apiRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = writer.Write(append(data, '\n'))
	return err
}

func readAPIClientResponse(reader *bufio.Reader) (apiResponse, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return apiResponse{}, err
	}
	var response apiResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return apiResponse{}, err
	}
	if response.Type != "response" || response.ID == "" {
		return apiResponse{}, fmt.Errorf("invalid manager API response")
	}
	return response, nil
}
