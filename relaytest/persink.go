package relaytest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// runPerSinkCases covers the accounting that makes two destinations possible.
//
// One published-at timestamp is only ever correct for one sink. With two, a
// webhook success followed by a broker outage has to be recorded as exactly
// that, or the retry either delivers to the webhook a second time or marks the
// event delivered with the broker never having seen it. Both are wrong, and
// they are wrong in opposite directions, which is why the acceptance is
// recorded per sink rather than as a flag.
//
// The two cases are one flow told in two halves — a partial acceptance, then
// the acceptance that completes it — so they share a fixture-building helper
// rather than a table.
func runPerSinkCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a retry targets only the sink that failed", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", 0))

		webhook, bus := delivers("webhook"), failsRetryably("bus")
		r := f.relay(relay.WithSinks(webhook, bus), relay.WithMaxAttempts(10))

		require.Equal(t, 1, f.pass(r).Claimed)

		entry := f.entry("e1")
		assert.Equal(t, []string{"webhook"}, entry.Accepted, "the sink that took it is recorded")
		assert.True(t, entry.HasAccepted("webhook"))
		assert.False(t, entry.HasAccepted("bus"))
		assert.False(t, entry.Delivered(), "one sink of two is not delivery")
		assert.True(t, entry.Pending(), "the event stays claimable for the sink that failed")

		f.waitUntilDue("e1")

		result := f.pass(r)
		require.Equal(t, 1, result.Claimed)

		assert.Equal(t, []string{"e1"}, webhook.seen(),
			"the sink that already accepted must not be offered the event again")
		assert.Equal(t, []string{"e1", "e1"}, bus.seen(), "only the sink that failed retries")
		assert.Equal(t, 1, result.Sinks["webhook"].Skipped,
			"the pass reports that the webhook was passed over rather than delivered to")
	})

	t.Run("an event is delivered only when every sink has accepted", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", 0))

		webhook, bus := delivers("webhook"), failsRetryably("bus")
		r := f.relay(relay.WithSinks(webhook, bus), relay.WithMaxAttempts(10))

		require.Equal(t, 1, f.pass(r).Claimed)
		require.False(t, f.entry("e1").Delivered())

		// The broker comes back.
		bus.answer(func(hmntsk.Event) relay.Outcome { return relay.Delivered() })

		f.waitUntilDue("e1")

		result := f.pass(r)
		assert.Equal(t, 1, result.Delivered)

		entry := f.entry("e1")
		assert.True(t, entry.Delivered(), "every configured sink has now taken it")
		assert.False(t, entry.Pending())
		assert.False(t, entry.DeadLettered())
		assert.ElementsMatch(t, []string{"webhook", "bus"}, entry.Accepted)
		assert.Empty(t, entry.LastError, "a delivered event carries no outstanding failure")

		if assert.NotNil(t, entry.PublishedAt) {
			assert.Equal(t, hmntsk.NormalizeTime(f.clock.Now()), entry.PublishedAt.UTC(),
				"delivery is stamped at the moment the last sink accepted")
		}

		f.clock.advance(24 * time.Hour)
		assert.Zero(t, f.pass(r).Claimed, "a delivered event is never claimed again")
		assert.Equal(t, []string{"e1"}, webhook.seen(), "and is delivered to each sink once")
		assert.Equal(t, []string{"e1", "e1"}, bus.seen())
	})
}
