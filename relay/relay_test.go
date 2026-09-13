package relay_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/relay"
)

// reference is the instant these tests measure from.
var reference = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// clock is a hand-driven [hmntsk.Clock], safe for concurrent use because the
// Run cases read it from the relay's goroutine while the test writes it.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// stubSink answers every delivery with one verdict and counts what it was
// offered.
type stubSink struct {
	name    string
	verdict relay.Outcome

	mu          sync.Mutex
	count       int
	lastAttempt relay.Attempt
}

func (s *stubSink) Name() string { return s.name }

func (s *stubSink) Deliver(_ context.Context, attempt relay.Attempt) relay.Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.count++
	s.lastAttempt = attempt

	return s.verdict
}

// attempt returns the attempt this sink was last handed, so that a case can
// assert the relay supplied an identity for it.
func (s *stubSink) attempt() relay.Attempt {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lastAttempt
}

func (s *stubSink) delivered() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.count
}

// brokenStore is a store whose claim or whose settlement fails.
//
// The two paths it covers — the database refusing the claim, and refusing the
// settlement after the delivery has already happened — are the ones the shared
// suite cannot reach, because a store that works never produces them and the
// suite runs only stores that work.
type brokenStore struct {
	hmntsk.Store
	claim  error
	settle error
}

func (s brokenStore) ClaimDueEvents(
	ctx context.Context,
	claim hmntsk.OutboxClaim,
) ([]hmntsk.OutboxEntry, error) {
	if s.claim != nil {
		return nil, s.claim
	}

	return s.Store.ClaimDueEvents(ctx, claim)
}

func (s brokenStore) MarkAccepted(ctx context.Context, acceptance hmntsk.Acceptance) error {
	if s.settle != nil {
		return s.settle
	}

	return s.Store.MarkAccepted(ctx, acceptance)
}

// harness is an engine over the in-memory store whose clock the test moves by
// hand.
type harness struct {
	t      *testing.T
	store  *memstore.Store
	engine *hmntsk.Service
	clock  *clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	return newHarnessOver(t, func(store hmntsk.Store) hmntsk.Store { return store })
}

// newHarnessOver builds a harness whose engine reaches the in-memory store
// through wrap, so that a case can make one storage call fail.
func newHarnessOver(t *testing.T, wrap func(hmntsk.Store) hmntsk.Store) *harness {
	t.Helper()

	store := memstore.New()
	tick := &clock{now: reference}

	engine, err := hmntsk.New(wrap(store), hmntsk.WithClock(tick))
	require.NoError(t, err)

	return &harness{t: t, store: store, engine: engine, clock: tick}
}

// seed records events durably, as the engine does inside the transaction that
// produced them.
func (h *harness) seed(ids ...string) {
	h.t.Helper()

	events := make([]hmntsk.Event, 0, len(ids))
	for i, id := range ids {
		events = append(events, hmntsk.Event{
			ID:         id,
			Type:       hmntsk.EventTypeCreated,
			TaskID:     hmntsk.TaskID("task-" + id),
			TaskType:   "approval",
			Status:     hmntsk.StatusReady,
			OccurredAt: reference.Add(-time.Duration(len(ids)-i) * time.Minute),
		})
	}

	err := h.store.Do(h.t.Context(), func(ctx context.Context) error {
		return h.store.Append(ctx, events)
	})
	require.NoError(h.t, err)
}

func (h *harness) entry(id string) hmntsk.OutboxEntry {
	h.t.Helper()

	entry, err := h.store.OutboxEntry(h.t.Context(), id)
	require.NoError(h.t, err)

	return entry
}

func TestNewRelayRefusesAConfigurationThatCouldOnlyFailLater(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		engine func(t *testing.T) *hmntsk.Service
		opts   []relay.RelayOption
		assert func(t *testing.T, built *relay.Relay, err error)
	}

	engineOver := func(t *testing.T) *hmntsk.Service { return newHarness(t).engine }

	cases := []testCase{
		{
			name:   "no engine",
			engine: func(*testing.T) *hmntsk.Service { return nil },
			opts:   []relay.RelayOption{relay.WithSinks(&stubSink{name: "bus"})},
			assert: func(t *testing.T, _ *relay.Relay, err error) {
				assert.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
		{
			name:   "no sinks",
			engine: engineOver,
			assert: func(t *testing.T, _ *relay.Relay, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.ErrorContains(t, err, "at least one sink",
					"a relay with no sink would mark every event delivered to nobody")
			},
		},
		{
			name:   "an unnamed sink",
			engine: engineOver,
			opts:   []relay.RelayOption{relay.WithSinks(&stubSink{})},
			assert: func(t *testing.T, _ *relay.Relay, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.ErrorContains(t, err, "named")
			},
		},
		{
			name:   "two sinks sharing a name",
			engine: engineOver,
			opts: []relay.RelayOption{
				relay.WithSinks(&stubSink{name: "bus"}, &stubSink{name: "bus"}),
			},
			assert: func(t *testing.T, _ *relay.Relay, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration,
					"a duplicate name must fail at construction, not at the first pass")
				assert.ErrorContains(t, err, "bus")
			},
		},
		{
			name:   "a nil sink among real ones is ignored",
			engine: engineOver,
			opts:   []relay.RelayOption{relay.WithSinks(nil, &stubSink{name: "bus"})},
			assert: func(t *testing.T, built *relay.Relay, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"bus"}, built.Sinks())
			},
		},
		{
			name:   "a named sink is enough",
			engine: engineOver,
			opts: []relay.RelayOption{
				relay.WithSinks(&stubSink{name: "webhook"}, &stubSink{name: "bus"}),
				relay.WithRelayOwner("relay-1"),
			},
			assert: func(t *testing.T, built *relay.Relay, err error) {
				require.NoError(t, err)
				assert.Equal(t, "relay-1", built.Owner())
				assert.Equal(t, []string{"webhook", "bus"}, built.Sinks(),
					"sinks are attempted in the order they were configured")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			built, err := relay.NewRelay(tc.engine(t), tc.opts...)
			tc.assert(t, built, err)
		})
	}
}

func TestAGeneratedOwnerIsUniquePerRelay(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	first, err := relay.NewRelay(h.engine, relay.WithSinks(&stubSink{name: "bus"}))
	require.NoError(t, err)

	second, err := relay.NewRelay(h.engine, relay.WithSinks(&stubSink{name: "bus"}))
	require.NoError(t, err)

	assert.NotEqual(t, first.Owner(), second.Owner(),
		"two relays sharing an owner would each reclaim the other's leases")
	assert.Contains(t, first.Owner(), "relay-", "an abandoned lease must be traceable")
}

// TestNothingStartsOnItsOwn is the assertion behind "the host decides when the
// relay runs".
//
// Constructing an engine and a relay must start no goroutine, no timer and no
// polling. An embedded library does not get to decide that the process it lives
// in now has a background thread — and a relay that polled on its own would
// deliver from a test that never asked it to.
func TestNothingStartsOnItsOwn(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := newHarness(t)
	h.seed("e1")

	bus := &stubSink{name: "bus", verdict: relay.Delivered()}

	built, err := relay.NewRelay(h.engine, relay.WithSinks(bus))
	require.NoError(t, err)
	require.NotEmpty(t, built.Owner())

	h.clock.advance(2 * time.Hour)

	// Time has passed and an event is long due, and still nothing has happened,
	// because nobody asked.
	assert.Zero(t, bus.delivered(), "no sink is offered anything until the host runs a pass")

	entry := h.entry("e1")
	assert.True(t, entry.Pending())
	assert.Zero(t, entry.Attempts)
	assert.Empty(t, entry.LockedBy, "no pass has taken a lease")
}

// TestRunStopsWhenTheHostStopsIt pins the other half: when the host does start
// a relay loop, cancelling its context ends it and leaves nothing running.
func TestRunStopsWhenTheHostStopsIt(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := newHarness(t)
	h.seed("e1")

	bus := &stubSink{name: "bus", verdict: relay.Delivered()}

	built, err := relay.NewRelay(h.engine, relay.WithSinks(bus), relay.WithRelayOwner("relay-1"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() { done <- built.Run(ctx, time.Millisecond) }()

	assert.Eventually(t, func() bool {
		return bus.delivered() > 0
	}, 5*time.Second, 5*time.Millisecond, "a running relay must deliver the due event")

	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the relay loop did not stop when its context was cancelled")
	}
}

func TestRunRefusesANonPositiveInterval(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	built, err := relay.NewRelay(h.engine, relay.WithSinks(&stubSink{name: "bus"}))
	require.NoError(t, err)

	assert.ErrorIs(t, built.Run(t.Context(), 0), hmntsk.ErrConfiguration)
	assert.ErrorIs(t, built.Run(t.Context(), -time.Second), hmntsk.ErrConfiguration)
}

// TestAStoreThatRefusesDoesNotCostThePass covers the two failures the shared
// suite cannot reach, because a working store never produces them: a claim the
// store refuses, and a settlement it refuses after the delivery has already
// happened.
// TestTheRelayGivesEachSinkAnAttemptIdentity covers what the relay adds to the
// event on its way to a sink.
//
// A sink is handed the attempt's identity rather than minting its own, because
// the relay is what counts attempts. Doing it here means a new sink inherits
// the guarantee instead of re-implementing it.
func TestTheRelayGivesEachSinkAnAttemptIdentity(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.seed("e1")

	bus := &stubSink{name: "bus", verdict: relay.Delivered()}

	built, err := relay.NewRelay(h.engine, relay.WithSinks(bus))
	require.NoError(t, err)

	_, err = built.Relay(t.Context())
	require.NoError(t, err)

	attempt := bus.attempt()

	assert.Equal(t, "e1", attempt.Event.ID, "the sink is handed the event it is delivering")
	assert.NotEmpty(t, attempt.DeliveryID,
		"every attempt carries an identifier, so a receiver can tell a redelivery from a repeat")
	assert.Equal(t, 1, attempt.Number, "attempts are counted from one")
}

func TestAStoreThatRefusesDoesNotCostThePass(t *testing.T) {
	t.Parallel()

	errStore := errors.New("the database is unreachable")

	t.Run("a refused claim fails the pass", func(t *testing.T) {
		t.Parallel()

		h := newHarnessOver(t, func(s hmntsk.Store) hmntsk.Store {
			return brokenStore{Store: s, claim: errStore}
		})
		h.seed("e1")

		bus := &stubSink{name: "bus", verdict: relay.Delivered()}

		built, err := relay.NewRelay(h.engine, relay.WithSinks(bus))
		require.NoError(t, err)

		_, err = built.Relay(t.Context())
		assert.ErrorIs(t, err, errStore,
			"a pass that could not claim did nothing, and says so rather than reporting a "+
				"successful pass over no events")
		assert.Zero(t, bus.delivered())
	})

	t.Run("a refused settlement is reported and the pass continues", func(t *testing.T) {
		t.Parallel()

		h := newHarnessOver(t, func(s hmntsk.Store) hmntsk.Store {
			return brokenStore{Store: s, settle: errStore}
		})
		h.seed("e1", "e2", "e3")

		bus := &stubSink{name: "bus", verdict: relay.Delivered()}

		var reported []error

		built, err := relay.NewRelay(
			h.engine,
			relay.WithSinks(bus),
			relay.WithRelayErrorHandler(func(_ context.Context, err error) {
				reported = append(reported, err)
			}),
		)
		require.NoError(t, err)

		result, err := built.Relay(t.Context())
		require.NoError(t, err)

		assert.Equal(t, 3, result.Claimed)
		assert.Equal(t, 3, bus.delivered(),
			"an event whose settlement failed must not abandon the events behind it")

		assert.Equal(t, 3, result.Unsettled,
			"the pass reports its at-least-once exposure: three deliveries nothing durable "+
				"records, every one of which will happen again")
		assert.Zero(t, result.Delivered,
			"a result must count what the pass made durable, not what it decided; these rows "+
				"still say pending")

		require.Len(t, reported, 3, "every failed settlement reaches the host")
		assert.ErrorIs(t, reported[0], errStore)
		assert.ErrorContains(t, reported[0], "e1", "the host is told which event went unrecorded")

		// The delivery happened and the record of it did not, so the event is
		// still pending and will be delivered again. That is at-least-once
		// behaving exactly as documented.
		assert.True(t, h.entry("e1").Pending())
	})
}
