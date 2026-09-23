package manager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func snapshotCLI(arguments []string, jsonOutput bool) int {
	if len(arguments) != 0 {
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "snapshot does not accept arguments")
		return 2
	}
	data, apiErr, err := requestManagerAPI("snapshot.get", map[string]any{})
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "manager_unavailable", err.Error())
		return 1
	}
	if apiErr != nil {
		writeCLIError(os.Stderr, jsonOutput, apiErr.Code, apiErr.Message)
		return 1
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "invalid_response", "manager returned an invalid snapshot")
		return 1
	}
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(snapshot); err != nil {
			writeCLIError(os.Stderr, true, "output_failed", err.Error())
			return 1
		}
		return 0
	}
	fmt.Printf("REVISION: %d\nHEALTHY: %t\nDEVICES: %d\nCONFIGURATIONS: %d\nOPERATIONS: %d\n", snapshot.StateRevision, snapshot.Health.Healthy, len(snapshot.Devices), len(snapshot.Configurations), len(snapshot.Operations))
	return 0
}

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

func applyCLI(arguments []string, jsonOutput bool) int {
	if len(arguments) < 1 {
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "apply requires MODEL_FILE and --name NAME for a new configuration")
		return 2
	}
	limit := loadSettings().maxConfigBytes
	if limit <= 0 {
		limit = defaultMaxConfigBytes
	}
	data, err := readFileLimited(arguments[0], limit)
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "candidate_unreadable", err.Error())
		return 1
	}
	var model ManagedConfigurationModel
	if err := json.Unmarshal(data, &model); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "MODEL_FILE must contain a managed configuration JSON object")
		return 2
	}
	params := configurationApplyParams{Model: model}
	for index := 1; index < len(arguments); index += 2 {
		if index+1 >= len(arguments) {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "apply options require a value")
			return 2
		}
		switch arguments[index] {
		case "--name":
			params.Name = arguments[index+1]
		case "--id":
			params.ConfigurationID = arguments[index+1]
		case "--revision":
			revision, err := strconv.ParseUint(arguments[index+1], 10, 64)
			if err != nil || revision == 0 {
				writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "--revision must be a positive integer")
				return 2
			}
			params.ExpectedRevision = &revision
		default:
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "apply accepts only --name, --id, and --revision")
			return 2
		}
	}
	operation, apiErr, err := requestIdentificationOperation("configuration.apply", params)
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
	fmt.Printf("ID: %s\nSTATE: %s\nCONFIGURATION: %s\nREASON CODE: %s\nREASON: %s\n", operation.ID, operation.State, operation.Resource.ID, operation.ReasonCode, operation.Reason)
	return 0
}

func configCLI(arguments []string, jsonOutput bool) int {
	if len(arguments) == 0 {
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "config requires list, create, update, enable, disable, delete, or adopt")
		return 2
	}
	if arguments[0] == "list" {
		if len(arguments) != 1 {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "config list does not accept arguments")
			return 2
		}
		return listConfigurationsCLI(jsonOutput)
	}
	var method string
	var params any
	switch arguments[0] {
	case "create":
		if len(arguments) != 4 || arguments[2] != "--name" {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "config create requires MODEL_FILE --name NAME")
			return 2
		}
		model, code := readManagedModelCLI(arguments[1], jsonOutput)
		if code != 0 {
			return code
		}
		method, params = "configuration.create", configurationApplyParams{Name: arguments[3], Model: model}
	case "update":
		if len(arguments) != 4 && (len(arguments) != 6 || arguments[4] != "--name") {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "config update requires CONFIGURATION_ID REVISION MODEL_FILE and an optional --name NAME")
			return 2
		}
		revision, err := strconv.ParseUint(arguments[2], 10, 64)
		if err != nil || revision == 0 {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "REVISION must be a positive integer")
			return 2
		}
		model, code := readManagedModelCLI(arguments[3], jsonOutput)
		if code != 0 {
			return code
		}
		params := configurationApplyParams{ConfigurationID: arguments[1], ExpectedRevision: &revision, Model: model}
		if len(arguments) == 6 {
			params.Name = arguments[5]
		}
		method = "configuration.update"
	case "enable", "disable", "delete":
		if len(arguments) != 3 {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "config "+arguments[0]+" requires CONFIGURATION_ID REVISION")
			return 2
		}
		revision, err := strconv.ParseUint(arguments[2], 10, 64)
		if err != nil || revision == 0 {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "REVISION must be a positive integer")
			return 2
		}
		if arguments[0] == "delete" {
			method, params = "configuration.delete", configurationDeleteParams{ConfigurationID: arguments[1], ExpectedRevision: revision}
		} else {
			method, params = "configuration.set_enabled", configurationSetEnabledParams{ConfigurationID: arguments[1], ExpectedRevision: revision, Enabled: arguments[0] == "enable"}
		}
	case "adopt":
		if len(arguments) != 2 && (len(arguments) != 4 || arguments[2] != "--name") {
			writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "config adopt requires EXTERNAL_CONFIGURATION_ID and an optional --name NAME")
			return 2
		}
		adopt := configurationAdoptParams{ConfigurationID: arguments[1]}
		if len(arguments) == 4 {
			adopt.Name = arguments[3]
		}
		method, params = "configuration.adopt", adopt
	default:
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "config requires list, create, update, enable, disable, delete, or adopt")
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
	fmt.Printf("ID: %s\nSTATE: %s\nCONFIGURATION: %s\nREVISION: %d\nREASON CODE: %s\nREASON: %s\n", operation.ID, operation.State, operation.Resource.ID, operation.ConfigurationRevision, operation.ReasonCode, operation.Reason)
	return 0
}

func listConfigurationsCLI(jsonOutput bool) int {
	data, apiErr, err := requestManagerAPI("configuration.list", map[string]any{})
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "manager_unavailable", err.Error())
		return 1
	}
	if apiErr != nil {
		writeCLIError(os.Stderr, jsonOutput, apiErr.Code, apiErr.Message)
		return 1
	}
	var result struct {
		Configurations []Configuration `json:"configurations"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "invalid_response", "manager returned an invalid configuration list")
		return 1
	}
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			writeCLIError(os.Stderr, true, "output_failed", err.Error())
			return 1
		}
		return 0
	}
	for _, configuration := range result.Configurations {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", configuration.ID, configuration.Ownership, configuration.Runtime.Phase, configuration.Runtime.ReasonCode, configuration.Name)
	}
	return 0
}

func readManagedModelCLI(path string, jsonOutput bool) (ManagedConfigurationModel, int) {
	limit := loadSettings().maxConfigBytes
	if limit <= 0 {
		limit = defaultMaxConfigBytes
	}
	data, err := readFileLimited(path, limit)
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "candidate_unreadable", err.Error())
		return ManagedConfigurationModel{}, 1
	}
	var model ManagedConfigurationModel
	if err := json.Unmarshal(data, &model); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "invalid_arguments", "MODEL_FILE must contain a managed configuration JSON object")
		return ManagedConfigurationModel{}, 2
	}
	return model, 0
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
