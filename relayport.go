package hmntsk

import (
	"context"
	"time"
)

// DefaultOutboxLease is how long a relay holds an event by default.
//
// It bounds how long a crashed relay strands an event, and it has to outlast a
// pass: a lease that expires while the relay is still working through its batch
// lets a second instance claim an event the first is about to deliver, and
// at-least-once becomes at-least-twice more often than it needs to.
const DefaultOutboxLease = 5 * time.Minute

// ClaimDueEvents takes a time-bounded lease on a batch of events that are due
// for delivery and returns them, oldest first.
//
// It is exposed so that the relay — which lives in its own package, and may
// equally be a host's own scheduler — can claim without reaching into the
// engine's internals, and so that the claiming and the delivering sit in
// separate transactions: the lease has to be committed before anything is
// attempted, or a second instance would not see it.
//
// A zero Now means the engine's clock, a zero Duration [DefaultOutboxLease] and
// a zero Limit [DefaultOutboxBatch].
func (s *Service) ClaimDueEvents(ctx context.Context, claim OutboxClaim) ([]OutboxEntry, error) {
	if claim.Duration <= 0 {
		claim.Duration = DefaultOutboxLease
	}

	if claim.Limit <= 0 {
		claim.Limit = DefaultOutboxBatch
	}

	if claim.Now.IsZero() {
		claim.Now = s.clock.Now()
	}

	var claimed []OutboxEntry

	err := s.store.Do(ctx, func(ctx context.Context) error {
		var claimErr error

		claimed, claimErr = s.store.ClaimDueEvents(ctx, claim)

		return claimErr
	})
	if err != nil {
		return nil, err
	}

	return claimed, nil
}

// RecordDeliveryAttempt writes the outcome of a failed but retryable attempt
// and releases the lease, so that the event is attempted again once it is due.
func (s *Service) RecordDeliveryAttempt(ctx context.Context, record AttemptRecord) error {
	return s.store.Do(ctx, func(ctx context.Context) error {
		return s.store.RecordAttempt(ctx, record)
	})
}

// MarkEventAccepted records which sinks have taken an event, marking it
// delivered only once every configured sink has, and releases the lease.
func (s *Service) MarkEventAccepted(ctx context.Context, acceptance Acceptance) error {
	return s.store.Do(ctx, func(ctx context.Context) error {
		return s.store.MarkAccepted(ctx, acceptance)
	})
}

// MarkEventDeadLettered stops further attempts on an event and releases the
// lease, retaining the event with its attempt count and last error.
func (s *Service) MarkEventDeadLettered(ctx context.Context, letter DeadLetter) error {
	return s.store.Do(ctx, func(ctx context.Context) error {
		return s.store.MarkDeadLettered(ctx, letter)
	})
}

// OutboxEntry reads one recorded event and its delivery state. It returns an
// error matching [ErrNotFound] when there is no such event.
//
// It is how a dead letter is inspected, which is the only thing the engine
// promises about one: the rows are queryable, and tooling to replay them is
// deliberately not in scope.
func (s *Service) OutboxEntry(ctx context.Context, eventID string) (OutboxEntry, error) {
	return s.store.OutboxEntry(ctx, eventID)
}

// Clock returns the engine's clock, so that anything scheduling against the
// engine — a relay computing a next-attempt time, a host's own driver —
// measures from the same instant the engine does rather than from its own
// [time.Now].
func (s *Service) Clock() Clock { return s.clock }

// NewEventID mints an identifier from the same monotonic generator the engine
// stamps events with.
//
// A relay uses it for the owner it records in the leases it takes, and again
// for each delivery attempt it hands a sink, so that a receiver can tell a
// redelivery of an event it has already seen from a new one.
func (s *Service) NewEventID() (string, error) {
	id, err := s.eventIDs.NewTaskID()
	if err != nil {
		return "", err
	}

	return id.String(), nil
}
