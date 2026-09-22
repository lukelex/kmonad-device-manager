package manager

import "strings"

// renderManagedConfiguration resolves an opaque device ID and produces the
// manager-owned defcfg input form. Its output is internal: callers must still
// validate it and decide whether to persist or run it.
func (m *manager) renderManagedConfiguration(model ManagedConfigurationModel) ([]byte, ValidationResult) {
	if model.DeviceID == "" {
		return nil, renderRejected(ReasonConfigurationRevisionStale, "a device ID is required", "Select a currently discovered keyboard.", nil)
	}
	if containsInputConfiguration(model.Behavior) {
		return nil, renderRejected(ReasonCandidateUnsupported, "behavior must not declare a KMonad input target", "Remove defcfg and device-file forms; the manager renders the input target.", nil)
	}
	m.refreshDevices()
	device, known := m.devices[model.DeviceID]
	if !known {
		return nil, renderRejected(ReasonConfigurationRevisionStale, "the selected device is no longer known", "Refresh devices and select a current keyboard.", &ResourceRef{Kind: ResourceDevice, ID: model.DeviceID})
	}
	if device.Availability != DeviceConnected {
		return nil, renderBlocked(device.ReasonCode, device.Reason, "Reconnect or fix access to the selected keyboard, then retry.", model.DeviceID)
	}
	discovered, err := discoverKeyboardDevices()
	if err != nil {
		return nil, renderBlocked(ReasonDependencyUnavailable, "keyboard discovery is unavailable", "Retry after Linux input discovery is available.", model.DeviceID)
	}
	matches := make([]discoveredKeyboard, 0, 1)
	for _, candidate := range discovered {
		if candidate.device.ID == model.DeviceID {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return nil, renderRejected(ReasonConfigurationRevisionStale, "the selected device changed before it could be resolved", "Refresh devices and select the keyboard again.", &ResourceRef{Kind: ResourceDevice, ID: model.DeviceID})
	}
	if len(matches) != 1 {
		return nil, renderBlocked(ReasonDeviceIdentityAmbiguous, "the selected device resolves to multiple input interfaces", "Disconnect duplicate devices or select an unambiguous keyboard.", model.DeviceID)
	}
	input, err := host.RenderKMonadInput(matches[0].nodePath)
	if err != nil {
		return nil, renderBlocked(ReasonDeviceInaccessible, "the selected device cannot be rendered as a KMonad input", "Check device access, then retry.", model.DeviceID)
	}
	behavior := strings.TrimSpace(model.Behavior)
	content := "(defcfg\n  " + input + "\n)\n"
	if behavior != "" {
		content += behavior + "\n"
	}
	return []byte(content), ValidationResult{Outcome: ValidationValid, ReasonCode: ReasonValidationSucceeded, Reason: "device resolved and input target rendered"}
}

func containsInputConfiguration(behavior string) bool {
	return strings.Contains(behavior, "(defcfg") || strings.Contains(behavior, "device-file")
}

func renderRejected(code ReasonCode, reason, remediation string, resource *ResourceRef) ValidationResult {
	return ValidationResult{Outcome: ValidationRejected, ReasonCode: code, Reason: reason, Diagnostics: []Diagnostic{{
		ID: "configuration.render", Severity: DiagnosticError, ReasonCode: code, Summary: reason, Remediation: remediation, Resource: resource,
	}}}
}

func renderBlocked(code ReasonCode, reason, remediation, deviceID string) ValidationResult {
	return ValidationResult{Outcome: ValidationBlocked, ReasonCode: code, Reason: reason, Diagnostics: []Diagnostic{{
		ID: "device.resolution", Severity: DiagnosticTemporary, ReasonCode: code, Summary: reason, Remediation: remediation,
		Resource: &ResourceRef{Kind: ResourceDevice, ID: deviceID},
	}}}
}
