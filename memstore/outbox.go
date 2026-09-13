package memstore

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/kartaladev/hmntsk"
)

// outboxRow is one durable event together with the relay's delivery state for
// it. It is the in-memory equivalent of the columns the SQL adapters add to
// task_outbox.
type outboxRow struct {
	event         hmntsk.Event
	attempts      int
	nextAttemptAt *time.Time
	lastError     string
	accepted      []string
	publishedAt   *time.Time
	lockedBy      string
	lockedUntil   *time.Time
}

// entry converts a row into the port's view of it, copying everything a caller
// could otherwise mutate through a shared slice or pointer.
func (r outboxRow) entry() hmntsk.OutboxEntry {
	return hmntsk.OutboxEntry{
		Event:         r.event,
		Attempts:      r.attempts,
		NextAttemptAt: copyTime(r.nextAttemptAt),
		LastError:     r.lastError,
		Accepted:      slices.Clone(r.accepted),
		PublishedAt:   copyTime(r.publishedAt),
		LockedBy:      r.lockedBy,
		LockedUntil:   copyTime(r.lockedUntil),
	}
}

// due reports whether the row is claimable at now: still pending, past its
// next-attempt time, and not held by a live lease.
func (r outboxRow) due(now time.Time) bool {
	if r.publishedAt != nil || r.nextAttemptAt == nil {
		return false
	}

	if r.lockedUntil != nil && now.Before(*r.lockedUntil) {
		return false
	}

	return !r.nextAttemptAt.After(now)
}

// copyTime returns a copy of a timestamp pointer, so that a caller holding the
// result cannot write through it into the store.
func copyTime(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}

	out := *at

	return &out
}

// outboxSnapshot returns every row as the scope on ctx sees it: committed rows
// with this scope's edits applied, followed by the rows it appended itself.
func (s *Store) outboxSnapshot(ctx context.Context) []outboxRow {
	transaction := txFrom(ctx)

	s.dataMu.RLock()
	rows := slices.Clone(s.outbox)
	s.dataMu.RUnlock()

	if transaction == nil {
		return rows
	}

	for i, row := range rows {
		if edited, ok := transaction.outboxEdits[row.event.ID]; ok {
			rows[i] = edited
		}
	}

	return append(rows, transaction.outbox...)
}

// OutboxEntry implements [hmntsk.OutboxStore]. It reads through an active
// transaction when there is one, so a scope sees its own uncommitted writes.
func (s *Store) OutboxEntry(ctx context.Context, eventID string) (hmntsk.OutboxEntry, error) {
	for _, row := range s.outboxSnapshot(ctx) {
		if row.event.ID == eventID {
			return row.entry(), nil
		}
	}

	return hmntsk.OutboxEntry{}, &hmntsk.OutboxNotFoundError{EventID: eventID}
}

// ClaimDueEvents implements [hmntsk.OutboxStore]. The lease is taken by a
// conditional update on the lease fields, never by a row lock, so the same
// mechanism works on a dialect that has no locking at all.
//
// Events are claimed oldest first, by the time they occurred and then by
// identifier, so that a backlog drains in the order it accumulated.
func (s *Store) ClaimDueEvents(ctx context.Context, claim hmntsk.OutboxClaim) ([]hmntsk.OutboxEntry, error) {
	transaction := txFrom(ctx)
	if transaction == nil {
		return nil, errOutsideTransaction("ClaimDueEvents")
	}

	now := hmntsk.NormalizeTime(claim.Now)

	limit := claim.Limit
	if limit <= 0 {
		limit = hmntsk.DefaultOutboxBatch
	}

	candidates := s.outboxSnapshot(ctx)
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.event.OccurredAt.Equal(right.event.OccurredAt) {
			return left.event.ID < right.event.ID
		}

		return left.event.OccurredAt.Before(right.event.OccurredAt)
	})

	claimed := make([]hmntsk.OutboxEntry, 0, limit)

	for _, row := range candidates {
		if len(claimed) == limit {
			break
		}

		if !row.due(now) {
			continue
		}

		until := now.Add(claim.Duration)
		row.lockedBy = claim.Owner
		row.lockedUntil = &until

		transaction.outboxEdits[row.event.ID] = row
		claimed = append(claimed, row.entry())
	}

	return claimed, nil
}

// RecordAttempt implements [hmntsk.OutboxStore].
func (s *Store) RecordAttempt(ctx context.Context, record hmntsk.AttemptRecord) error {
	return s.editOutbox(ctx, "RecordAttempt", record.EventID, func(row outboxRow) outboxRow {
		due := hmntsk.NormalizeTime(record.NextAttemptAt)

		row.attempts = record.Attempts
		row.nextAttemptAt = &due
		row.lastError = record.LastError

		return release(row)
	})
}

// MarkAccepted implements [hmntsk.OutboxStore].
//
// Accepted replaces the recorded set rather than adding to it, so recording the
// same acceptance twice is harmless — which matters, because a relay can crash
// between delivering and recording.
func (s *Store) MarkAccepted(ctx context.Context, acceptance hmntsk.Acceptance) error {
	return s.editOutbox(ctx, "MarkAccepted", acceptance.EventID, func(row outboxRow) outboxRow {
		row.accepted = slices.Clone(acceptance.Accepted)
		row.attempts = acceptance.Attempts
		row.lastError = acceptance.LastError

		if acceptance.PublishedAt != nil {
			published := hmntsk.NormalizeTime(*acceptance.PublishedAt)
			row.publishedAt = &published
			row.lastError = ""

			return release(row)
		}

		if acceptance.NextAttemptAt != nil {
			due := hmntsk.NormalizeTime(*acceptance.NextAttemptAt)
			row.nextAttemptAt = &due
		}

		return release(row)
	})
}

// MarkDeadLettered implements [hmntsk.OutboxStore]. Clearing the next-attempt
// time is what marks the event dead: it is the entry that has no next attempt
// and was never published.
func (s *Store) MarkDeadLettered(ctx context.Context, letter hmntsk.DeadLetter) error {
	return s.editOutbox(ctx, "MarkDeadLettered", letter.EventID, func(row outboxRow) outboxRow {
		row.attempts = letter.Attempts
		row.lastError = letter.LastError
		row.nextAttemptAt = nil

		if letter.Accepted != nil {
			row.accepted = slices.Clone(letter.Accepted)
		}

		return release(row)
	})
}

// release drops the delivery lease. Every settlement releases it: the event is
// not this relay's any more, whatever the outcome was.
func release(row outboxRow) outboxRow {
	row.lockedBy = ""
	row.lockedUntil = nil

	return row
}

// editOutbox stages a change to one row inside the active transaction.
func (s *Store) editOutbox(
	ctx context.Context,
	operation string,
	eventID string,
	edit func(outboxRow) outboxRow,
) error {
	transaction := txFrom(ctx)
	if transaction == nil {
		return errOutsideTransaction(operation)
	}

	for _, row := range s.outboxSnapshot(ctx) {
		if row.event.ID != eventID {
			continue
		}

		edited := edit(row)

		// A row this scope appended itself is not yet committed, so the edit
		// belongs with the append rather than in the edit set.
		for i, staged := range transaction.outbox {
			if staged.event.ID == eventID {
				transaction.outbox[i] = edited

				return nil
			}
		}

		transaction.outboxEdits[eventID] = edited

		return nil
	}

	return &hmntsk.OutboxNotFoundError{EventID: eventID}
}
