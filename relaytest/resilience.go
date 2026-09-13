package relaytest

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// Sentinel failures the cases can look for in a recorded last error, so that an
// assertion names the failure it expects rather than matching a substring that
// happens to be there.
var (
	errUnreachable = errors.New("the receiver is unreachable")
	errRefused     = errors.New("the receiver refused the delivery")
)

// runResilienceCases covers what a pass does when part of it goes wrong.
//
// A relay pass is a loop over events that call out to the network, so partial
// failure is the normal case rather than the exceptional one. What must not
// happen is that one bad event costs the batch: an unroutable webhook on the
// oldest event would then block every event behind it, and a backlog would
// never drain past its first problem.
//
// The failure also has to reach the host. A relay swallowing delivery failures
// is a relay whose backlog grows silently, and the row's last error is only
// discovered by whoever thinks to query for it.
func runResilienceCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("one event failing does not abandon the rest of the pass", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(
			event("e1", -3*time.Minute),
			event("e2", -2*time.Minute),
			event("e3", -time.Minute),
		)

		// The failure is on the second event of three, so that passing means
		// the pass continued rather than that it never had to.
		bus := newSink("bus", func(e hmntsk.Event) relay.Outcome {
			if e.ID == "e2" {
				return relay.Retryable(errUnreachable)
			}

			return relay.Delivered()
		})

		result := f.pass(f.relay(relay.WithSinks(bus), relay.WithMaxAttempts(10)))

		assert.Equal(t, 3, result.Claimed)
		assert.Equal(t, []string{"e1", "e2", "e3"}, bus.seen(),
			"the events after the failing one are still attempted")
		assert.Equal(t, 2, result.Delivered)
		assert.Equal(t, 1, result.Retried)
		assert.Zero(t, result.DeadLettered)

		assert.True(t, f.entry("e1").Delivered())
		assert.True(t, f.entry("e3").Delivered())

		failed := f.entry("e2")
		assert.True(t, failed.Pending(), "the failure is rescheduled, not dropped")
		assert.Equal(t, 1, failed.Attempts)
		assert.Contains(t, failed.LastError, errUnreachable.Error())

		reported := f.errors.all()
		require.NotEmpty(t, reported, "a delivery failure must not be swallowed")
		assert.Contains(t, errors.Join(reported...).Error(), "e2",
			"the host is told which event failed")
	})

	t.Run("one sink failing does not stop the others", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", 0))

		// The failing sink is first, so that passing means the fan-out
		// continued past it.
		webhook, bus := failsRetryably("webhook"), delivers("bus")

		result := f.pass(f.relay(relay.WithSinks(webhook, bus), relay.WithMaxAttempts(10)))

		assert.Equal(t, []string{"e1"}, bus.seen(), "the second sink is still offered the event")
		assert.Equal(t, 1, result.Sinks["bus"].Delivered)
		assert.Equal(t, 1, result.Sinks["webhook"].Retryable)
		assert.Equal(t, []string{"bus"}, f.entry("e1").Accepted)
	})

	t.Run("a redelivered event carries the same identifier", func(t *testing.T) {
		f := newFixture(t, factory)
		f.seed(event("e1", 0))

		bus := failsRetryably("bus")
		r := f.relay(relay.WithSinks(bus), relay.WithMaxAttempts(10))

		require.Equal(t, 1, f.pass(r).Claimed)

		bus.answer(func(hmntsk.Event) relay.Outcome { return relay.Delivered() })
		f.waitUntilDue("e1")

		require.Equal(t, 1, f.pass(r).Claimed)

		assert.Equal(t, []string{"e1", "e1"}, bus.seen(),
			"delivery is at-least-once, and a consumer de-duplicates on the event identifier, "+
				"so a retry must not mint a new one")

		// The other half of the same guarantee: the event identifier is what a
		// consumer de-duplicates on, and the delivery identifier is what tells
		// it the two arrivals were two attempts rather than one seen twice.
		deliveries := bus.delivered()
		require.Len(t, deliveries, 2)
		assert.NotEmpty(t, deliveries[0], "every delivery carries an attempt identifier")
		assert.NotEqual(t, deliveries[0], deliveries[1],
			"a redelivery is a distinct attempt and must say so")

		assert.Equal(t, []int{1, 2}, bus.attemptNumbers(),
			"the relay counts the attempts, so a sink does not have to")
	})
}
