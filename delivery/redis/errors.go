package redis

import (
	"errors"
	"fmt"

	"github.com/kartaladev/hmntsk"
)

// The sink's error taxonomy. It mirrors the engine's shape in
// [github.com/kartaladev/hmntsk]: a sentinel a caller matches with [errors.Is],
// and a concrete type carrying the detail.
var (
	// ErrConfiguration reports a wiring mistake found by [New], before a single
	// event is published.
	ErrConfiguration = errors.New("redis: invalid configuration")

	// ErrInvalidEvent reports an event the sink cannot turn into a message.
	// It is the one failure no further attempt can change, and is the only
	// reason the sink ever returns [github.com/kartaladev/hmntsk/relay.Permanent].
	ErrInvalidEvent = errors.New("redis: invalid event")

	// ErrPublish reports that the broker did not accept the write. Every
	// instance is retryable; see [Sink.Deliver] for why.
	ErrPublish = errors.New("redis: publish failed")
)

// ConfigurationError reports a construction-time wiring mistake.
type ConfigurationError struct {
	// Detail says what is wrong and, where possible, how to fix it.
	Detail string
}

// Error implements the error interface.
func (e *ConfigurationError) Error() string {
	return "redis: invalid configuration: " + e.Detail
}

// Unwrap makes the error match [ErrConfiguration].
func (e *ConfigurationError) Unwrap() error { return ErrConfiguration }

// InvalidEventError reports an event that cannot be rendered as a message: one
// with no identifier, or one whose JSON payload will not marshal.
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
	msg := "redis: invalid event"
	if e.TaskID != "" {
		msg = fmt.Sprintf("redis: invalid event for task %s", e.TaskID)
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

// PublishError reports a write the broker did not accept: a broker that could
// not be reached, one that did not answer inside the attempt's timeout, and one
// that rejected the command are all this error.
//
// It names the stream and the event so that the last error recorded against a
// stuck outbox entry says which destination refused it, without a second
// lookup.
type PublishError struct {
	// Stream is the stream the event was being published to.
	Stream string
	// EventID is the event that was being published.
	EventID string
	// Cause is the client's own error.
	Cause error
}

// Error implements the error interface.
func (e *PublishError) Error() string {
	return fmt.Sprintf("redis: publishing event %s to stream %q: %v", e.EventID, e.Stream, e.Cause)
}

// Unwrap makes the error match [ErrPublish] and the client's own error, so a
// host can still inspect the cause — [context.DeadlineExceeded] for an attempt
// abandoned at its timeout, for instance.
func (e *PublishError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrPublish}
	}

	return []error{ErrPublish, e.Cause}
}
