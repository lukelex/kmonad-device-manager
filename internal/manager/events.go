package manager

import (
	"os"
	"reflect"
	"sort"
	"time"
)

type publicState struct {
	devices        map[string]Device
	configurations map[string]Configuration
	operations     map[string]Operation
}

const (
	maxRetainedEvents     = 1024
	eventSubscriberBuffer = 1024
)

const (
	publicEventMetricDeviceAdded = iota
	publicEventMetricDeviceAvailabilityChanged
	publicEventMetricConfigurationChanged
	publicEventMetricConfigurationRuntimeChanged
	publicEventMetricConfigurationRejected
	publicEventMetricConfigurationRolledBack
	publicEventMetricOperationChanged
	publicEventMetricDiagnosticChanged
	publicEventMetricManagerResyncRequired
	publicEventMetricCount
)

var publicEventMetricTypes = [...]EventType{
	EventDeviceAdded,
	EventDeviceAvailabilityChanged,
	EventConfigurationChanged,
	EventConfigurationRuntimeChanged,
	EventConfigurationRejected,
	EventConfigurationRolledBack,
	EventOperationChanged,
	EventDiagnosticChanged,
	EventManagerResyncRequired,
}

type eventSubscriber struct {
	events chan Event
	resync chan Event
}

type eventsSubscribeParams struct {
	AfterEventID  *uint64 `json:"after_event_id,omitempty"`
	AfterServerID string  `json:"after_server_id,omitempty"`
	forceResync   bool
}

type eventSubscription struct {
	ID            uint64
	StateRevision uint64
	LatestEventID uint64
	Replay        []Event
	Events        <-chan Event
	Resync        <-chan Event
	ResyncNeeded  bool
	ResyncEvent   Event
}

func (m *manager) publishEvent(eventType EventType, resource ResourceRef, reasonCode ReasonCode, data map[string]any) {
	m.advanceStateRevision()
	m.nextEventID++
	event := Event{EventID: m.nextEventID, StateRevision: m.stateRevision, Time: time.Now(), Type: eventType, Resource: resource, ReasonCode: reasonCode, Data: data}
	m.recordPublicEvent(event)
	m.events = append(m.events, event)
	if len(m.events) > maxRetainedEvents {
		m.events = append([]Event(nil), m.events[len(m.events)-maxRetainedEvents:]...)
	}
	for id, subscriber := range m.eventSubscribers {
		select {
		case subscriber.events <- event:
		default:
			delete(m.eventSubscribers, id)
			select {
			case subscriber.resync <- m.resyncRequiredEvent():
			default:
			}
		}
	}
}

func (m *manager) subscribeEvents(params eventsSubscribeParams) eventSubscription {
	after := m.nextEventID
	if params.AfterEventID != nil {
		after = *params.AfterEventID
	}
	if params.forceResync || (params.AfterEventID != nil && after > m.nextEventID) || (len(m.events) != 0 && m.events[0].EventID > 0 && after < m.events[0].EventID-1) {
		return eventSubscription{StateRevision: m.stateRevision, LatestEventID: m.nextEventID, ResyncNeeded: true, ResyncEvent: m.resyncRequiredEvent()}
	}
	replay := make([]Event, 0)
	for _, event := range m.events {
		if event.EventID > after {
			replay = append(replay, event)
		}
	}
	if m.eventSubscribers == nil {
		m.eventSubscribers = make(map[uint64]*eventSubscriber)
	}
	m.nextSubscriberID++
	subscriber := &eventSubscriber{events: make(chan Event, eventSubscriberBuffer), resync: make(chan Event, 1)}
	m.eventSubscribers[m.nextSubscriberID] = subscriber
	return eventSubscription{ID: m.nextSubscriberID, StateRevision: m.stateRevision, LatestEventID: m.nextEventID, Replay: replay, Events: subscriber.events, Resync: subscriber.resync}
}

func (m *manager) unsubscribeEvents(id uint64) {
	subscriber, exists := m.eventSubscribers[id]
	if !exists {
		return
	}
	delete(m.eventSubscribers, id)
	close(subscriber.events)
}

func (m *manager) eventList() []Event {
	events := append([]Event(nil), m.events...)
	sort.Slice(events, func(i, j int) bool { return events[i].EventID < events[j].EventID })
	return events
}

func (m *manager) resyncRequiredEvent() Event {
	event := Event{EventID: m.nextEventID, StateRevision: m.stateRevision, Time: time.Now(), Type: EventManagerResyncRequired,
		Resource: ResourceRef{Kind: ResourceManager, ID: "manager"}, ReasonCode: ReasonManagerResyncRequired,
		Data: map[string]any{"remediation": "fetch snapshot.get and resubscribe"}}
	m.recordPublicEvent(event)
	return event
}

func (m *manager) recordPublicEvent(event Event) {
	for index, eventType := range publicEventMetricTypes {
		if event.Type == eventType {
			m.publicEvents[index].Add(1)
			break
		}
	}
	if os.Getenv("KMONAD_LOG_FORMAT") != "json" {
		return
	}
	logEvent("manager_transition", "manager public state transition", map[string]any{
		"event_id":       event.EventID,
		"state_revision": event.StateRevision,
		"event_type":     event.Type,
		"resource_kind":  event.Resource.Kind,
		"resource_id":    event.Resource.ID,
		"reason_code":    event.ReasonCode,
	})
}

func (m *manager) capturePublicState() publicState {
	state := publicState{
		devices:        make(map[string]Device, len(m.devices)),
		configurations: make(map[string]Configuration, len(m.managedConfigs)+len(m.externalConfigs)),
		operations:     make(map[string]Operation, len(m.operations)),
	}
	for id, device := range m.devices {
		state.devices[id] = device
	}
	for id, configuration := range m.managedConfigs {
		state.configurations[id] = m.managedConfigurationResource(configuration)
	}
	for id, configuration := range m.externalConfigs {
		state.configurations[id] = m.externalConfigurationResource(configuration)
	}
	for id, operation := range m.operations {
		state.operations[id] = operation
	}
	return state
}

func (m *manager) publishStateChanges(before publicState) {
	after := m.capturePublicState()
	for id, device := range after.devices {
		previous, known := before.devices[id]
		resource := ResourceRef{Kind: ResourceDevice, ID: id}
		switch {
		case !known:
			m.publishEvent(EventDeviceAdded, resource, device.ReasonCode, map[string]any{})
		case !reflect.DeepEqual(previous, device):
			m.publishEvent(EventDeviceAvailabilityChanged, resource, device.ReasonCode, map[string]any{})
		}
	}
	for id, configuration := range after.configurations {
		previous, known := before.configurations[id]
		resource := ResourceRef{Kind: ResourceConfiguration, ID: id}
		if !known || previous.Ownership != configuration.Ownership || previous.Enabled != configuration.Enabled || previous.DeviceID != configuration.DeviceID || previous.DesiredRevision != configuration.DesiredRevision || previous.ActiveRevision != configuration.ActiveRevision {
			m.publishEvent(EventConfigurationChanged, resource, ReasonConfigurationDiscovered, map[string]any{})
		}
		if !known || !reflect.DeepEqual(previous.Runtime, configuration.Runtime) {
			m.publishEvent(EventConfigurationRuntimeChanged, resource, configuration.Runtime.ReasonCode, map[string]any{})
		}
	}
	for id := range before.configurations {
		if _, exists := after.configurations[id]; !exists {
			m.publishEvent(EventConfigurationChanged, ResourceRef{Kind: ResourceConfiguration, ID: id}, ReasonRuntimeStopped, map[string]any{})
		}
	}
	for id, operation := range after.operations {
		previous, known := before.operations[id]
		if known && reflect.DeepEqual(previous, operation) {
			continue
		}
		m.publishOperationChange(operation)
	}
}

func (m *manager) publishOperationChange(operation Operation) {
	m.publishEvent(EventOperationChanged, operation.Resource, operation.ReasonCode, map[string]any{})
	if operation.Resource.Kind != ResourceConfiguration {
		return
	}
	switch operation.State {
	case OperationRejected:
		m.publishEvent(EventConfigurationRejected, operation.Resource, operation.ReasonCode, map[string]any{})
	case OperationRolledBack:
		m.publishEvent(EventConfigurationRolledBack, operation.Resource, operation.ReasonCode, map[string]any{})
	}
}
