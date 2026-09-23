package manager

func (m *manager) managerInfo() ManagerInfo {
	return ManagerInfo{
		APIVersions:   []int{1},
		Platform:      host.Platform(),
		Backend:       host.Backend(),
		KMonad:        normalizedKMonadInfo(m.kmonad),
		StateRevision: m.stateRevision,
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
		Health:       m.managerHealth(),
	}
}

func managerCapabilities() []Capability {
	return []Capability{
		{Name: CapabilityDeviceDiscovery, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "keyboard inventory is available"},
		{Name: CapabilityDeviceIdentification, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "keypress identification is available"},
		{Name: CapabilityCandidateValidation, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "candidate validation is available"},
		{Name: CapabilityManagedConfigurations, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "transactional managed configuration apply is available"},
		{Name: CapabilityExternalConfigurationAdoption, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "lossless external configuration adoption is available"},
		{Name: CapabilityEventStream, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "ordered retained event streaming is available"},
		{Name: CapabilityMultipleIndependentKeyboards, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "independent .kbd supervision is active"},
		{Name: CapabilityAutomaticHotplugRecovery, Available: true, ReasonCode: ReasonCapabilityAvailable, Reason: "configured devices are reconciled after reconnect"},
	}
}
