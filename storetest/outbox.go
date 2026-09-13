package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// runOutboxCases covers the relay's view of the durable event record: which
// events a pass may take, what each settlement does to a row, and what a row
// says about itself afterwards.
//
// The cases are the storage half of the relay's guarantees. Exclusive claiming
// without a row lock, a next-attempt time that is honoured, a lease that heals
// itself and a dead letter that stays readable are all behaviour an adapter can
// get wrong on its own, and all of them are invisible until events start going
// missing in production.
func runOutboxCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("claiming", func(t *testing.T) { outboxClaiming(t, factory) })
	t.Run("scheduling", func(t *testing.T) { outboxScheduling(t, factory) })
	t.Run("settlement", func(t *testing.T) { outboxSettlement(t, factory) })
	t.Run("missing events", func(t *testing.T) { outboxMissingEvents(t, factory) })
}

// recordEvents stores events the way the engine does: inside the transaction
// that produced them.
func recordEvents(t *testing.T, h Harness, events ...hmntsk.Event) {
	t.Helper()

	require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
		return h.Store.Append(ctx, events)
	}))
}

// claimDue runs one relay pass's worth of claiming.
func claimDue(t *testing.T, h Harness, now time.Time, owner string, limit int) []hmntsk.OutboxEntry {
	t.Helper()

	var claimed []hmntsk.OutboxEntry

	require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
		var err error

		claimed, err = h.Store.ClaimDueEvents(ctx, hmntsk.OutboxClaim{
			Now: now, Owner: owner, Duration: time.Minute, Limit: limit,
		})

		return err
	}))

	return claimed
}

// settle runs one settlement write in its own transaction, as a relay does so
// that one event's failure cannot roll back another's success.
func settle(t *testing.T, h Harness, write func(ctx context.Context) error) {
	t.Helper()

	require.NoError(t, h.Store.Do(t.Context(), write))
}

// entryOf reads one outbox entry back.
func entryOf(t *testing.T, h Harness, eventID string) hmntsk.OutboxEntry {
	t.Helper()

	entry, err := h.Store.OutboxEntry(t.Context(), eventID)
	require.NoError(t, err)

	return entry
}

// eventAt builds an event for a task, recorded at a given instant.
func eventAt(task hmntsk.Task, id string, at time.Time) hmntsk.Event {
	event := NewEvent(task, hmntsk.EventTypeCompleted, id)
	event.OccurredAt = at

	return event
}

// outboxClaiming covers exclusivity, which has to hold without any row-level
// locking because one supported dialect has none.
func outboxClaiming(t *testing.T, factory Factory) {
	t.Helper()

	h := factory(t)

	var seq ids

	task := Seed(t, h, NewTask(seq.next()))
	recordEvents(t, h, eventAt(task, "event-1", Reference))

	first := claimDue(t, h, Reference, "relay-1", 10)
	require.Len(t, first, 1, "a recorded event is due as soon as it is recorded")
	assert.Equal(t, "event-1", first[0].Event.ID)
	assert.Equal(t, "relay-1", first[0].LockedBy)
	assert.True(t, first[0].Pending())
	assert.Equal(t, 0, first[0].Attempts)
	require.NotNil(t, first[0].NextAttemptAt)
	assert.True(t, Reference.Equal(*first[0].NextAttemptAt))

	assert.Empty(t, claimDue(t, h, Reference, "relay-2", 10),
		"two relays running at once must attempt an event exactly once between them")

	reclaimed := claimDue(t, h, Reference.Add(2*time.Minute), "relay-3", 10)
	require.Len(t, reclaimed, 1,
		"a relay that crashed mid-pass must not strand the event past its lease")
	assert.Equal(t, "relay-3", reclaimed[0].LockedBy)
}

// outboxScheduling covers which events a pass takes and in what order.
func outboxScheduling(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a backlog drains oldest first", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := Seed(t, h, NewTask(seq.next()))

		recordEvents(t, h,
			eventAt(task, "event-newest", Reference),
			eventAt(task, "event-oldest", Reference.Add(-2*time.Hour)),
			eventAt(task, "event-middle", Reference.Add(-time.Hour)),
		)

		claimed := claimDue(t, h, Reference, "relay-1", 2)
		require.Len(t, claimed, 2, "a limit caps what one pass takes")
		assert.Equal(t, "event-oldest", claimed[0].Event.ID)
		assert.Equal(t, "event-middle", claimed[1].Event.ID,
			"a backlog drains in the order it accumulated")
	})

	t.Run("an event is not claimed before it is due", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := Seed(t, h, NewTask(seq.next()))
		recordEvents(t, h, eventAt(task, "event-1", Reference))

		require.Len(t, claimDue(t, h, Reference, "relay-1", 10), 1)

		settle(t, h, func(ctx context.Context) error {
			return h.Store.RecordAttempt(ctx, hmntsk.AttemptRecord{
				EventID:       "event-1",
				Attempts:      1,
				NextAttemptAt: Reference.Add(5 * time.Minute),
				LastError:     "503 from receiver",
			})
		})

		assert.Empty(t, claimDue(t, h, Reference.Add(time.Minute), "relay-1", 10),
			"a retry that has not come due yet must not be attempted")

		due := claimDue(t, h, Reference.Add(6*time.Minute), "relay-1", 10)
		require.Len(t, due, 1, "once the next-attempt time passes the event is claimable again")
		assert.Equal(t, 1, due[0].Attempts)
		assert.Equal(t, "503 from receiver", due[0].LastError)
	})
}

// outboxSettlement covers what each of the three settlements leaves behind.
func outboxSettlement(t *testing.T, factory Factory) {
	t.Helper()

	type testCase struct {
		name   string
		write  func(ctx context.Context, store hmntsk.Store) error
		assert func(t *testing.T, entry hmntsk.OutboxEntry, claimable []hmntsk.OutboxEntry)
	}

	published := Reference.Add(time.Minute)

	cases := []testCase{
		{
			name: "an event every sink took is delivered and never claimed again",
			write: func(ctx context.Context, store hmntsk.Store) error {
				return store.MarkAccepted(ctx, hmntsk.Acceptance{
					EventID: "event-1", Accepted: []string{"webhook", "bus"},
					PublishedAt: &published, Attempts: 1,
				})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry, claimable []hmntsk.OutboxEntry) {
				assert.True(t, entry.Delivered())
				assert.False(t, entry.DeadLettered())
				assert.Equal(t, []string{"webhook", "bus"}, entry.Accepted)
				assert.Empty(t, claimable, "a delivered event is nobody's work any more")
			},
		},
		{
			name: "an event one sink took stays claimable for the one that refused",
			write: func(ctx context.Context, store hmntsk.Store) error {
				next := Reference.Add(5 * time.Minute)

				return store.MarkAccepted(ctx, hmntsk.Acceptance{
					EventID: "event-1", Accepted: []string{"webhook"},
					NextAttemptAt: &next, Attempts: 1, LastError: "bus unavailable",
				})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry, claimable []hmntsk.OutboxEntry) {
				assert.False(t, entry.Delivered(),
					"an event is complete only when every sink has taken it")
				assert.True(t, entry.HasAccepted("webhook"))
				assert.False(t, entry.HasAccepted("bus"))
				assert.Equal(t, "bus unavailable", entry.LastError)
				require.Len(t, claimable, 1, "the retry targets the sink that failed")
				assert.Equal(t, []string{"webhook"}, claimable[0].Accepted,
					"a retry must not redeliver to the sink that already succeeded")
			},
		},
		{
			name: "a dead letter is retained, readable and never attempted again",
			write: func(ctx context.Context, store hmntsk.Store) error {
				return store.MarkDeadLettered(ctx, hmntsk.DeadLetter{
					EventID: "event-1", Attempts: 5, LastError: "400 from receiver",
				})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry, claimable []hmntsk.OutboxEntry) {
				assert.True(t, entry.DeadLettered())
				assert.False(t, entry.Pending())
				assert.False(t, entry.Delivered())
				assert.Equal(t, 5, entry.Attempts)
				assert.Equal(t, "400 from receiver", entry.LastError,
					"what failed and why has to be a query, not a log search")
				assert.Equal(t, "event-1", entry.Event.ID)
				assert.Empty(t, claimable)
			},
		},
		{
			name: "a recorded attempt releases the lease along with everything else",
			write: func(ctx context.Context, store hmntsk.Store) error {
				return store.RecordAttempt(ctx, hmntsk.AttemptRecord{
					EventID: "event-1", Attempts: 1,
					NextAttemptAt: Reference, LastError: "503 from receiver",
				})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry, claimable []hmntsk.OutboxEntry) {
				assert.Empty(t, entry.LockedBy, "a settled event is not this relay's any more")
				assert.Nil(t, entry.LockedUntil)
				assert.False(t, entry.IsLeased(Reference))
				require.Len(t, claimable, 1, "an event whose lease was released is claimable at once")
				assert.Equal(t, "relay-2", claimable[0].LockedBy)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := factory(t)

			var seq ids

			task := Seed(t, h, NewTask(seq.next()))
			recordEvents(t, h, eventAt(task, "event-1", Reference))

			require.Len(t, claimDue(t, h, Reference, "relay-1", 10), 1)

			settle(t, h, func(ctx context.Context) error { return tc.write(ctx, h.Store) })

			// A pass run after the next-attempt time of every case, so that
			// "still claimable" and "not claimable any more" are the answer to
			// the same question.
			tc.assert(t,
				entryOf(t, h, "event-1"),
				claimDue(t, h, Reference.Add(10*time.Minute), "relay-2", 10),
			)
		})
	}
}

// outboxMissingEvents covers the answer every outbox operation owes for an
// identifier that is not there.
func outboxMissingEvents(t *testing.T, factory Factory) {
	t.Helper()

	type testCase struct {
		name string
		call func(ctx context.Context, store hmntsk.Store) error
	}

	published := Reference

	cases := []testCase{
		{
			name: "reading",
			call: func(ctx context.Context, store hmntsk.Store) error {
				_, err := store.OutboxEntry(ctx, "no-such-event")

				return err
			},
		},
		{
			name: "recording an attempt",
			call: func(ctx context.Context, store hmntsk.Store) error {
				return store.RecordAttempt(ctx, hmntsk.AttemptRecord{
					EventID: "no-such-event", Attempts: 1, NextAttemptAt: Reference,
				})
			},
		},
		{
			name: "marking accepted",
			call: func(ctx context.Context, store hmntsk.Store) error {
				return store.MarkAccepted(ctx, hmntsk.Acceptance{
					EventID: "no-such-event", Accepted: []string{"webhook"},
					PublishedAt: &published, Attempts: 1,
				})
			},
		},
		{
			name: "dead-lettering",
			call: func(ctx context.Context, store hmntsk.Store) error {
				return store.MarkDeadLettered(ctx, hmntsk.DeadLetter{
					EventID: "no-such-event", Attempts: 5, LastError: "gone",
				})
			},
		},
	}

	h := factory(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error

			require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
				err = tc.call(ctx, h.Store)

				return nil
			}))

			require.Error(t, err)
			assert.ErrorIs(t, err, hmntsk.ErrNotFound)

			var notFound *hmntsk.OutboxNotFoundError

			require.ErrorAs(t, err, &notFound)
			assert.Equal(t, "no-such-event", notFound.EventID)
		})
	}
}
