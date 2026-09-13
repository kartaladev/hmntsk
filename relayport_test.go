package hmntsk_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// newOutboxService returns a service over an in-memory store with one event
// already recorded, and the store itself.
func newOutboxService(t *testing.T) (*hmntsk.Service, *memstore.Store) {
	t.Helper()

	store := memstore.New()

	svc, err := hmntsk.New(store, hmntsk.WithClock(hmntsk.ClockFunc(func() time.Time {
		return outboxReference
	})))
	require.NoError(t, err)

	return svc, store
}

// TestServiceOpensItsOwnTransactionForTheRelay covers the plumbing the relay
// package reaches the outbox through. The relay runs outside any transaction of
// the host's, exactly as the sweeper does, so the service has to open one — and
// the lease has to be committed before delivery is attempted, or a second
// instance would not see it.
func TestServiceOpensItsOwnTransactionForTheRelay(t *testing.T) {
	t.Parallel()

	t.Run("claiming needs no transaction from the caller", func(t *testing.T) {
		t.Parallel()

		svc, store := newOutboxService(t)
		seedOutbox(t, store, newOutboxEvent("e1", 0))

		claimed, err := svc.ClaimDueEvents(t.Context(), hmntsk.OutboxClaim{
			Owner:    "relay-1",
			Duration: time.Minute,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		// Committed, not merely staged: a second claim must find it held.
		again, err := svc.ClaimDueEvents(t.Context(), hmntsk.OutboxClaim{
			Owner:    "relay-2",
			Duration: time.Minute,
		})
		require.NoError(t, err)
		assert.Empty(t, again, "the lease is visible to another relay at once")
	})

	t.Run("an unset claim takes the engine's defaults", func(t *testing.T) {
		t.Parallel()

		svc, store := newOutboxService(t)
		seedOutbox(t, store, newOutboxEvent("e1", 0))

		claimed, err := svc.ClaimDueEvents(t.Context(), hmntsk.OutboxClaim{Owner: "relay-1"})
		require.NoError(t, err)
		require.Len(t, claimed, 1, "a zero Now means the clock's now, and a zero Limit the default batch")

		require.NotNil(t, claimed[0].LockedUntil)
		assert.Equal(t, outboxReference.Add(hmntsk.DefaultOutboxLease), claimed[0].LockedUntil.UTC(),
			"a zero Duration means the default lease")
	})

	t.Run("each settlement commits on its own", func(t *testing.T) {
		t.Parallel()

		svc, store := newOutboxService(t)
		seedOutbox(t, store, newOutboxEvent("e1", 0))

		require.NoError(t, svc.RecordDeliveryAttempt(t.Context(), hmntsk.AttemptRecord{
			EventID:       "e1",
			Attempts:      1,
			NextAttemptAt: outboxReference.Add(time.Minute),
			LastError:     "receiver returned 503",
		}))

		entry, err := svc.OutboxEntry(t.Context(), "e1")
		require.NoError(t, err)
		assert.Equal(t, 1, entry.Attempts)
		assert.Equal(t, "receiver returned 503", entry.LastError)

		published := outboxReference.Add(time.Minute)
		require.NoError(t, svc.MarkEventAccepted(t.Context(), hmntsk.Acceptance{
			EventID:     "e1",
			Accepted:    []string{"webhook"},
			PublishedAt: &published,
			Attempts:    2,
		}))

		entry, err = svc.OutboxEntry(t.Context(), "e1")
		require.NoError(t, err)
		assert.True(t, entry.Delivered())

		require.NoError(t, svc.MarkEventDeadLettered(t.Context(), hmntsk.DeadLetter{
			EventID:   "e1",
			Attempts:  3,
			LastError: "gave up",
		}))

		entry, err = svc.OutboxEntry(t.Context(), "e1")
		require.NoError(t, err)
		assert.Equal(t, "gave up", entry.LastError)
	})

	t.Run("the relay can read the clock and mint identifiers", func(t *testing.T) {
		t.Parallel()

		svc, _ := newOutboxService(t)

		assert.Equal(t, outboxReference, svc.Clock().Now(),
			"the relay schedules against the engine's clock, not its own")

		first, err := svc.NewEventID()
		require.NoError(t, err)

		second, err := svc.NewEventID()
		require.NoError(t, err)

		assert.NotEqual(t, first, second, "a delivery identifier is distinct per attempt")
		assert.NotEmpty(t, first)
	})
}
