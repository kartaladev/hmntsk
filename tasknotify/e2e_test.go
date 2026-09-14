package tasknotify_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
	"github.com/kartaladev/hmntsk/tasknotify"
)

// e2e wires the engine, the relay and the projector over one database.
type e2e struct {
	t        *testing.T
	engine   *hmntsk.Service
	notifier *notify.Service
	relay    *relay.Relay
	recorder *recorder
	clock    *steppedClock
	store    *flakyStore
	reports  *reportLog
}

// e2eConfig adjusts the wiring.
type e2eConfig struct {
	opts []tasknotify.Option
}

func newE2E(t *testing.T, b backend, db *sql.DB, cfg e2eConfig) *e2e {
	t.Helper()

	s := newStores(t, b, db)
	clock := &steppedClock{}

	engine, err := hmntsk.New(s.engine,
		hmntsk.WithClock(clock),
		hmntsk.WithGroupResolver(hmntsk.NewStaticAssignment(map[string][]string{
			"approvers": {"bob", "carol"},
			"managers":  {"frank"},
		})),
	)
	require.NoError(t, err)
	require.NoError(t, engine.Register(hmntsk.TypeSpec{Name: "approval"}))

	store := &flakyStore{Store: s.notify}

	notifier, err := notify.New(store)
	require.NoError(t, err)

	reports := &reportLog{}

	projector, err := tasknotify.New(engine, notifier,
		append([]tasknotify.Option{tasknotify.WithErrorHandler(reports.add)}, cfg.opts...)...)
	require.NoError(t, err)

	rec := &recorder{}

	r, err := relay.NewRelay(engine,
		relay.WithSinks(projector, rec),
		relay.WithBackoff(time.Second, time.Minute),
	)
	require.NoError(t, err)

	return &e2e{t: t, engine: engine, notifier: notifier, relay: r, recorder: rec, clock: clock, store: store, reports: reports}
}

// pass runs one relay pass and returns its result.
func (e *e2e) pass() relay.Result {
	e.t.Helper()

	result, err := e.relay.Relay(e.t.Context())
	require.NoError(e.t, err)

	return result
}

// later moves the clock past any retry delay, so the next pass retries.
func (e *e2e) later() { e.clock.advance(2 * time.Hour) }

// create creates a pooled approval task and returns its identifier.
func (e *e2e) create(pool hmntsk.CandidatePool, escalation *hmntsk.EscalationPolicy) hmntsk.TaskID {
	e.t.Helper()

	result, err := e.engine.Create(e.t.Context(), hmntsk.CreateRequest{
		Type: "approval", Actor: "owner", Candidates: &pool, Escalation: escalation,
	})
	require.NoError(e.t, err)

	return result.Task.ID
}

// act performs a lifecycle operation.
func (e *e2e) act(op func(ctx context.Context) (hmntsk.Result, error)) {
	e.t.Helper()

	_, err := op(e.t.Context())
	require.NoError(e.t, err)
}

// count counts a recipient's notifications on a task of a kind in a state.
func (e *e2e) count(task hmntsk.TaskID, recipient, kind string, state notify.State) int {
	e.t.Helper()

	page, err := e.notifier.List(e.t.Context(), notify.ListQuery{
		Recipient: recipient, Subject: string(task), Kinds: []string{kind}, States: []notify.State{state},
		Limit: notify.MaxListLimit,
	})
	require.NoError(e.t, err)

	return len(page.Notifications)
}

// all lists a recipient's notifications on a task.
func (e *e2e) all(task hmntsk.TaskID, recipient string) []notify.Notification {
	e.t.Helper()

	page, err := e.notifier.List(e.t.Context(), notify.ListQuery{
		Recipient: recipient, Subject: string(task), Limit: notify.MaxListLimit,
	})
	require.NoError(e.t, err)

	return page.Notifications
}

func req(task hmntsk.TaskID, actor string) hmntsk.TaskRequest {
	return hmntsk.TaskRequest{TaskID: task, Actor: actor}
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()

	three := hmntsk.CandidatePool{Users: []string{"alice", "bob", "carol"}}

	type testCase struct {
		name string
		cfg  e2eConfig
		run  func(t *testing.T, e *e2e)
	}

	cases := []testCase{
		{
			name: "in order, every event is projected and completion closes everything",
			run: func(t *testing.T, e *e2e) {
				task := e.create(hmntsk.CandidatePool{Users: []string{"alice", "carol"}, Groups: []string{"approvers"}},
					&hmntsk.EscalationPolicy{Action: hmntsk.EscalationWiden, AddGroups: []string{"managers"}})
				e.pass()

				assert.Equal(t, 1, e.count(task, "bob", tasknotify.KindOffer, notify.StateActive))

				e.act(func(ctx context.Context) (hmntsk.Result, error) { return e.engine.Escalate(ctx, req(task, "sweeper")) })
				e.pass()

				assert.Equal(t, 1, e.count(task, "frank", tasknotify.KindOffer, notify.StateActive), "widening offers the task")
				assert.Len(t, e.all(task, "alice"), 1, "widening does not offer it twice")

				e.act(func(ctx context.Context) (hmntsk.Result, error) { return e.engine.Claim(ctx, req(task, "carol")) })
				e.pass()

				assert.Equal(t, 1, e.count(task, "alice", tasknotify.KindTaken, notify.StateActive))
				assert.Zero(t, e.count(task, "carol", tasknotify.KindTaken, notify.StateActive))

				e.act(func(ctx context.Context) (hmntsk.Result, error) { return e.engine.Release(ctx, req(task, "carol")) })
				e.pass()

				assert.Equal(t, 1, e.count(task, "alice", tasknotify.KindOffer, notify.StateActive), "released work is offered again")
				assert.Zero(t, e.count(task, "alice", tasknotify.KindTaken, notify.StateActive))

				e.act(func(ctx context.Context) (hmntsk.Result, error) { return e.engine.Claim(ctx, req(task, "bob")) })
				e.act(func(ctx context.Context) (hmntsk.Result, error) {
					return e.engine.Delegate(ctx, hmntsk.DelegateRequest{TaskRequest: req(task, "bob"), Target: "alice"})
				})
				e.pass()

				assert.Equal(t, 1, e.count(task, "alice", tasknotify.KindAssigned, notify.StateActive))

				e.act(func(ctx context.Context) (hmntsk.Result, error) { return e.engine.Start(ctx, req(task, "alice")) })
				e.act(func(ctx context.Context) (hmntsk.Result, error) {
					return e.engine.Complete(ctx, hmntsk.CompleteRequest{TaskRequest: req(task, "alice"), Output: json.RawMessage(`{}`)})
				})

				result := e.pass()
				assert.Zero(t, result.DeadLettered)

				for _, recipient := range []string{"alice", "bob", "carol", "frank"} {
					for _, n := range e.all(task, recipient) {
						assert.Equal(t, notify.StateClosed, n.State, "%s %s", recipient, n.Kind)
					}
				}

				assert.Equal(t, 8, e.recorder.count(), "the other sink received every event")
			},
		},
		{
			name: "a transient store failure on a claim is retried on the next pass",
			run: func(t *testing.T, e *e2e) {
				task := e.create(three, nil)
				e.pass()

				e.store.failCloses(1)
				e.act(func(ctx context.Context) (hmntsk.Result, error) { return e.engine.Claim(ctx, req(task, "carol")) })

				first := e.pass()
				assert.Equal(t, 1, first.Retried, "the claim is scheduled again")

				e.later()

				second := e.pass()
				assert.Equal(t, 1, second.Delivered)

				for _, recipient := range []string{"alice", "bob"} {
					assert.Equal(t, 1, e.count(task, recipient, tasknotify.KindTaken, notify.StateActive), recipient)
				}

				assert.Zero(t, e.count(task, "carol", tasknotify.KindTaken, notify.StateActive))
				assert.Equal(t, 2, e.recorder.count(), "the other sink took each event once")
			},
		},
		{
			name: "an older event retried after a newer one never reopens its offers",
			run: func(t *testing.T, e *e2e) {
				e.store.failInserts(1)

				task := e.create(three, nil)
				e.act(func(ctx context.Context) (hmntsk.Result, error) { return e.engine.Claim(ctx, req(task, "carol")) })

				first := e.pass()
				assert.Equal(t, 1, first.Retried, "the creation is retried")
				assert.Equal(t, 1, first.Delivered, "the claim goes ahead of it")

				e.later()
				e.pass()

				for _, recipient := range []string{"alice", "bob", "carol"} {
					assert.Zero(t, e.count(task, recipient, tasknotify.KindOffer, notify.StateActive), recipient)
					assert.Zero(t, e.count(task, recipient, tasknotify.KindTaken, notify.StateActive), recipient)
				}
			},
		},
		{
			name: "a projection no retry can fix is reported and the other sink still gets the event",
			cfg: e2eConfig{opts: []tasknotify.Option{tasknotify.WithData(
				func(context.Context, tasknotify.DraftInput) (json.RawMessage, error) {
					return json.RawMessage("not json"), nil
				})}},
			run: func(t *testing.T, e *e2e) {
				task := e.create(three, nil)

				result := e.pass()
				assert.Equal(t, 1, result.Delivered)
				assert.Zero(t, result.DeadLettered)
				assert.Zero(t, result.Retried)

				assert.Equal(t, 1, e.recorder.count())
				require.Len(t, e.reports.all(), 1)
				assert.ErrorIs(t, e.reports.all()[0], notify.ErrValidation)
				assert.Empty(t, e.all(task, "alice"))
			},
		},
	}

	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			db := openDatabase(t, b)

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					tc.run(t, newE2E(t, b, db, tc.cfg))
				})
			}
		})
	}
}

// steppedClock is the system clock moved forward by however much a test has
// advanced it, so that a retry delay can pass without waiting for it.
type steppedClock struct{ offset atomic.Int64 }

func (c *steppedClock) Now() time.Time { return time.Now().Add(time.Duration(c.offset.Load())) }

func (c *steppedClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// flakyStore fails a set number of closes or inserts before reaching the store,
// the way an unavailable database would.
type flakyStore struct {
	notify.Store

	closes  atomic.Int32
	inserts atomic.Int32
}

func (s *flakyStore) failCloses(n int32)  { s.closes.Store(n) }
func (s *flakyStore) failInserts(n int32) { s.inserts.Store(n) }

func (s *flakyStore) Close(
	ctx context.Context, req notify.CloseRequest, at time.Time, ids notify.IDGenerator,
) (notify.CloseResult, error) {
	if s.closes.Add(-1) >= 0 {
		return notify.CloseResult{}, notify.ErrUnavailable
	}

	return s.Store.Close(ctx, req, at, ids)
}

func (s *flakyStore) Insert(ctx context.Context, subject string, insertions []notify.Insertion) (notify.InsertResult, error) {
	if s.inserts.Add(-1) >= 0 {
		return notify.InsertResult{}, notify.ErrUnavailable
	}

	return s.Store.Insert(ctx, subject, insertions)
}

// recorder is a second sink that accepts every event.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Deliver(_ context.Context, attempt relay.Attempt) relay.Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, attempt.Event.ID)

	return relay.Delivered()
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.events)
}

// reportLog records the failures a projector reports.
type reportLog struct {
	mu   sync.Mutex
	errs []error
}

func (l *reportLog) add(_ context.Context, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errs = append(l.errs, err)
}

func (l *reportLog) all() []error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]error(nil), l.errs...)
}
