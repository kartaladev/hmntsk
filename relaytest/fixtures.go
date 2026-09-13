package relaytest

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// reference is the instant every case measures from.
//
// It is fixed rather than [time.Now] so that a failure reports a schedule a
// reader can check by hand: a next-attempt time is the reference plus a round
// number of seconds, not an opaque timestamp that differed by the duration of
// the test run.
var reference = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// backoff is the retry base every case configures, chosen so that the doubling
// is legible: 30s, 60s, 120s, 240s.
const backoff = 30 * time.Second

// clock is a hand-driven [hmntsk.Clock]. It is safe for concurrent use, because
// the claiming cases run two relays at once over one of them.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

// newClock returns a clock reading the reference instant.
func newClock() *clock { return &clock{now: reference} }

// Now implements [hmntsk.Clock].
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// advance moves the clock forward.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// sink is a [relay.Sink] that records what it was offered and answers with
// whatever the case tells it to.
//
// Its verdict is a function of the event rather than a fixed value, because the
// interesting cases are the mixed ones: one event of three failing, one sink of
// two accepting, a destination that recovers between passes.
type sink struct {
	name string

	mu         sync.Mutex
	offered    []string
	deliveries []string
	attempts   []int
	verdict    func(event hmntsk.Event) relay.Outcome
}

// newSink returns a sink answering with verdict.
func newSink(name string, verdict func(event hmntsk.Event) relay.Outcome) *sink {
	return &sink{name: name, verdict: verdict}
}

// delivers returns a sink that takes everything.
func delivers(name string) *sink {
	return newSink(name, func(hmntsk.Event) relay.Outcome { return relay.Delivered() })
}

// failsPermanently returns a sink that refuses everything in a way no further
// attempt could survive.
func failsPermanently(name string) *sink {
	return newSink(name, func(hmntsk.Event) relay.Outcome { return relay.Permanent(errRefused) })
}

// failsRetryably returns a sink that refuses everything in a way another
// attempt might survive.
func failsRetryably(name string) *sink {
	return newSink(name, func(hmntsk.Event) relay.Outcome { return relay.Retryable(errUnreachable) })
}

// Name implements [relay.Sink].
func (s *sink) Name() string { return s.name }

// Deliver implements [relay.Sink].
func (s *sink) Deliver(_ context.Context, attempt relay.Attempt) relay.Outcome {
	s.mu.Lock()
	s.offered = append(s.offered, attempt.Event.ID)
	s.deliveries = append(s.deliveries, attempt.DeliveryID)
	s.attempts = append(s.attempts, attempt.Number)
	verdict := s.verdict
	s.mu.Unlock()

	return verdict(attempt.Event)
}

// delivered returns the delivery identifiers this sink was handed, in order, so
// that a case can assert a redelivery is distinguishable from a first attempt.
func (s *sink) delivered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.deliveries)
}

// attemptNumbers returns the attempt number carried by each delivery, in order.
func (s *sink) attemptNumbers() []int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.attempts)
}

// answer changes what the sink says from the next delivery onwards, which is
// how a case models a destination that recovers.
func (s *sink) answer(verdict func(event hmntsk.Event) relay.Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.verdict = verdict
}

// seen returns the identifiers this sink was offered, in order and with
// repeats, so that a case can assert both what arrived and how often.
func (s *sink) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.offered...)
}

// fixture is one store, one clock and the events seeded into it.
type fixture struct {
	t      *testing.T
	store  hmntsk.Store
	clock  *clock
	engine *hmntsk.Service
	errors *collector
}

// collector gathers what a relay reported to its host.
type collector struct {
	mu     sync.Mutex
	errors []error
}

// record implements the relay's error handler.
func (c *collector) record(_ context.Context, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.errors = append(c.errors, err)
}

// all returns everything reported so far.
func (c *collector) all() []error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]error(nil), c.errors...)
}

// newFixture builds a store from the factory, an engine over it whose clock the
// case drives by hand, and a collector for whatever the relay reports.
//
// The engine is a real [hmntsk.Service] rather than a stand-in for one, so that
// what these cases exercise is the path a host takes: the relay reaches the
// store through the engine's relay port, and a store that claims correctly
// under a hand-written transaction but not under that port fails here.
func newFixture(t *testing.T, factory Factory) *fixture {
	t.Helper()

	harness := factory(t)
	require.NotNil(t, harness.Store, "a harness must supply the store under test")

	tick := newClock()

	engine, err := hmntsk.New(harness.Store, hmntsk.WithClock(tick))
	require.NoError(t, err, "build the engine over the store under test")

	return &fixture{
		t:      t,
		store:  harness.Store,
		clock:  tick,
		engine: engine,
		errors: &collector{},
	}
}

// event builds an event that occurred at an offset from the reference. An event
// is due as soon as it is recorded, so a negative offset is a backlog.
func event(id string, offset time.Duration) hmntsk.Event {
	return hmntsk.Event{
		ID:         id,
		Type:       hmntsk.EventTypeCreated,
		TaskID:     hmntsk.TaskID("task-" + id),
		TaskType:   "approval",
		Status:     hmntsk.StatusReady,
		Version:    1,
		OccurredAt: reference.Add(offset),
	}
}

// seed records events durably, as the engine does inside the transaction that
// produced them.
func (f *fixture) seed(events ...hmntsk.Event) {
	f.t.Helper()

	err := f.store.Do(f.t.Context(), func(ctx context.Context) error {
		return f.store.Append(ctx, events)
	})
	require.NoError(f.t, err, "seed the outbox")
}

// entry reads one outbox entry back, which is how every case inspects what a
// pass decided.
func (f *fixture) entry(eventID string) hmntsk.OutboxEntry {
	f.t.Helper()

	entry, err := f.store.OutboxEntry(f.t.Context(), eventID)
	require.NoError(f.t, err, "read outbox entry %s", eventID)

	return entry
}

// relay builds a relay over the fixture's store with the suite's defaults: a
// legible backoff, no jitter unless a case asks for it, and an error handler
// that collects rather than discards.
func (f *fixture) relay(opts ...relay.RelayOption) *relay.Relay {
	f.t.Helper()

	defaults := []relay.RelayOption{
		relay.WithBackoff(backoff, time.Hour),
		relay.WithJitter(0),
		relay.WithRelayErrorHandler(f.errors.record),
	}

	built, err := relay.NewRelay(f.engine, append(defaults, opts...)...)
	require.NoError(f.t, err, "build the relay")

	return built
}

// pass runs one relay pass and fails the case if claiming itself failed.
func (f *fixture) pass(r *relay.Relay) relay.Result {
	f.t.Helper()

	result, err := r.Relay(f.t.Context())
	require.NoError(f.t, err, "a relay pass must not fail on claiming")

	return result
}

// due returns when the event becomes claimable again, failing the case if it
// never does.
//
// The instant is normalised the way the engine normalises every timestamp it
// stores, so that a dialect keeping coarser precision than the relay computes
// is compared on the terms the engine actually promises — and so that the whole
// suite has one notion of "the same stored instant" rather than one per case.
func (f *fixture) due(eventID string) time.Time {
	f.t.Helper()

	entry := f.entry(eventID)
	require.NotNilf(f.t, entry.NextAttemptAt, "event %s has no next attempt", eventID)

	return hmntsk.NormalizeTime(*entry.NextAttemptAt)
}

// waitUntilDue moves the clock to the moment the relay itself said the event
// becomes claimable again.
//
// Cases advance to the relay's own schedule rather than to a duration they
// worked out themselves, so that what they exercise is the retry the relay
// scheduled — a case that guessed would still pass against a relay that had
// scheduled something else entirely.
func (f *fixture) waitUntilDue(eventID string) {
	f.t.Helper()

	f.clock.advance(f.due(eventID).Sub(f.clock.Now()))
}
