package hmntsk

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// DefaultOutboxBatch is how many due events one relay pass claims by default.
//
// It is deliberately smaller than [DefaultSweepBatch]: every claimed event is a
// network call per sink, and a pass that claims more than it can deliver inside
// its lease hands the remainder back to the next pass anyway.
const DefaultOutboxBatch = 50

// OutboxNotFoundError reports that no event with that identifier is recorded.
type OutboxNotFoundError struct {
	// EventID is the identifier that was looked up.
	EventID string
}

// Error implements the error interface.
func (e *OutboxNotFoundError) Error() string {
	return fmt.Sprintf("hmntsk: outbox entry %s not found", e.EventID)
}

// Unwrap makes the error match [ErrNotFound].
func (e *OutboxNotFoundError) Unwrap() error { return ErrNotFound }

// OutboxClaim asks for a batch of events that are due for delivery to be
// claimed exclusively.
//
// It is the relay's counterpart to [LeaseRequest], and takes a lease the same
// way for the same reason: one supported dialect has no row-level locking, so
// exclusivity has to come from a conditional update rather than a lock.
type OutboxClaim struct {
	// Now is the instant the relay considers current. An event is due when its
	// next-attempt time is at or before it.
	Now time.Time
	// Owner identifies the relay, so that an abandoned lease is traceable.
	Owner string
	// Duration is how long the lease holds. Once it expires the event is
	// claimable again, which is what stops a crashed relay from stranding it.
	Duration time.Duration
	// Limit caps how many events one pass claims. Zero means the
	// implementation's default.
	Limit int
}

// OutboxEntry is one durable event together with everything the relay knows
// about delivering it.
//
// The event itself is stored whole; the fields around it are the delivery state
// the relay reads and writes. An entry is in exactly one of three conditions,
// and [OutboxEntry.Delivered], [OutboxEntry.DeadLettered] and
// [OutboxEntry.Pending] name them.
type OutboxEntry struct {
	// Event is the recorded event, exactly as it was written.
	Event Event
	// Attempts is how many delivery attempts have been made.
	Attempts int
	// NextAttemptAt is when the event becomes due again. It is nil once the
	// event has been dead-lettered: there is no next attempt.
	NextAttemptAt *time.Time
	// LastError is the failure recorded by the most recent attempt, empty when
	// none has failed.
	//
	// It is text for a person to read, not a format to parse. The relay writes
	// one "<sink>: <message>" part per sink that refused the attempt, in the
	// order the sinks are configured, joined with "; " — and a sink's message
	// may itself contain that separator. Which sinks took the event is
	// [OutboxEntry.Accepted]. A sink's typed error, to match with errors.Is or
	// errors.As, exists only while the pass runs, through the relay's error
	// handler (relay.WithRelayErrorHandler); per-sink failures are not stored.
	LastError string
	// Accepted names the sinks that have taken this event. A retry targets only
	// the sinks absent from it.
	Accepted []string
	// PublishedAt is when every configured sink had accepted the event, and is
	// nil until then.
	PublishedAt *time.Time
	// LockedBy identifies the relay currently holding the delivery lease.
	LockedBy string
	// LockedUntil is when that lease expires. A lease whose deadline has passed
	// is available to any relay.
	LockedUntil *time.Time
}

// Delivered reports whether every configured sink has accepted the event.
func (e OutboxEntry) Delivered() bool { return e.PublishedAt != nil }

// DeadLettered reports whether the event will not be attempted again.
//
// A dead letter is distinguishable from a delivered event and from a pending
// one without a column of its own: it is the entry that has no next attempt and
// was never published. That is the shape design decision D5 asks for — a state
// on the row, retained with its attempt count and last error.
func (e OutboxEntry) DeadLettered() bool {
	return e.NextAttemptAt == nil && e.PublishedAt == nil
}

// Pending reports whether the event is still awaiting delivery.
func (e OutboxEntry) Pending() bool {
	return e.NextAttemptAt != nil && e.PublishedAt == nil
}

// IsLeased reports whether a relay currently holds this entry.
func (e OutboxEntry) IsLeased(now time.Time) bool {
	return e.LockedUntil != nil && now.Before(*e.LockedUntil)
}

// HasAccepted reports whether the named sink has already taken this event.
func (e OutboxEntry) HasAccepted(sink string) bool {
	return slices.Contains(e.Accepted, sink)
}

// AttemptRecord is the outcome of one delivery attempt that is to be tried
// again.
//
// Recording it releases the lease: the event is not this relay's any more, and
// the next pass that finds it due may take it.
type AttemptRecord struct {
	// EventID names the event the attempt was made for.
	EventID string
	// Attempts is the attempt count after this attempt.
	Attempts int
	// NextAttemptAt is when the event becomes due again.
	NextAttemptAt time.Time
	// LastError describes the failure, and replaces whatever was recorded
	// before. It is human-readable text in the shape described on
	// [OutboxEntry.LastError], not a format to parse.
	LastError string
}

// Acceptance records which sinks have taken an event.
//
// Accepted is the whole set, not a delta, so that recording it twice is
// harmless — which matters because a relay can crash between delivering and
// recording. Recording an acceptance releases the lease.
type Acceptance struct {
	// EventID names the event.
	EventID string
	// Accepted is every sink that has now taken the event.
	Accepted []string
	// PublishedAt is set only once every configured sink has accepted. Until
	// then the event stays claimable, because it is not yet delivered.
	PublishedAt *time.Time
	// NextAttemptAt is when the event becomes due for the sinks that have not
	// accepted it. It is ignored when PublishedAt is set.
	NextAttemptAt *time.Time
	// Attempts is the attempt count after this attempt.
	Attempts int
	// LastError describes the failure of the sinks that did not accept, empty
	// when all of them did.
	LastError string
}

// DeadLetter records that an event will not be attempted again, because its
// attempts are exhausted or a sink reported a permanent failure.
//
// It releases the lease and clears the next-attempt time. The row stays, with
// its attempt count and its last error, so that "what failed, and why?" is a
// query rather than a log search.
type DeadLetter struct {
	// EventID names the event.
	EventID string
	// Attempts is the attempt count after the attempt that exhausted it.
	Attempts int
	// LastError is the failure that ended it. It is human-readable text in the
	// shape described on [OutboxEntry.LastError], not a format to parse.
	LastError string
	// Accepted is every sink that took the event before it was given up on,
	// including any that took it on this final attempt.
	//
	// It is recorded even though nothing will read it to decide a retry,
	// because inspecting a dead letter is the only thing the engine promises
	// about one: a row claiming no destination received an event that one
	// demonstrably did is the answer an operator would replay from.
	Accepted []string
}

// OutboxStore is the relay's view of the durable event record: the read and the
// four writes that move an event from recorded to delivered or dead-lettered.
//
// It is separate from [EventSink], which is the producing side, because the two
// have different callers: the engine appends inside the transaction that
// produced the event, and a relay — possibly in another process — claims and
// settles afterwards.
type OutboxStore interface {
	// ClaimDueEvents takes a time-bounded lease on up to Limit events that are
	// due for delivery and returns them, oldest first. It must work without
	// row-level locking, because one supported dialect has none.
	//
	// An event is due when it is neither delivered nor dead-lettered, its
	// next-attempt time has passed, and no live lease is held on it.
	ClaimDueEvents(ctx context.Context, claim OutboxClaim) ([]OutboxEntry, error)
	// RecordAttempt writes the outcome of a failed but retryable attempt and
	// releases the lease.
	RecordAttempt(ctx context.Context, record AttemptRecord) error
	// MarkAccepted records the sinks that have taken an event, marking it
	// delivered only when every configured sink has, and releases the lease.
	MarkAccepted(ctx context.Context, acceptance Acceptance) error
	// MarkDeadLettered stops further attempts on an event, retaining it with
	// its attempt count and last error, and releases the lease.
	MarkDeadLettered(ctx context.Context, letter DeadLetter) error
	// OutboxEntry reads one entry by event identifier. It returns an error
	// matching [ErrNotFound] when there is no such event.
	//
	// It exists so that a dead letter is inspectable, which is the only thing
	// the engine promises about one.
	OutboxEntry(ctx context.Context, eventID string) (OutboxEntry, error)
}
