package hmntsk_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// outboxReference is the instant the outbox cases measure from.
var outboxReference = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// seedOutbox commits one event to the store's outbox and returns its entry.
func seedOutbox(t *testing.T, store *memstore.Store, event hmntsk.Event) hmntsk.OutboxEntry {
	t.Helper()

	ctx, done := store.ContextWithTx(t.Context())
	require.NoError(t, store.Append(ctx, []hmntsk.Event{event}))
	done(true)

	entry, err := store.OutboxEntry(t.Context(), event.ID)
	require.NoError(t, err, "the appended event must be readable as an outbox entry")

	return entry
}

// inOutboxTx runs fn inside a committed transaction, which is what every
// outbox write requires — the same discipline the repository methods keep.
func inOutboxTx(t *testing.T, store *memstore.Store, fn func(ctx context.Context) error) {
	t.Helper()

	ctx, done := store.ContextWithTx(t.Context())

	err := fn(ctx)
	done(err == nil)

	require.NoError(t, err)
}

// newOutboxEvent builds an event recorded at an offset from the reference.
func newOutboxEvent(id string, offset time.Duration) hmntsk.Event {
	return hmntsk.Event{
		ID:         id,
		Type:       hmntsk.EventTypeCreated,
		TaskID:     hmntsk.TaskID("task-" + id),
		TaskType:   "approval",
		Status:     hmntsk.StatusReady,
		OccurredAt: outboxReference.Add(offset),
	}
}

// TestOutboxEntryRoundTripsItsDeliveryState is the red test of task 1.1: an
// appended event comes back carrying the attempt count, next-attempt time, last
// error, lease fields and per-sink acceptance the relay needs, and each of the
// four settlements is readable afterwards.
func TestOutboxEntryRoundTripsItsDeliveryState(t *testing.T) {
	t.Parallel()

	t.Run("a freshly appended event is pending and due at once", func(t *testing.T) {
		t.Parallel()

		store := memstore.New()
		entry := seedOutbox(t, store, newOutboxEvent("e1", 0))

		assert.Equal(t, "e1", entry.Event.ID, "the event travels with its delivery state")
		assert.Equal(t, 0, entry.Attempts, "nothing has been attempted yet")
		assert.Empty(t, entry.LastError, "nothing has failed yet")
		assert.Empty(t, entry.Accepted, "no sink has taken it")
		assert.Empty(t, entry.LockedBy, "no relay holds it")
		assert.Nil(t, entry.LockedUntil)
		assert.Nil(t, entry.PublishedAt)
		assert.True(t, entry.Pending(), "a new event is pending")
		assert.False(t, entry.Delivered())
		assert.False(t, entry.DeadLettered())

		if assert.NotNil(t, entry.NextAttemptAt, "a new event has a next-attempt time") {
			assert.False(t, entry.NextAttemptAt.After(entry.Event.OccurredAt),
				"a new event is due as soon as it is recorded")
		}
	})

	t.Run("claiming takes a lease and returns the entry", func(t *testing.T) {
		t.Parallel()

		store := memstore.New()
		seedOutbox(t, store, newOutboxEvent("e1", 0))

		var claimed []hmntsk.OutboxEntry

		inOutboxTx(t, store, func(ctx context.Context) error {
			var err error
			claimed, err = store.ClaimDueEvents(ctx, hmntsk.OutboxClaim{
				Now:      outboxReference.Add(time.Minute),
				Owner:    "relay-1",
				Duration: time.Minute,
				Limit:    10,
			})

			return err
		})
		require.Len(t, claimed, 1, "the due event is claimed")

		assert.Equal(t, "relay-1", claimed[0].LockedBy, "the claim records its owner")
		require.NotNil(t, claimed[0].LockedUntil)
		assert.Equal(t, outboxReference.Add(2*time.Minute), claimed[0].LockedUntil.UTC(),
			"the lease runs for the requested duration")
	})

	t.Run("a recorded attempt keeps the event pending and readable", func(t *testing.T) {
		t.Parallel()

		store := memstore.New()
		seedOutbox(t, store, newOutboxEvent("e1", 0))

		due := outboxReference.Add(5 * time.Minute)
		inOutboxTx(t, store, func(ctx context.Context) error {
			return store.RecordAttempt(ctx, hmntsk.AttemptRecord{
				EventID:       "e1",
				Attempts:      1,
				NextAttemptAt: due,
				LastError:     "receiver returned 503",
			})
		})

		entry, err := store.OutboxEntry(t.Context(), "e1")
		require.NoError(t, err)

		assert.Equal(t, 1, entry.Attempts)
		assert.Equal(t, "receiver returned 503", entry.LastError)
		assert.True(t, entry.Pending(), "a retryable failure leaves the event pending")
		require.NotNil(t, entry.NextAttemptAt)
		assert.Equal(t, due, entry.NextAttemptAt.UTC())
		assert.Empty(t, entry.LockedBy, "recording an attempt releases the lease")
	})

	t.Run("acceptance is tracked per sink and completes only when all have taken it", func(t *testing.T) {
		t.Parallel()

		store := memstore.New()
		seedOutbox(t, store, newOutboxEvent("e1", 0))

		due := outboxReference.Add(time.Minute)
		inOutboxTx(t, store, func(ctx context.Context) error {
			return store.MarkAccepted(ctx, hmntsk.Acceptance{
				EventID:       "e1",
				Accepted:      []string{"webhook"},
				NextAttemptAt: &due,
				Attempts:      1,
				LastError:     "bus unreachable",
			})
		})

		entry, err := store.OutboxEntry(t.Context(), "e1")
		require.NoError(t, err)

		assert.Equal(t, []string{"webhook"}, entry.Accepted)
		assert.True(t, entry.HasAccepted("webhook"))
		assert.False(t, entry.HasAccepted("bus"))
		assert.False(t, entry.Delivered(), "one sink of two is not delivery")
		assert.True(t, entry.Pending(), "it remains claimable for the sink that failed")

		published := outboxReference.Add(2 * time.Minute)
		inOutboxTx(t, store, func(ctx context.Context) error {
			return store.MarkAccepted(ctx, hmntsk.Acceptance{
				EventID:     "e1",
				Accepted:    []string{"webhook", "bus"},
				PublishedAt: &published,
				Attempts:    2,
			})
		})

		entry, err = store.OutboxEntry(t.Context(), "e1")
		require.NoError(t, err)

		assert.True(t, entry.Delivered(), "every sink has now accepted")
		assert.False(t, entry.Pending())
		assert.False(t, entry.DeadLettered())
	})

	t.Run("a dead letter is retained with its attempt count and last error", func(t *testing.T) {
		t.Parallel()

		store := memstore.New()
		seedOutbox(t, store, newOutboxEvent("e1", 0))

		inOutboxTx(t, store, func(ctx context.Context) error {
			return store.MarkDeadLettered(ctx, hmntsk.DeadLetter{
				EventID:   "e1",
				Attempts:  5,
				LastError: "receiver returned 400",
			})
		})

		entry, err := store.OutboxEntry(t.Context(), "e1")
		require.NoError(t, err, "a dead letter remains stored")

		assert.True(t, entry.DeadLettered())
		assert.False(t, entry.Pending())
		assert.False(t, entry.Delivered())
		assert.Equal(t, 5, entry.Attempts)
		assert.Equal(t, "receiver returned 400", entry.LastError)

		var claimed []hmntsk.OutboxEntry

		inOutboxTx(t, store, func(ctx context.Context) error {
			var err error
			claimed, err = store.ClaimDueEvents(ctx, hmntsk.OutboxClaim{
				Now:      outboxReference.Add(time.Hour),
				Owner:    "relay-1",
				Duration: time.Minute,
			})

			return err
		})
		assert.Empty(t, claimed, "no further pass attempts a dead letter")
	})

	t.Run("an unknown event is not found", func(t *testing.T) {
		t.Parallel()

		_, err := memstore.New().OutboxEntry(t.Context(), "nope")
		assert.ErrorIs(t, err, hmntsk.ErrNotFound)
	})
}
