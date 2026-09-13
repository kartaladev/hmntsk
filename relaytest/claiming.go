package relaytest

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// runClaimingCases covers the lease that makes delivery exclusive.
//
// The cases assert the outcome — each event attempted exactly once — and never
// the mechanism, because there is no mechanism they could assert: one supported
// dialect has no row-level locking at all, so exclusivity has to come from a
// conditional update on the lease columns. A store that reached for a lock here
// would pass nothing extra and would stop working on SQLite.
//
// These flows diverge structurally — one runs two relays at once, one plants an
// abandoned lease by hand, one counts offers within a single pass — so they are
// separate cases rather than rows of one table.
func runClaimingCases(t *testing.T, factory Factory) {
	t.Helper()

	// The assertion is on the outcome, not on the interleaving, and that is
	// deliberate: a store entitled to serialise its transactions — the
	// in-memory one does, behind a single mutex — turns this into two
	// sequential passes and still has to pass it, because exclusive claiming
	// is what is being asserted and serialising is one lawful way to achieve
	// it. On a store that really does run them at once, the same assertion is
	// the race it looks like.
	t.Run("two relays running at once attempt each event exactly once", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(
			event("e1", -4*time.Minute),
			event("e2", -3*time.Minute),
			event("e3", -2*time.Minute),
			event("e4", -time.Minute),
		)

		first, second := delivers("bus"), delivers("bus")

		relays := []*relay.Relay{
			f.relay(relay.WithSinks(first), relay.WithRelayOwner("relay-1"), relay.WithRelayBatch(2)),
			f.relay(relay.WithSinks(second), relay.WithRelayOwner("relay-2"), relay.WithRelayBatch(2)),
		}

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			claimed int
		)

		for _, r := range relays {
			wg.Add(1)

			go func() {
				defer wg.Done()

				result, err := r.Relay(t.Context())

				mu.Lock()
				defer mu.Unlock()

				assert.NoError(t, err)

				claimed += result.Claimed
			}()
		}

		wg.Wait()

		attempted := append(first.seen(), second.seen()...)
		slices.Sort(attempted)

		assert.Equal(t, []string{"e1", "e2", "e3", "e4"}, attempted,
			"every event is attempted, and none of them twice")
		assert.Equal(t, 4, claimed, "the two relays between them claim each event once")

		for _, id := range []string{"e1", "e2", "e3", "e4"} {
			assert.Truef(t, f.entry(id).Delivered(), "event %s must be marked delivered", id)
		}
	})

	t.Run("a crashed relay does not strand its event", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", -time.Minute))

		// A relay that claims and then dies leaves exactly this behind: a live
		// lease and no settlement.
		abandoned, err := f.engine.ClaimDueEvents(t.Context(), hmntsk.OutboxClaim{
			Now:      f.clock.Now(),
			Owner:    "relay-crashed",
			Duration: time.Minute,
			Limit:    10,
		})
		require.NoError(t, err)
		require.Len(t, abandoned, 1, "the due event is claimable before anything holds it")

		bus := delivers("bus")
		survivor := f.relay(relay.WithSinks(bus), relay.WithRelayOwner("relay-2"))

		result := f.pass(survivor)
		assert.Zero(t, result.Claimed, "a live lease is another relay's to settle")
		assert.Empty(t, bus.seen(), "and nothing is delivered behind its back")

		f.clock.advance(time.Minute + time.Second)

		result = f.pass(survivor)
		assert.Equal(t, 1, result.Claimed, "an expired lease makes the event claimable again")
		assert.Equal(t, []string{"e1"}, bus.seen())
		assert.True(t, f.entry("e1").Delivered())
	})

	t.Run("each event is attempted once per pass", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(
			event("e1", -3*time.Minute),
			event("e2", -2*time.Minute),
			event("e3", -time.Minute),
		)

		bus := failsRetryably("bus")
		r := f.relay(relay.WithSinks(bus))

		result := f.pass(r)
		assert.Equal(t, 3, result.Claimed)
		assert.Equal(t, []string{"e1", "e2", "e3"}, bus.seen(),
			"one pass offers each claimed event once, not once per sink call it retries internally")

		// The pass settled every event it claimed, so a second pass at the same
		// instant finds the lease released and the schedule in the future.
		assert.Zero(t, f.pass(r).Claimed, "a settled event is not claimed again until it is due")
		assert.Len(t, bus.seen(), 3)
	})
}
