package webhook

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kartaladev/hmntsk"
)

// The headers every delivery carries. A receiver can route, correlate and
// de-duplicate on these without reading the body at all, which is what lets a
// gateway or a queue-filler sit in front of the real consumer.
const (
	// HeaderEventID carries the event's identifier. It is stable across every
	// attempt at the same event, so it is what a receiver de-duplicates on.
	HeaderEventID = "Hmntsk-Event-Id"
	// HeaderDeliveryID carries this attempt's identifier. It is fresh for every
	// attempt, so two of them carrying one event identifier is precisely how a
	// receiver knows it has seen a repeat rather than a new event.
	HeaderDeliveryID = "Hmntsk-Delivery-Id"
	// HeaderEventType carries the event's catalogue type, such as
	// "task.completed".
	HeaderEventType = "Hmntsk-Event-Type"
	// HeaderTaskID carries the task the event is about.
	HeaderTaskID = "Hmntsk-Task-Id"
	// HeaderTaskType carries the task's registered type name.
	HeaderTaskType = "Hmntsk-Task-Type"
	// HeaderOwnerType carries the correlation owner type the host supplied.
	HeaderOwnerType = "Hmntsk-Correlation-Owner-Type"
	// HeaderOwnerRef carries the correlation owner reference the host supplied.
	HeaderOwnerRef = "Hmntsk-Correlation-Owner-Ref"
	// HeaderActivityKey carries the correlation activity key the host supplied.
	HeaderActivityKey = "Hmntsk-Correlation-Activity-Key"
)

// HeaderTimestamp carries the instant a delivery was signed, as seconds since
// the Unix epoch. It is part of the signed material, so it cannot be moved
// forward to refresh a captured delivery.
const HeaderTimestamp = "Hmntsk-Timestamp"

// HeaderSignature carries the signature over the timestamp and the body, in the
// form "v1=" followed by lowercase hexadecimal. See [Sign].
const HeaderSignature = "Hmntsk-Signature"

// ContentType is the media type of every delivery body.
const ContentType = "application/json; charset=utf-8"

// Payload is the JSON body of one delivery.
type Payload struct {
	// DeliveryID identifies this attempt, and no other.
	DeliveryID string `json:"deliveryId"`
	// DeliveredAt is when the attempt was made and signed. It is the same
	// instant the timestamp header carries.
	DeliveredAt time.Time `json:"deliveredAt"`
	// Event is what happened.
	Event PayloadEvent `json:"event"`
	// Correlation is what the host supplied at creation, echoed unchanged.
	Correlation hmntsk.CorrelationData `json:"correlation,omitzero"`
	// ReferenceParameters are the caller's own bytes, echoed exactly as they
	// were supplied.
	ReferenceParameters json.RawMessage `json:"referenceParameters,omitempty"`
}

// PayloadEvent is the event a delivery reports.
type PayloadEvent struct {
	// ID identifies the event, and is stable across redeliveries.
	ID string `json:"id"`
	// Type is the catalogue member this event belongs to.
	Type hmntsk.EventType `json:"type"`
	// TaskID is the task the event is about.
	TaskID hmntsk.TaskID `json:"taskId"`
	// TaskType is the task's registered type name.
	TaskType string `json:"taskType"`
	// Status is the task's status after the transition.
	Status hmntsk.Status `json:"status"`
	// Version is the task's version after the transition.
	Version int64 `json:"version"`
	// Actor is who performed the operation, empty when the system acted.
	Actor string `json:"actor,omitempty"`
	// Assignee is the task's assignee after the transition.
	Assignee string `json:"assignee,omitempty"`
	// Candidates is the task's candidate pool after the transition, exclusions
	// included. Groups are named, never expanded into members.
	Candidates hmntsk.CandidatePool `json:"candidates,omitzero"`
	// PreviousAssignee is the holder the transition replaced, present only on
	// a release or a delegation.
	PreviousAssignee string `json:"previousAssignee,omitempty"`
	// CreatedBy is who created the task.
	CreatedBy string `json:"createdBy,omitempty"`
	// OccurredAt is when the transition happened.
	OccurredAt time.Time `json:"occurredAt"`
	// Reason is the free text supplied with a failure, cancellation or fault.
	Reason string `json:"reason,omitempty"`
	// Output is the completion payload, present only on a completion.
	Output json.RawMessage `json:"output,omitempty"`
}

// MarshalJSON implements [json.Marshaler], and renders the body the sink sends.
//
// It splices the reference parameters into the body as the bytes they are,
// rather than letting encoding/json re-encode them. That is not a
// micro-optimisation: [hmntsk.CallbackTarget] promises the caller that the
// engine never reads, reorders or reformats these bytes, and re-encoding
// reformats them — it strips the caller's whitespace, and a caller who chose a
// canonical form to sign or hash over would find the form changed underneath
// them. The engine's own framing is encoding/json's; the caller's framing stays
// the caller's.
//
// The sink calls this method directly rather than through [json.Marshal],
// because json.Marshal compacts whatever a Marshaler returns and so would undo
// the splice. Anyone rendering a Payload for the wire should do the same; the
// difference is confined to whitespace, since compaction preserves names, order
// and number literals.
func (p Payload) MarshalJSON() ([]byte, error) {
	parameters := p.ReferenceParameters
	p.ReferenceParameters = nil

	// The conversion drops this method, so the marshalling below is the
	// ordinary struct one rather than a recursive call.
	type body Payload

	encoded, err := json.Marshal(body(p))
	if err != nil {
		return nil, fmt.Errorf("webhook: encode delivery body: %w", err)
	}

	if len(parameters) == 0 {
		return encoded, nil
	}

	if !json.Valid(parameters) {
		return nil, fmt.Errorf("%w: they cannot be echoed inside a json body", ErrReferenceParameters)
	}

	const field = `"referenceParameters":`

	out := make([]byte, 0, len(encoded)+len(parameters)+len(field)+2)
	out = append(out, encoded[:len(encoded)-1]...)

	if len(encoded) > len("{}") {
		out = append(out, ',')
	}

	out = append(out, field...)
	out = append(out, parameters...)

	return append(out, '}'), nil
}

// newPayload renders an event as the body of one delivery attempt.
func newPayload(event hmntsk.Event, deliveryID string, deliveredAt time.Time) Payload {
	payload := Payload{
		DeliveryID:  deliveryID,
		DeliveredAt: deliveredAt,
		Event: PayloadEvent{
			ID:               event.ID,
			Type:             event.Type,
			TaskID:           event.TaskID,
			TaskType:         event.TaskType,
			Status:           event.Status,
			Version:          event.Version,
			Actor:            event.Actor,
			Assignee:         event.Assignee,
			Candidates:       event.Candidates,
			PreviousAssignee: event.PreviousAssignee,
			CreatedBy:        event.CreatedBy,
			OccurredAt:       event.OccurredAt,
			Reason:           event.Reason,
			Output:           event.Output,
		},
		Correlation: event.Correlation,
	}

	if event.Callback != nil {
		payload.ReferenceParameters = event.Callback.ReferenceParameters
	}

	return payload
}
