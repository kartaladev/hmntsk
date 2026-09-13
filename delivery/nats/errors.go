package nats

import (
	"errors"
	"fmt"

	natsgo "github.com/nats-io/nats.go"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// The sinks' error taxonomy. It mirrors the engine's shape in
// [github.com/kartaladev/hmntsk]: a sentinel a caller matches with [errors.Is],
// and a concrete type carrying the detail.
var (
	// ErrConfiguration reports a wiring mistake found by [NewSink] or
	// [NewJetStreamSink], before a single event is published.
	ErrConfiguration = errors.New("nats: invalid configuration")

	// ErrInvalidEvent reports an event a sink cannot turn into a message. No
	// further attempt can change it, so it is always
	// [github.com/kartaladev/hmntsk/relay.Permanent].
	ErrInvalidEvent = errors.New("nats: invalid event")

	// ErrPublish reports that the server did not take a message. The outcome it
	// travels in says whether another attempt might: see [Sink.Deliver] and
	// [JetStreamSink.Deliver] for which causes are permanent.
	ErrPublish = errors.New("nats: publish failed")
)

// ConfigurationError reports a construction-time wiring mistake.
type ConfigurationError struct {
	// Detail says what is wrong and, where possible, how to fix it.
	Detail string
}

// Error implements the error interface.
func (e *ConfigurationError) Error() string {
	return "nats: invalid configuration: " + e.Detail
}

// Unwrap makes the error match [ErrConfiguration].
func (e *ConfigurationError) Unwrap() error { return ErrConfiguration }

// InvalidEventError reports an event that cannot be rendered as a message: one
// with no identifier, or one whose JSON body will not marshal.
//
// It is deliberately not retryable. The event is stored in the outbox and will
// be identical on every attempt, so a second pass would fail in exactly the
// same place.
type InvalidEventError struct {
	// TaskID is the task the event is about, where known.
	TaskID hmntsk.TaskID
	// Detail says what is wrong with the event.
	Detail string
	// Cause is the underlying failure, nil when the event is simply incomplete.
	Cause error
}

// Error implements the error interface.
func (e *InvalidEventError) Error() string {
	msg := "nats: invalid event"
	if e.TaskID != "" {
		msg = fmt.Sprintf("nats: invalid event for task %s", e.TaskID)
	}

	if e.Detail != "" {
		msg += ": " + e.Detail
	}

	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}

	return msg
}

// Unwrap makes the error match [ErrInvalidEvent] and, where there is one, the
// underlying cause.
func (e *InvalidEventError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrInvalidEvent}
	}

	return []error{ErrInvalidEvent, e.Cause}
}

// PublishError reports a message the server did not take: a connection that
// was closed or reconnecting, a server that did not confirm receipt inside the
// attempt's timeout, and a publication the server refused are all this error.
//
// It names the subject and the event so that the last error recorded against a
// stuck outbox entry says which destination refused it, without a second
// lookup.
type PublishError struct {
	// Subject is the subject the event was being published to.
	Subject string
	// EventID is the event that was being published.
	EventID string
	// Cause is the client's own error.
	Cause error
}

// Error implements the error interface.
func (e *PublishError) Error() string {
	return fmt.Sprintf("nats: publishing event %s to subject %q: %v", e.EventID, e.Subject, e.Cause)
}

// Unwrap makes the error match [ErrPublish] and the client's own error, so a
// host can still inspect the cause — [context.DeadlineExceeded] for a flush
// that did not complete in time, or jetstream.ErrNoStreamResponse for a subject
// no stream captures.
func (e *PublishError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrPublish}
	}

	return []error{ErrPublish, e.Cause}
}

// publishFailed classifies a publication of msg the server did not take.
//
// Two refusals are permanent, because the next pass would send the same bytes
// and meet the same refusal: a message larger than the server's maximum
// payload, and a subject the client will not publish to. The second cannot
// happen for a validated prefix and a catalogue event type, and is classified
// for completeness. Everything else is retryable, because it is a property of
// the connection or the server at that moment.
//
// That includes [natsgo.ErrHeadersNotSupported], which sounds permanent: the
// client returns it for every message with headers until the connection has
// completed its first connect, so a permanent verdict would dead-letter every
// event a host published while its server was still unreachable.
func publishFailed(msg *natsgo.Msg, cause error) relay.Outcome {
	err := &PublishError{Subject: msg.Subject, EventID: msg.Header.Get(HeaderEventID), Cause: cause}

	if errors.Is(cause, natsgo.ErrMaxPayload) || errors.Is(cause, natsgo.ErrBadSubject) {
		return relay.Permanent(err)
	}

	return relay.Retryable(err)
}
