package redis

import (
	"errors"
	"fmt"
)

// The broadcaster's error taxonomy: a sentinel a caller matches with
// [errors.Is], and a concrete type carrying the detail. It mirrors
// delivery/redis, copied rather than imported.
var (
	// ErrConfiguration reports a wiring mistake found by [NewBroadcaster],
	// before a single signal is published.
	ErrConfiguration = errors.New("redis: invalid configuration")

	// ErrPublish reports that the broker did not take a message. The notify
	// service hands it to its signal error handler; it never fails the
	// notification write that produced the signal.
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

// PublishError reports a message the broker did not take: a broker that could
// not be reached, one that did not answer inside the publish timeout, and one
// that rejected the command are all this error.
type PublishError struct {
	// Channel is the channel the message was being published to.
	Channel string
	// Signals is how many signals the message carried.
	Signals int
	// Cause is the client's own error.
	Cause error
}

// Error implements the error interface.
func (e *PublishError) Error() string {
	return fmt.Sprintf("redis: publishing %d signals to channel %q: %v", e.Signals, e.Channel, e.Cause)
}

// Unwrap makes the error match [ErrPublish] and the client's own error, so a
// host can still inspect the cause, such as [context.DeadlineExceeded] for a
// publish abandoned at its timeout.
func (e *PublishError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrPublish}
	}

	return []error{ErrPublish, e.Cause}
}
