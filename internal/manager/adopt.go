package manager

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

type configurationAdoptParams struct {
	ConfigurationID string `json:"configuration_id"`
	Name            string `json:"name,omitempty"`
}

func (m *manager) prepareExternalAdoption(ctx context.Context, params configurationAdoptParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	m.refreshExternalConfigurationRegistry()
	external, exists := m.externalConfigs[params.ConfigurationID]
	if !exists {
		return commandResult{err: &apiError{Code: "not_found", Message: "external configuration does not exist"}}
	}
	if external.AdoptedBy != "" {
		return commandResult{err: &apiError{Code: "conflict", Message: "external configuration is already represented by a managed configuration"}}
	}
	data, err := readFileLimited(external.Path, m.configurationByteLimit())
	if err != nil || configurationDigest(data) != external.Signature {
		return commandResult{err: &apiError{Code: "configuration_changed", Message: "external configuration changed; refresh it before adoption"}}
	}
	model, err := managedModelFromExternal(data, external.DeviceID)
	if err != nil {
		return commandResult{err: &apiError{Code: "candidate_unsupported", Message: err.Error()}}
	}
	name := params.Name
	if name == "" {
		name = external.Name
	}
	return m.prepareManagedApply(ctx, configurationApplyParams{
		Name: name, Model: model, adoptExternalID: external.ID, operationKind: OperationAdopt,
	})
}

func managedModelFromExternal(content []byte, deviceID string) (ManagedConfigurationModel, error) {
	if deviceID == "" {
		return ManagedConfigurationModel{}, fmt.Errorf("external configuration device is unavailable or not representable")
	}
	start := bytes.Index(content, []byte("(defcfg"))
	if start < 0 {
		return ManagedConfigurationModel{}, fmt.Errorf("external configuration has no representable defcfg input form")
	}
	end, ok := sExpressionEnd(content, start)
	if !ok {
		return ManagedConfigurationModel{}, fmt.Errorf("external configuration has an unterminated defcfg form")
	}
	tokens, err := tokenizeConfig(content[start:end])
	if err != nil || !canonicalInputDefcfg(tokens) {
		return ManagedConfigurationModel{}, fmt.Errorf("external configuration defcfg has options that cannot be adopted losslessly")
	}
	behavior := strings.TrimSpace(string(append(append([]byte{}, content[:start]...), content[end:]...)))
	if containsManagerOwnedConfiguration(behavior) {
		return ManagedConfigurationModel{}, fmt.Errorf("external configuration contains additional manager-owned configuration that cannot be adopted losslessly")
	}
	return ManagedConfigurationModel{DeviceID: deviceID, Behavior: behavior}, nil
}

func canonicalInputDefcfg(tokens []configToken) bool {
	return len(tokens) == 8 && tokens[0].kind == '(' && tokens[1].kind == 's' && tokens[1].value == "defcfg" &&
		tokens[2].kind == 's' && tokens[2].value == "input" && tokens[3].kind == '(' &&
		tokens[4].kind == 's' && tokens[4].value == "device-file" && tokens[5].kind == 'q' &&
		tokens[6].kind == ')' && tokens[7].kind == ')'
}

func sExpressionEnd(content []byte, start int) (int, bool) {
	depth := 0
	inString := false
	for index := start; index < len(content); index++ {
		switch content[index] {
		case '\\':
			if inString {
				index++
			}
		case '"':
			inString = !inString
		case ';':
			if !inString {
				for index < len(content) && content[index] != '\n' {
					index++
				}
			}
		case '(':
			if !inString {
				depth++
			}
		case ')':
			if !inString {
				depth--
				if depth == 0 {
					return index + 1, true
				}
			}
		}
	}
	return 0, false
}

func adoptExternalConfiguration(ctx context.Context, owner *manager, params configurationAdoptParams) commandResult {
	prepared := owner.submitCommand(ctx, func(commandContext context.Context, m *manager) commandResult {
		return m.prepareExternalAdoption(commandContext, params)
	})
	if prepared.err != nil || prepared.result == nil {
		return prepared
	}
	if operation, ok := prepared.result.(map[string]Operation); ok {
		return commandResult{result: operation}
	}
	preparation, ok := prepared.result.(managedApplyPreparation)
	if !ok {
		return commandResult{err: &apiError{Code: "internal", Message: "adoption preparation failed"}}
	}
	validation := runManagedApplyValidation(ctx, preparation)
	finishContext := ctx
	if ctx.Err() != nil {
		finishContext = context.Background()
	}
	return owner.submitCommand(finishContext, func(commandContext context.Context, m *manager) commandResult {
		return m.finishManagedApply(commandContext, preparation, validation)
	})
}
