package manager

import (
	"context"
	"encoding/json"
)

func previewValidation(ctx context.Context, owner *manager, raw json.RawMessage) commandResult {
	var params validationPreviewParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "invalid validation.preview parameters"}}
	}
	prepared := owner.submitCommand(ctx, func(commandContext context.Context, m *manager) commandResult {
		return m.prepareValidationPreview(commandContext, params)
	})
	if prepared.err != nil {
		return prepared
	}
	if result, ok := prepared.result.(ValidationResult); ok {
		return commandResult{result: map[string]ValidationResult{"validation": result}}
	}
	preparation, ok := prepared.result.(validationPreparation)
	if !ok {
		return commandResult{err: &apiError{Code: "internal", Message: "validation preparation failed"}}
	}
	result := runPreparedValidation(ctx, preparation)
	return commandResult{result: map[string]ValidationResult{"validation": result}}
}
