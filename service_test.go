package hmntsk_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// recorder collects the events delivered to in-process consumers.
type recorder struct {
	mu     sync.Mutex
	events []hmntsk.Event
	err    error
}

func (r *recorder) HandleEvent(_ context.Context, event hmntsk.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, event)

	return r.err
}

func (r *recorder) delivered() []hmntsk.Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]hmntsk.Event, len(r.events))
	copy(out, r.events)

	return out
}

func (r *recorder) types() []hmntsk.EventType {
	delivered := r.delivered()

	out := make([]hmntsk.EventType, 0, len(delivered))
	for _, event := range delivered {
		out = append(out, event.Type)
	}

	return out
}

// harness is a wired engine over the in-memory store.
type harness struct {
	svc      *hmntsk.Service
	store    *memstore.Store
	recorder *recorder

	// currentID is the task a case is working on, so that shared assertion
	// helpers can read it back without threading it through.
	currentID hmntsk.TaskID
}

// newHarness wires a service whose approval type is claimable by alice and bob.
func newHarness(t *testing.T, opts ...hmntsk.Option) *harness {
	t.Helper()

	store := memstore.New()
	rec := &recorder{}

	base := append(baseOptions(t, approvalSpec()), hmntsk.WithEventHandlers(rec))

	svc, err := hmntsk.New(store, append(base, opts...)...)
	require.NoError(t, err)

	return &harness{svc: svc, store: store, recorder: rec}
}

// baseOptions is the wiring every engine test starts from: a registry holding
// the freeform type and any extra specs, the test directory and the fixed clock.
func baseOptions(t *testing.T, specs ...hmntsk.TypeSpec) []hmntsk.Option {
	t.Helper()

	registry := hmntsk.NewRegistry()
	for _, spec := range append(specs, hmntsk.TypeSpec{Name: "freeform"}) {
		require.NoError(t, registry.Register(spec))
	}

	return []hmntsk.Option{
		hmntsk.WithRegistry(registry),
		hmntsk.WithGroupResolver(testDirectory()),
		hmntsk.WithClock(hmntsk.ClockFunc(func() time.Time { return testNow })),
	}
}

// createApproval creates a pooled approval task claimable by alice and bob.
func (h *harness) createApproval(t *testing.T) hmntsk.Task {
	t.Helper()

	result, err := h.svc.Create(t.Context(), hmntsk.CreateRequest{
		Type:        "approval",
		Actor:       "system",
		Input:       json.RawMessage(`{"amount":100,"justification":"new laptop"}`),
		Candidates:  &hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
		Correlation: hmntsk.CorrelationData{OwnerType: "process", OwnerRef: "p-1"},
	})
	require.NoError(t, err)
	require.Equal(t, hmntsk.StatusReady, result.Task.Status)

	return result.Task
}

func TestServiceNewRejectsANonTransactionalSink(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		store  hmntsk.Store
		assert func(t *testing.T, svc *hmntsk.Service, err error)
	}

	cases := []testCase{
		{
			name:  "a transactional store is accepted",
			store: memstore.New(),
			assert: func(t *testing.T, svc *hmntsk.Service, err error) {
				require.NoError(t, err)
				assert.NotNil(t, svc)
			},
		},
		{
			name:  "a sink that cannot join the transaction is refused at construction",
			store: leakySink{Store: memstore.New()},
			assert: func(t *testing.T, svc *hmntsk.Service, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.Nil(t, svc)
				assert.Contains(t, err.Error(), "transaction")
			},
		},
		{
			name:  "no store at all is refused",
			store: nil,
			assert: func(t *testing.T, svc *hmntsk.Service, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.Nil(t, svc)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, err := hmntsk.New(tc.store)
			tc.assert(t, svc, err)
		})
	}
}

func TestServiceCreate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		request hmntsk.CreateRequest
		assert  func(t *testing.T, h *harness, result hmntsk.Result, err error)
	}

	cases := []testCase{
		{
			name: "a pooled task is created and announced",
			request: hmntsk.CreateRequest{
				Type:       "approval",
				Actor:      "system",
				Input:      json.RawMessage(`{"amount":100,"justification":"new laptop"}`),
				Candidates: &hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			},
			assert: func(t *testing.T, h *harness, result hmntsk.Result, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReady, result.Task.Status)
				assert.False(t, result.Task.ID.IsZero())
				assert.Equal(t, []hmntsk.EventType{hmntsk.EventTypeCreated}, h.recorder.types())
				assert.NotEmpty(t, h.recorder.delivered()[0].ID, "a durable event must be identifiable")
			},
		},
		{
			name: "a single-candidate task is reserved on creation",
			request: hmntsk.CreateRequest{
				Type:       "approval",
				Input:      json.RawMessage(`{"amount":1,"justification":"x"}`),
				Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
			},
			assert: func(t *testing.T, _ *harness, result hmntsk.Result, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, result.Task.Status)
				assert.Equal(t, "alice", result.Task.Assignee)
			},
		},
		{
			name: "a task nobody can do is faulted, not orphaned",
			request: hmntsk.CreateRequest{
				Type:       "approval",
				Input:      json.RawMessage(`{"amount":1,"justification":"x"}`),
				Candidates: &hmntsk.CandidatePool{Groups: []string{"empty-team"}},
			},
			assert: func(t *testing.T, h *harness, result hmntsk.Result, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusError, result.Task.Status)
				assert.Equal(t, []hmntsk.EventType{hmntsk.EventTypeErrored}, h.recorder.types())
			},
		},
		{
			name: "an unregistered type is refused and nothing is stored",
			request: hmntsk.CreateRequest{
				Type:       "approvel",
				Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
			},
			assert: func(t *testing.T, h *harness, _ hmntsk.Result, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnregisteredType)
				assert.Zero(t, h.store.Len())
				assert.Empty(t, h.recorder.delivered())
			},
		},
		{
			name: "an input that fails the schema is refused and nothing is stored",
			request: hmntsk.CreateRequest{
				Type:       "approval",
				Input:      json.RawMessage(`{"amount":"a lot"}`),
				Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
			},
			assert: func(t *testing.T, h *harness, _ hmntsk.Result, err error) {
				require.ErrorIs(t, err, hmntsk.ErrValidation)
				assert.Zero(t, h.store.Len())
				assert.Empty(t, h.recorder.delivered())
			},
		},
		{
			name: "the payload is stored byte for byte",
			request: hmntsk.CreateRequest{
				Type:       "freeform",
				Input:      json.RawMessage(`{"zulu":1,"seq":9007199254740993}`),
				Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
			},
			assert: func(t *testing.T, h *harness, result hmntsk.Result, err error) {
				require.NoError(t, err)

				stored, err := h.svc.Get(t.Context(), result.Task.ID)
				require.NoError(t, err)
				assertVerbatim(t, `{"zulu":1,"seq":9007199254740993}`, stored.Input)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			result, err := h.svc.Create(t.Context(), tc.request)
			tc.assert(t, h, result, err)
		})
	}
}

func TestServiceLifecycleHappyPath(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := t.Context()
	task := h.createApproval(t)

	claimed, err := h.svc.Claim(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReserved, claimed.Task.Status)
	assert.Equal(t, "alice", claimed.Task.Assignee)

	released, err := h.svc.Release(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReady, released.Task.Status)

	reclaimed, err := h.svc.Claim(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "bob"})
	require.NoError(t, err)
	assert.Equal(t, "bob", reclaimed.Task.Assignee)

	started, err := h.svc.Start(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "bob"})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusInProgress, started.Task.Status)

	delegated, err := h.svc.Delegate(ctx, hmntsk.DelegateRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "bob", Comment: "over to you"},
		Target:      "alice",
	})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReserved, delegated.Task.Status)
	assert.Equal(t, "alice", delegated.Task.Assignee)

	suspended, err := h.svc.Suspend(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusSuspended, suspended.Task.Status)

	resumed, err := h.svc.Resume(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReserved, resumed.Task.Status)

	restarted, err := h.svc.Start(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusInProgress, restarted.Task.Status)

	completed, err := h.svc.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"},
		Output:      json.RawMessage(`{"approved":false,"note":"over budget"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusCompleted, completed.Task.Status,
		"a denial is a completion; the outcome lives in the payload")

	assert.Equal(t, []hmntsk.EventType{
		hmntsk.EventTypeCreated,
		hmntsk.EventTypeClaimed,
		hmntsk.EventTypeReleased,
		hmntsk.EventTypeClaimed,
		hmntsk.EventTypeStarted,
		hmntsk.EventTypeDelegated,
		hmntsk.EventTypeSuspended,
		hmntsk.EventTypeResumed,
		hmntsk.EventTypeStarted,
		hmntsk.EventTypeCompleted,
	}, h.recorder.types())

	history, err := h.svc.History(ctx, task.ID)
	require.NoError(t, err)
	assert.Len(t, history, 10, "one history record per accepted transition")

	durable := h.store.Events()
	assert.Len(t, durable, 10, "every delivered event is also durable")
}

func TestServiceTerminalAndFailurePaths(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		act    func(ctx context.Context, h *harness, id hmntsk.TaskID) error
		assert func(t *testing.T, h *harness, task hmntsk.Task, err error)
	}

	cases := []testCase{
		{
			name: "the assignee can report they cannot do the work",
			act: func(ctx context.Context, h *harness, id hmntsk.TaskID) error {
				_, err := h.svc.Fail(ctx, hmntsk.TaskRequest{
					TaskID: id, Actor: "alice", Comment: "not my authority",
				})

				return err
			},
			assert: func(t *testing.T, h *harness, task hmntsk.Task, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusFailed, task.Status)
				assert.Contains(t, h.recorder.types(), hmntsk.EventTypeFailed)
			},
		},
		{
			name: "the owner can cancel in-flight work",
			act: func(ctx context.Context, h *harness, id hmntsk.TaskID) error {
				_, err := h.svc.Cancel(ctx, hmntsk.TaskRequest{
					TaskID: id, Actor: "owner", Comment: "no longer needed",
				})

				return err
			},
			assert: func(t *testing.T, h *harness, task hmntsk.Task, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusExited, task.Status)
				assert.Contains(t, h.recorder.types(), hmntsk.EventTypeCancelled)
			},
		},
		{
			name: "escalation widens the pool and announces itself",
			act: func(ctx context.Context, h *harness, id hmntsk.TaskID) error {
				_, err := h.svc.Escalate(ctx, hmntsk.TaskRequest{TaskID: id, Actor: "system"})

				return err
			},
			assert: func(t *testing.T, h *harness, task hmntsk.Task, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusInProgress, task.Status)
				assert.Contains(t, task.Candidates.Groups, "managers",
					"the type's default policy widens to managers")
				assert.Contains(t, task.Candidates.Groups, "finance-approvers",
					"previously eligible actors must remain eligible")
				assert.Contains(t, h.recorder.types(), hmntsk.EventTypeEscalated)
			},
		},
		{
			name: "an output missing a required field is refused",
			act: func(ctx context.Context, h *harness, id hmntsk.TaskID) error {
				_, err := h.svc.Complete(ctx, hmntsk.CompleteRequest{
					TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: "alice"},
					Output:      json.RawMessage(`{"note":"looks fine"}`),
				})

				return err
			},
			assert: func(t *testing.T, h *harness, task hmntsk.Task, err error) {
				require.ErrorIs(t, err, hmntsk.ErrValidation)
				assert.Equal(t, hmntsk.StatusInProgress, task.Status, "the task is unchanged")
				assert.NotContains(t, h.recorder.types(), hmntsk.EventTypeCompleted)
			},
		},
		{
			name: "a non-assignee is refused",
			act: func(ctx context.Context, h *harness, id hmntsk.TaskID) error {
				_, err := h.svc.Complete(ctx, hmntsk.CompleteRequest{
					TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: "bob"},
					Output:      json.RawMessage(`{"approved":true}`),
				})

				return err
			},
			assert: func(t *testing.T, h *harness, task hmntsk.Task, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnauthorized)
				assert.Equal(t, hmntsk.StatusInProgress, task.Status)
				assert.NotContains(t, h.recorder.types(), hmntsk.EventTypeCompleted)
			},
		},
		{
			name: "a stale version is refused and reports the current one",
			act: func(ctx context.Context, h *harness, id hmntsk.TaskID) error {
				stale := int64(1)
				_, err := h.svc.Complete(ctx, hmntsk.CompleteRequest{
					TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: "alice", Version: &stale},
					Output:      json.RawMessage(`{"approved":true}`),
				})

				return err
			},
			assert: func(t *testing.T, h *harness, task hmntsk.Task, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConflict)

				var conflict *hmntsk.ConflictError

				require.ErrorAs(t, err, &conflict)
				assert.Equal(t, task.Version, conflict.Current)
				assert.Equal(t, int64(1), conflict.Expected)
				assert.NotContains(t, h.recorder.types(), hmntsk.EventTypeCompleted)
			},
		},
		{
			name: "an unknown task is not found",
			act: func(ctx context.Context, h *harness, _ hmntsk.TaskID) error {
				_, err := h.svc.Claim(ctx, hmntsk.TaskRequest{TaskID: "no-such-task", Actor: "alice"})

				return err
			},
			assert: func(t *testing.T, _ *harness, _ hmntsk.Task, err error) {
				require.ErrorIs(t, err, hmntsk.ErrNotFound)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			ctx := t.Context()
			task := h.createApproval(t)

			_, err := h.svc.Claim(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
			require.NoError(t, err)

			_, err = h.svc.Start(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
			require.NoError(t, err)

			h.recorder.mu.Lock()
			h.recorder.events = nil
			h.recorder.mu.Unlock()

			actErr := tc.act(ctx, h, task.ID)

			stored, err := h.svc.Get(ctx, task.ID)
			require.NoError(t, err)

			tc.assert(t, h, stored, actErr)
		})
	}
}

func TestServiceConcurrentClaimHasExactlyOneWinner(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	task := h.createApproval(t)

	var (
		mu      sync.Mutex
		winners []string
		losers  []error
		wg      sync.WaitGroup
	)

	for _, actor := range []string{"alice", "bob"} {
		wg.Go(func() {
			_, err := h.svc.Claim(t.Context(), hmntsk.TaskRequest{TaskID: task.ID, Actor: actor})

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				losers = append(losers, err)

				return
			}

			winners = append(winners, actor)
		})
	}

	wg.Wait()

	assert.Len(t, winners, 1, "exactly one actor may win the claim")
	require.Len(t, losers, 1)
	assert.ErrorIs(t, losers[0], hmntsk.ErrConflict)
}

// leakySink is a store whose event sink cannot join a transaction.
type leakySink struct{ hmntsk.Store }

func (leakySink) Transactional() bool { return false }
