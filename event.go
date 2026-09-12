package hmntsk

import (
	"encoding/json"
	"time"
)

// EventType names one member of the closed event catalogue. The catalogue
// covers every accepted lifecycle transition and nothing else: there is exactly
// one type per transition, and no operation invents a type outside this set.
type EventType string

// The closed event catalogue.
const (
	// EventTypeCreated reports a task that now exists and has been assigned or
	// pooled.
	EventTypeCreated EventType = "task.created"
	// EventTypeClaimed reports a task reserved by an eligible actor.
	EventTypeClaimed EventType = "task.claimed"
	// EventTypeReleased reports a task returned to its pool.
	EventTypeReleased EventType = "task.released"
	// EventTypeStarted reports work begun on a task.
	EventTypeStarted EventType = "task.started"
	// EventTypeDelegated reports a task reassigned to another actor.
	EventTypeDelegated EventType = "task.delegated"
	// EventTypeCompleted reports a task closed with an output. It says nothing
	// about whether the outcome was favourable; read the output for that.
	EventTypeCompleted EventType = "task.completed"
	// EventTypeFailed reports that the actor could not do the work.
	EventTypeFailed EventType = "task.failed"
	// EventTypeSuspended reports a task withdrawn from circulation.
	EventTypeSuspended EventType = "task.suspended"
	// EventTypeResumed reports a suspended task returned to circulation.
	EventTypeResumed EventType = "task.resumed"
	// EventTypeEscalated reports a task that passed its deadline and had its
	// policy applied. The engine notifies nobody; that is a consumer's job.
	EventTypeEscalated EventType = "task.escalated"
	// EventTypeCancelled reports a task closed at its owner's request.
	EventTypeCancelled EventType = "task.cancelled"
	// EventTypeObsoleted reports a task superseded by escalation.
	EventTypeObsoleted EventType = "task.obsoleted"
	// EventTypeErrored reports a system-originated fault that closed a task.
	EventTypeErrored EventType = "task.errored"
)

// eventCatalogue lists every published event type, in lifecycle order.
var eventCatalogue = [...]EventType{
	EventTypeCreated,
	EventTypeClaimed,
	EventTypeReleased,
	EventTypeStarted,
	EventTypeDelegated,
	EventTypeCompleted,
	EventTypeFailed,
	EventTypeSuspended,
	EventTypeResumed,
	EventTypeEscalated,
	EventTypeCancelled,
	EventTypeObsoleted,
	EventTypeErrored,
}

// EventTypes returns the closed catalogue of event types, in lifecycle order.
func EventTypes() []EventType {
	out := make([]EventType, len(eventCatalogue))
	copy(out, eventCatalogue[:])

	return out
}

// String implements [fmt.Stringer].
func (e EventType) String() string { return string(e) }

// Valid reports whether e is a member of the published catalogue.
func (e EventType) Valid() bool {
	for _, candidate := range eventCatalogue {
		if e == candidate {
			return true
		}
	}

	return false
}

// operationEventTypes maps each event-producing operation to its event type.
// Operations absent from this map produce no event: SaveProgress is the only
// one, because no consumer is blocked on a draft.
var operationEventTypes = map[Operation]EventType{
	OpCreate:   EventTypeCreated,
	OpClaim:    EventTypeClaimed,
	OpRelease:  EventTypeReleased,
	OpStart:    EventTypeStarted,
	OpDelegate: EventTypeDelegated,
	OpComplete: EventTypeCompleted,
	OpFail:     EventTypeFailed,
	OpSuspend:  EventTypeSuspended,
	OpResume:   EventTypeResumed,
	OpEscalate: EventTypeEscalated,
	OpCancel:   EventTypeCancelled,
	OpObsolete: EventTypeObsoleted,
	OpFault:    EventTypeErrored,
}

// EventTypeForOperation returns the event an operation produces, and false for
// an operation that produces none.
func EventTypeForOperation(op Operation) (EventType, bool) {
	et, ok := operationEventTypes[op]

	return et, ok
}

// Event is what the engine tells the outside world. The engine publishes; it
// never calls host business logic, workflow engines or use cases directly, so
// adding a consumer requires no change inside the engine.
//
// An event names no caller-specific type. Everything a consumer needs to route
// it is in Correlation, which the host supplied at creation and the engine
// echoes unchanged.
type Event struct {
	// ID identifies this event. It is assigned when the event is recorded, not
	// when the transition is computed, so that transitions stay pure.
	ID string `json:"id,omitempty"`
	// Type is the catalogue member this event belongs to.
	Type EventType `json:"type"`
	// TaskID is the task the event is about.
	TaskID TaskID `json:"taskId"`
	// TaskType is the task's registered type name, so a consumer can filter
	// without reading the task.
	TaskType string `json:"taskType"`
	// Status is the task's status after the transition.
	Status Status `json:"status"`
	// Version is the task's version after the transition.
	Version int64 `json:"version"`
	// Actor is who performed the operation, empty when the system acted.
	Actor string `json:"actor,omitempty"`
	// Assignee is the task's assignee after the transition, empty when it sits
	// in the pool.
	Assignee string `json:"assignee,omitempty"`
	// OccurredAt is when the transition happened, UTC at microsecond precision.
	OccurredAt time.Time `json:"occurredAt"`
	// Correlation ties the event back to the work that asked for the task.
	Correlation CorrelationData `json:"correlation,omitzero"`
	// Callback is the task's callback target, carried so that a delivering
	// consumer has the address and the reference parameters to echo.
	Callback *CallbackTarget `json:"callback,omitempty"`
	// Transition is the audit record for the transition that produced this
	// event. Exactly one event and one record are produced per transition.
	Transition TransitionRecord `json:"transition"`
	// Reason is the free text supplied with a failure, cancellation, fault or
	// supersession.
	Reason string `json:"reason,omitempty"`
	// Output is the completion payload, present only on
	// [EventTypeCompleted]. It is the caller's bytes, unaltered.
	Output json.RawMessage `json:"output,omitempty"`
}
