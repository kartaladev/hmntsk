package notify

import "time"

// Clock is the library's source of time. It has the same method set as the
// task engine's clock, so a host passes its engine clock unchanged.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// SystemClock is the default [Clock]. Its zero value is ready to use.
type SystemClock struct{}

// Now implements [Clock].
func (SystemClock) Now() time.Time { return time.Now() }

// ClockFunc adapts a function to the [Clock] interface.
type ClockFunc func() time.Time

// Now implements [Clock].
func (f ClockFunc) Now() time.Time { return f() }

// normalizeTime is the precision every store keeps: UTC, microseconds. MySQL
// and PostgreSQL both store microseconds, so an instant written with more reads
// back as a different value, and comparisons stop agreeing across stores.
func normalizeTime(instant time.Time) time.Time {
	return instant.UTC().Truncate(time.Microsecond)
}
