package manager

func (m *manager) managerInfo() ManagerInfo {
	return ManagerInfo{
		APIVersions:     []int{1},
		Platform:        host.Platform(),
		PlatformVersion: host.PlatformVersion(),
		Backend:         host.Backend(),
		BackendVersion:  host.BackendVersion(),
		KMonad:          normalizedKMonadInfo(m.kmonad),
		StateRevision:   m.stateRevision,
		EventCursor: EventCursor{
			EventID:       m.nextEventID,
			StateRevision: m.stateRevision,
		},
		Limits: ManagerLimits{
			MaxConfigurations:     m.maxConfigs,
			MaxConfigurationBytes: m.maxConfigBytes,
			CommandQueue:          managerCommandQueueSize,
			EventHistory:          maxRetainedEvents,
			EventSubscriberQueue:  eventSubscriberBuffer,
			APIMaxClients:         apiMaxClients,
			APIInFlightRequests:   apiMaxInFlight,
			APIFrameBytes:         apiFrameLimit,
			DefaultDeadlineMS:     apiDefaultDeadline.Milliseconds(),
		},
		Capabilities: managerCapabilities(),
		Limitations:  managerLimitations(),
		Health:       m.managerHealth(),
	}
}

func managerCapabilities() []Capability {
	return managerCapabilitiesFor(host.Platform(), host.Backend())
}

func managerCapabilitiesFor(platformName, backend string) []Capability {
	available := platformName == "linux" && backend == "linux-evdev"
	reasonCode, reason := ReasonCapabilityAvailable, "available on the Linux evdev backend"
	if !available {
		reasonCode, reason = ReasonPlatformUnsupported, "not available on this platform backend"
	}
	return []Capability{
		{Name: CapabilityDeviceDiscovery, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityDeviceIdentification, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityCandidateValidation, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityManagedConfigurations, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityExternalConfigurationAdoption, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityEventStream, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityMultipleIndependentKeyboards, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityAutomaticHotplugRecovery, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityPerDeviceMapping, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityInputTargetDeviceFile, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityConfigurationContentRead, Available: available, ReasonCode: reasonCode, Reason: reason},
		{Name: CapabilityConfigurationExport, Available: available, ReasonCode: reasonCode, Reason: reason},
	}
}

func managerLimitations() []FeatureLimitation {
	return []FeatureLimitation{
		{ID: "platform.linux_evdev_only", ReasonCode: ReasonPlatformUnsupported, Summary: "Only the Linux evdev backend is currently supported.", Remediation: "Use the manager on Linux with evdev input devices."},
		{ID: "input.device_file_only", ReasonCode: ReasonCandidateUnsupported, Summary: "Only KMonad device-file input targets are supported.", Remediation: "Use a device-file input or a manager-owned configuration model."},
		{ID: "configuration.external_read_only", ReasonCode: ReasonConfigurationExternalReadOnly, Summary: "External configurations remain read-only until explicitly adopted.", Remediation: "Adopt a representable external configuration before editing it through the manager."},
		{ID: "api.local_same_user_only", ReasonCode: ReasonOperationUnsupported, Summary: "The manager API is available only through the same-user local Unix socket.", Remediation: "Run a client as the manager user on the same host."},
	}
}
