package relaytest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// runDeadLetterCases covers the end of the line: an event the relay stops
// attempting, and what is left behind when it does.
//
// A dead letter is a state on the row rather than a separate table, and it is
// the absence of a next attempt rather than a marker column of its own: the
// entry that will never be attempted again and never was published. These cases
// assert that shape from the outside, through [hmntsk.OutboxEntry.Pending],
// [hmntsk.OutboxEntry.Delivered] and [hmntsk.OutboxEntry.DeadLettered], so that
// a store is free to record it however its dialect prefers.
func runDeadLetterCases(t *testing.T, factory Factory) {
	t.Helper()

	// outcome is everything a case looks at once the passes are done: the row
	// the relay left behind, the last pass's report, what the sink was offered,
	// and what reached the host.
	type outcome struct {
		entry    hmntsk.OutboxEntry
		result   relay.Result
		offered  []string
		reported []error
	}

	type testCase struct {
		name string
		// verdict is what the sink answers every delivery with.
		verdict relay.Outcome
		// maxAttempts is the relay's attempt budget for the case.
		maxAttempts int
		// passes is how many due passes the case runs. The clock is advanced to
		// each next-attempt time between them, so the count is attempts rather
		// than an arbitrary number of ticks.
		passes int
		assert func(t *testing.T, got outcome)
	}

	cases := []testCase{
		{
			name:        "attempts are exhausted",
			verdict:     relay.Retryable(errUnreachable),
			maxAttempts: 3,
			passes:      3,
			assert: func(t *testing.T, got outcome) {
				assert.True(t, got.entry.DeadLettered(), "the budget ran out")
				assert.False(t, got.entry.Pending())
				assert.False(t, got.entry.Delivered())
				assert.Equal(t, 3, got.entry.Attempts, "every attempt in the budget was spent")
				assert.Contains(t, got.entry.LastError, "bus",
					"the last error names what refused it")
				assert.Equal(t, 1, got.result.DeadLettered)
				assert.Equal(t, []string{"e1", "e1", "e1"}, got.offered,
					"the sink was offered the event once per attempt and no more")
				assert.NotEmpty(t, got.reported, "the host is told why an event was abandoned")
			},
		},
		{
			name:        "a permanent failure skips the remaining attempts",
			verdict:     relay.Permanent(errRefused),
			maxAttempts: 5,
			passes:      1,
			assert: func(t *testing.T, got outcome) {
				assert.True(t, got.entry.DeadLettered(),
					"a verdict no attempt can change is not worth four more attempts")
				assert.Equal(t, 1, got.entry.Attempts, "the remaining budget is not spent")
				assert.Contains(t, got.entry.LastError, errRefused.Error())
				assert.Equal(t, 1, got.result.DeadLettered)
				assert.Equal(t, []string{"e1"}, got.offered)
			},
		},
		{
			// The zero Outcome is not a verdict: a sink returning it has
			// forgotten to classify its result.
			name:        "a sink that will not classify its result is a sink bug",
			verdict:     relay.Outcome{},
			maxAttempts: 5,
			passes:      1,
			assert: func(t *testing.T, got outcome) {
				assert.True(t, got.entry.DeadLettered(),
					"an outcome that says nothing cannot be retried forever on the hope it "+
						"starts saying something")
				assert.Equal(t, 1, got.result.DeadLettered)
				require.NotEmpty(t, got.reported, "the host is told its sink is broken")
				assert.ErrorContains(t, got.reported[0], "unclassified")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, factory)
			f.seed(event("e1", 0))

			bus := newSink("bus", func(hmntsk.Event) relay.Outcome { return tc.verdict })
			r := f.relay(relay.WithSinks(bus), relay.WithMaxAttempts(tc.maxAttempts))

			var result relay.Result

			for pass := range tc.passes {
				result = f.pass(r)
				require.Equalf(t, 1, result.Claimed, "pass %d must claim the event", pass+1)

				if f.entry("e1").Pending() {
					f.waitUntilDue("e1")
				}
			}

			tc.assert(t, outcome{
				entry:    f.entry("e1"),
				result:   result,
				offered:  bus.seen(),
				reported: f.errors.all(),
			})
		})
	}

	t.Run("a dead letter remains inspectable and is never attempted again", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", -time.Minute), event("e2", -time.Second))

		bus := newSink("bus", func(e hmntsk.Event) relay.Outcome {
			if e.ID == "e1" {
				return relay.Permanent(errRefused)
			}

			return relay.Delivered()
		})

		r := f.relay(relay.WithSinks(bus), relay.WithMaxAttempts(5))
		require.Equal(t, 2, f.pass(r).Claimed)

		f.clock.advance(24 * time.Hour)
		assert.Zero(t, f.pass(r).Claimed, "neither a dead letter nor a delivered event is due")
		assert.Equal(t, []string{"e1", "e2"}, bus.seen(), "and neither is offered again")

		dead := f.entry("e1")
		assert.True(t, dead.DeadLettered(), "the row is retained, not deleted")
		assert.Equal(t, "e1", dead.Event.ID, "the event itself is still readable")
		assert.Equal(t, 1, dead.Attempts)
		assert.Contains(t, dead.LastError, errRefused.Error())

		delivered := f.entry("e2")
		assert.True(t, delivered.Delivered())
		assert.False(t, delivered.DeadLettered(),
			"a delivered event and a dead letter must not look alike")
	})

	t.Run("a dead letter records the sinks that did take it", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", 0))

		// The webhook takes it on the very attempt the bus gives up on. That
		// acceptance is the one at risk: the acceptances from earlier attempts
		// are already on the row, and this one has to be written by the same
		// settlement that marks the event dead.
		hook := delivers("webhook")
		bus := failsPermanently("bus")

		r := f.relay(relay.WithSinks(hook, bus), relay.WithMaxAttempts(5))
		require.Equal(t, 1, f.pass(r).Claimed)

		dead := f.entry("e1")
		require.True(t, dead.DeadLettered())

		assert.True(t, dead.HasAccepted("webhook"),
			"a destination that demonstrably received the event must say so on the row: "+
				"inspecting a dead letter is the only thing the engine promises about one, "+
				"and a replay driven from an empty set would deliver to the webhook twice")
		assert.False(t, dead.HasAccepted("bus"),
			"and the destination that refused it must not")
	})
}
