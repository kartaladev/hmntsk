package nats

import (
	"encoding/json"
	"strconv"

	natsgo "github.com/nats-io/nats.go"

	"github.com/kartaladev/hmntsk/relay"
)

// message renders one attempt as the message both sinks publish: the subject
// for its event type, the routing headers, and the whole event as the body.
//
// The task type stays out of the subject. The registry accepts any non-empty
// type name, and one containing a wildcard, a dot or a space would publish to a
// subject that is refused, or silently to a different depth; a consumer filters
// on the task type header instead.
func message(subjectPrefix string, attempt relay.Attempt) (*natsgo.Msg, error) {
	event := attempt.Event

	if event.ID == "" {
		return nil, &InvalidEventError{
			TaskID: event.TaskID,
			Detail: "the event has no identifier, so neither a consumer nor a stream could de-duplicate it",
		}
	}

	body, err := json.Marshal(event)
	if err != nil {
		return nil, &InvalidEventError{
			TaskID: event.TaskID,
			Detail: "the event will not marshal to JSON",
			Cause:  err,
		}
	}

	// The client replaces a line break in a header value with a space, so a
	// value the host supplied cannot break the framing. The body keeps the
	// original, which is why it and not the header is authoritative.
	header := natsgo.Header{
		"Content-Type":   {ContentType},
		HeaderSchema:     {Schema},
		HeaderEventID:    {event.ID},
		HeaderDeliveryID: {attempt.DeliveryID},
		HeaderAttempt:    {strconv.Itoa(attempt.Number)},
		HeaderEventType:  {string(event.Type)},
		HeaderTaskID:     {string(event.TaskID)},
		HeaderTaskType:   {event.TaskType},
	}

	if value := event.Correlation.OwnerType; value != "" {
		header[HeaderOwnerType] = []string{value}
	}

	if value := event.Correlation.OwnerRef; value != "" {
		header[HeaderOwnerRef] = []string{value}
	}

	if value := event.Correlation.ActivityKey; value != "" {
		header[HeaderActivityKey] = []string{value}
	}

	return &natsgo.Msg{
		Subject: subjectPrefix + "." + string(event.Type),
		Header:  header,
		Data:    body,
	}, nil
}
