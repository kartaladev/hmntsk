package nats

import (
	"errors"
	"fmt"

	"github.com/kartaladev/hmntsk/notify"
)

// The broadcaster's error taxonomy: a sentinel a caller matches with
// [errors.Is], and a concrete type carrying the detail. It mirrors
// delivery/nats, copied rather than imported.
var (
	// ErrConfiguration reports a wiring mistake found by [NewBroadcaster],
	// before a single signal is published.
	ErrConfiguration = errors.New("nats: invalid configuration")

	// ErrPublish reports that the connection did not take a message. The notify
	// service hands it to its signal error handler; it never fails the
	// notification write that produced the signal.
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

// Unwrap makes the error match both [ErrConfiguration] and
// [notify.ErrConfiguration]: the broadcaster is a [notify.Broadcaster], and a
// mistake wiring it is a notify wiring mistake too.
func (e *ConfigurationError) Unwrap() []error {
	return []error{ErrConfiguration, notify.ErrConfiguration}
}

// PublishError reports a message the connection did not take: a closed
// connection, and a reconnect buffer that overflowed while the server was away,
// are both this error.
type PublishError struct {
	// Subject is the subject the message was being published to.
	Subject string
	// Signals is how many signals the message carried.
	Signals int
	// Cause is the client's own error.
	Cause error
}

// Error implements the error interface.
func (e *PublishError) Error() string {
	return fmt.Sprintf("nats: publishing %d signals to subject %q: %v", e.Signals, e.Subject, e.Cause)
}

// Unwrap makes the error match [ErrPublish] and the client's own error, such as
// nats.ErrConnectionClosed.
func (e *PublishError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrPublish}
	}

	return []error{ErrPublish, e.Cause}
}
