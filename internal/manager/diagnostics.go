package manager

import "sort"

func (m *manager) publicDiagnostics() []Diagnostic {
	diagnostics := []Diagnostic{managerHealthDiagnostic(m.managerHealth()), kmonadDiagnostic(normalizedKMonadInfo(m.kmonad))}
	for _, device := range m.devices {
		diagnostics = append(diagnostics, deviceDiagnostic(device))
	}
	for _, configuration := range m.managedConfigs {
		diagnostics = append(diagnostics, configurationDiagnostic(m.managedConfigurationResource(configuration)))
	}
	for _, configuration := range m.externalConfigs {
		diagnostics = append(diagnostics, configurationDiagnostic(m.externalConfigurationResource(configuration)))
	}
	sort.Slice(diagnostics, func(i, j int) bool { return diagnostics[i].ID < diagnostics[j].ID })
	return diagnostics
}

func kmonadDiagnostic(info KMonadInfo) Diagnostic {
	diagnostic := Diagnostic{ID: "manager.kmonad", ReasonCode: info.ReasonCode, Summary: info.Reason,
		Resource: &ResourceRef{Kind: ResourceManager, ID: "manager"}}
	switch info.Compatibility {
	case KMonadCompatibilityCompatible:
		diagnostic.Severity = DiagnosticOK
		diagnostic.Remediation = "No action is required."
	case KMonadCompatibilityUnknown:
		diagnostic.Severity = DiagnosticWarning
		diagnostic.Remediation = "Install a KMonad release that reports a semantic version of 0.4.0 or newer."
	default:
		diagnostic.Severity = DiagnosticError
		diagnostic.Remediation = "Install KMonad 0.4.0 or newer and ensure it remains available to the user service."
	}
	return diagnostic
}

func managerHealthDiagnostic(health ManagerHealth) Diagnostic {
	diagnostic := Diagnostic{
		ID: "manager.health", ReasonCode: health.ReasonCode, Summary: health.Reason,
		Resource: &ResourceRef{Kind: ResourceManager, ID: "manager"},
	}
	if health.Healthy {
		diagnostic.Severity = DiagnosticOK
		diagnostic.Remediation = "No action is required."
	}
	if health.ReasonCode == ReasonManagerStarting {
		diagnostic.Severity = DiagnosticTemporary
		diagnostic.Remediation = "Wait for the manager to complete its first reconciliation."
	}
	if !health.Healthy {
		diagnostic.Severity = DiagnosticError
		diagnostic.Remediation = "Inspect manager logs and restart the user service if it does not recover."
	}
	return diagnostic
}

func deviceDiagnostic(device Device) Diagnostic {
	diagnostic := Diagnostic{
		ID: "device." + device.ID + ".availability", ReasonCode: device.ReasonCode,
		Summary: device.Reason, Resource: &ResourceRef{Kind: ResourceDevice, ID: device.ID},
	}
	switch device.Availability {
	case DeviceConnected:
		diagnostic.Severity = DiagnosticOK
		diagnostic.Remediation = "No action is required."
	case DeviceDisconnected:
		diagnostic.Severity = DiagnosticTemporary
		diagnostic.Remediation = "Reconnect the keyboard and wait for the manager to reconcile it."
	case DeviceConflicting:
		diagnostic.Severity = DiagnosticWarning
		diagnostic.Remediation = "Resolve the conflicting keyboard mapping before enabling another configuration."
	default:
		diagnostic.Severity = DiagnosticError
		diagnostic.Remediation = "Check keyboard support and access permissions."
	}
	return diagnostic
}

func configurationDiagnostic(configuration Configuration) Diagnostic {
	diagnostic := Diagnostic{
		ID: "configuration." + configuration.ID + ".runtime", ReasonCode: configuration.Runtime.ReasonCode,
		Summary: configuration.Runtime.Reason, Resource: &ResourceRef{Kind: ResourceConfiguration, ID: configuration.ID},
	}
	switch configuration.Runtime.Phase {
	case RuntimeRunning, RuntimeDisabled:
		diagnostic.Severity = DiagnosticOK
		diagnostic.Remediation = "No action is required."
	case RuntimeWaiting, RuntimeValidating, RuntimeApplying, RuntimeBackoff, RuntimeRecovering:
		diagnostic.Severity = DiagnosticTemporary
		diagnostic.Remediation = "Wait for the manager to retry, or reconnect the configured keyboard."
	case RuntimeDuplicate:
		diagnostic.Severity = DiagnosticWarning
		diagnostic.Remediation = "Assign this configuration to a keyboard not claimed by another configuration."
	default:
		diagnostic.Severity = DiagnosticError
		diagnostic.Remediation = "Inspect the configuration and its most recent operation before retrying."
	}
	return diagnostic
}
