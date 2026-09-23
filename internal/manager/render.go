package manager

import (
	"errors"
	"strings"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

// renderManagedConfiguration resolves an opaque device ID and produces the
// complete platform-owned defcfg. Its output is internal: callers must still
// validate it and decide whether to persist or run it.
func (m *manager) renderManagedConfiguration(model ManagedConfigurationModel) ([]byte, ValidationResult) {
	if model.DeviceID == "" {
		return nil, renderRejected(ReasonConfigurationRevisionStale, "a device ID is required", "Select a currently discovered keyboard.", nil)
	}
	if containsManagerOwnedConfiguration(model.Behavior) {
		return nil, renderRejected(ReasonCandidateUnsupported, "behavior must not declare manager-owned KMonad configuration", "Remove defcfg, device-file, uinput-sink, and other input/output forms; the manager renders them.", nil)
	}
	m.refreshDevices()
	device, known := m.devices[model.DeviceID]
	if !known {
		return nil, renderRejected(ReasonConfigurationRevisionStale, "the selected device is no longer known", "Refresh devices and select a current keyboard.", &ResourceRef{Kind: ResourceDevice, ID: model.DeviceID})
	}
	if device.Role == DeviceRoleManagerOutput {
		return nil, renderRejected(ReasonDeviceManagerOutput, "the selected device is a manager-owned virtual output", "Select a physical keyboard input.", &ResourceRef{Kind: ResourceDevice, ID: model.DeviceID})
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
	defcfg, err := host.RenderKMonadDefcfg(matches[0].nodePath, managedOutputName(model.DeviceID))
	if err != nil {
		if errors.Is(err, platform.ErrKMonadOutputUnavailable) {
			return nil, renderBlocked(ReasonPlatformUnsupported, "the active platform backend cannot render a manager-owned KMonad output", "Use a platform backend with KMonad output support, then retry.", model.DeviceID)
		}
		return nil, renderBlocked(ReasonDeviceInaccessible, "the selected device cannot be rendered as a KMonad input/output configuration", "Check device access, then retry.", model.DeviceID)
	}
	behavior := strings.TrimSpace(model.Behavior)
	content := defcfg + "\n"
	if behavior != "" {
		content += behavior + "\n"
	}
	return []byte(content), ValidationResult{Outcome: ValidationValid, ReasonCode: ReasonValidationSucceeded, Reason: "device resolved and manager-owned input/output rendered"}
}

func containsManagerOwnedConfiguration(behavior string) bool {
	tokens, err := tokenizeConfig([]byte(behavior))
	if err != nil {
		return strings.Contains(behavior, "(defcfg") || strings.Contains(behavior, "device-file") || strings.Contains(behavior, "uinput-sink")
	}
	for _, token := range tokens {
		if token.kind != 's' {
			continue
		}
		switch token.value {
		case "defcfg", "device-file", "uinput-sink":
			return true
		}
	}
	return false
}

func managedOutputName(deviceID string) string {
	// Use a bounded digest of the opaque manager ID. It is stable across model
	// updates, unique enough for independently supervised devices, and does not
	// disclose the private input node to KMonad's virtual-device name.
	return "kmonad-device-manager-" + configurationDigest([]byte(deviceID))[:16]
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
