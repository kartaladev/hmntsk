package relaytest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/relay"
)

// runSchedulingCases covers the order a backlog drains in and the delay a
// failure earns.
//
// Every case here switches jitter off or supplies the randomness itself, so
// that the assertions are about the schedule rather than about a range it
// happens to fall in. The one case that is about jitter supplies two known
// draws and asserts the two events end up apart.
//
// The flows diverge — one counts offers, one walks a sequence of passes, one
// compares two events against each other — so they are separate cases rather
// than rows of one table.
func runSchedulingCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a backlog drains oldest first", func(t *testing.T) {
		f := newFixture(t, factory)

		// Seeded out of order, so that passing this means sorting rather than
		// echoing the insertion order back.
		f.seed(
			event("e2", -2*time.Minute),
			event("e3", -time.Minute),
			event("e1", -3*time.Minute),
		)

		bus := delivers("bus")
		assert.Equal(t, 3, f.pass(f.relay(relay.WithSinks(bus))).Claimed)
		assert.Equal(t, []string{"e1", "e2", "e3"}, bus.seen(),
			"events are attempted in the order they occurred")
	})

	t.Run("the delay grows between attempts up to the ceiling", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", 0))

		const ceiling = 90 * time.Second

		r := f.relay(
			relay.WithSinks(failsRetryably("bus")),
			relay.WithBackoff(backoff, ceiling),
			relay.WithMaxAttempts(10),
		)

		waits := make([]time.Duration, 0, 4)

		for range 4 {
			at := f.clock.Now()

			require.Equal(t, 1, f.pass(r).Claimed, "the event is due at %s", at)

			wait := f.due("e1").Sub(at)
			waits = append(waits, wait)

			// Move to the moment the relay itself said the event is due, so the
			// next pass measures the schedule the relay wrote rather than one
			// the case guessed.
			f.clock.advance(wait)
		}

		assert.Equal(t, []time.Duration{backoff, 2 * backoff, ceiling, ceiling}, waits,
			"each wait doubles until the ceiling caps it")
	})

	t.Run("an event is not claimed before it is due", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", 0))

		bus := failsRetryably("bus")
		r := f.relay(relay.WithSinks(bus), relay.WithMaxAttempts(10))

		require.Equal(t, 1, f.pass(r).Claimed)

		// One second short of the schedule the relay wrote.
		f.clock.advance(backoff - time.Second)

		assert.Zero(t, f.pass(r).Claimed, "a pass before the next-attempt time claims nothing")
		assert.Len(t, bus.seen(), 1, "and offers the sink nothing")

		f.clock.advance(time.Second)

		assert.Equal(t, 1, f.pass(r).Claimed, "the event is claimable once it is due")
		assert.Len(t, bus.seen(), 2)
	})

	t.Run("jitter keeps two events failing together out of lockstep", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", -2*time.Minute), event("e2", -time.Minute))

		lockstep := f.relay(relay.WithSinks(failsRetryably("bus")), relay.WithJitter(0))
		require.Equal(t, 2, f.pass(lockstep).Claimed)

		assert.Equal(t, f.due("e1"), f.due("e2"),
			"without jitter two events that failed together retry together, "+
				"which is what knocks a recovering receiver over a second time")

		f.clock.advance(time.Hour)

		draws := []float64{0.1, 0.9}
		jittered := f.relay(
			relay.WithSinks(failsRetryably("bus")),
			relay.WithMaxAttempts(10),
			relay.WithJitter(0.5),
			relay.WithJitterSource(func() float64 {
				draw := draws[0]
				draws = draws[1:]

				return draw
			}),
		)
		require.Equal(t, 2, f.pass(jittered).Claimed)

		first, second := f.due("e1"), f.due("e2")
		assert.NotEqual(t, first, second, "jitter spreads the retries")
		assert.Empty(t, draws, "each event's delay draws its own random component")

		// The draws bracket the unjittered delay rather than replacing it: a
		// low draw retries sooner, a high one later, and the expectation is
		// unchanged.
		unjittered := f.clock.Now().Add(2 * backoff)
		assert.True(t, first.Before(unjittered), "a low draw retries sooner, not never")
		assert.True(t, second.After(unjittered), "a high draw retries later, not much later")
	})
}
