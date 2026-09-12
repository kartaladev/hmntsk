package hmntsk_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// sweepHarness is an engine whose clock the test moves by hand.
type sweepHarness struct {
	svc      *hmntsk.Service
	store    *memstore.Store
	recorder *recorder
	now      func() time.Time
	advance  func(d time.Duration)
}

// newSweepHarness wires an engine whose approval type widens to managers when
// it runs late.
func newSweepHarness(t *testing.T, spec hmntsk.TypeSpec) *sweepHarness {
	t.Helper()

	registry := hmntsk.NewRegistry()
	require.NoError(t, registry.Register(spec))

	var (
		mu      sync.Mutex
		current = testNow
	)

	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		return current
	}

	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()

		current = current.Add(d)
	}

	store := memstore.New()
	rec := &recorder{}

	svc, err := hmntsk.New(store,
		hmntsk.WithRegistry(registry),
		hmntsk.WithGroupResolver(testDirectory()),
		hmntsk.WithClock(hmntsk.ClockFunc(now)),
		hmntsk.WithEventHandlers(rec),
	)
	require.NoError(t, err)

	return &sweepHarness{svc: svc, store: store, recorder: rec, now: now, advance: advance}
}

// overdueSpec is an approval type that is late after an hour and widens to
// managers.
func overdueSpec(mutate ...func(spec *hmntsk.TypeSpec)) hmntsk.TypeSpec {
	spec := hmntsk.TypeSpec{
		Name:            "approval",
		DefaultDeadline: time.Hour,
		DefaultEscalation: &hmntsk.EscalationPolicy{
			Action:    hmntsk.EscalationWiden,
			AddGroups: []string{"managers"},
		},
	}

	for _, apply := range mutate {
		apply(&spec)
	}

	return spec
}

// create makes an approval task pooled to finance-approvers.
func (h *sweepHarness) create(t *testing.T) hmntsk.Task {
	t.Helper()

	result, err := h.svc.Create(t.Context(), hmntsk.CreateRequest{
		Type:       "approval",
		Actor:      "system",
		Input:      json.RawMessage(`{}`),
		Candidates: &hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
		Callback: &hmntsk.CallbackTarget{
			Address:             "https://host.example/hook",
			ReferenceParameters: json.RawMessage(`{"corr":"abc"}`),
		},
	})
	require.NoError(t, err)

	return result.Task
}

func TestSweepEscalatesOverdueWorkOnly(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())
	task := h.create(t)

	sweeper, err := hmntsk.NewSweeper(h.svc, hmntsk.WithSweepOwner("sweeper-1"))
	require.NoError(t, err)

	result, err := sweeper.Sweep(t.Context())
	require.NoError(t, err)
	assert.Zero(t, result.Claimed, "a task within its deadline is not swept")

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReady, stored.Status)
	assert.Empty(t, stored.Candidates.Groups[1:], "nothing was widened")

	h.advance(2 * time.Hour)

	result, err = sweeper.Sweep(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Claimed)
	assert.Equal(t, 1, result.Escalated)
	require.Len(t, result.Events, 1)
	assert.Equal(t, hmntsk.EventTypeEscalated, result.Events[0].Type)

	stored, err = h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReady, stored.Status, "widening does not move the task")
	assert.Equal(t, []string{"finance-approvers", "managers"}, stored.Candidates.Groups,
		"previously eligible actors must remain eligible after widening")
	assert.Equal(t, 1, stored.EscalationCount)

	eligible, err := hmntsk.IsEligible(t.Context(), testDirectory(), stored.Candidates, "alice")
	require.NoError(t, err)
	assert.True(t, eligible, "the original pool must still be able to claim the task")
}

func TestReadingAnOverdueTaskDoesNotEscalateIt(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())
	task := h.create(t)

	h.advance(2 * time.Hour)

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, stored.EscalationCount, "deadlines are evaluated off the request path")
	assert.Equal(t, []string{"finance-approvers"}, stored.Candidates.Groups)
	assert.NotContains(t, h.recorder.types(), hmntsk.EventTypeEscalated)

	page, err := h.svc.Query(t.Context(), hmntsk.Query{Candidate: "alice"})
	require.NoError(t, err)
	require.Len(t, page.Tasks, 1)
	assert.Equal(t, 0, page.Tasks[0].EscalationCount, "nor does a query")
}

func TestTwoSweepsEscalateEachTaskExactlyOnce(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())

	const tasks = 6

	for range tasks {
		h.create(t)
	}

	h.advance(2 * time.Hour)

	first, err := hmntsk.NewSweeper(h.svc, hmntsk.WithSweepOwner("sweeper-1"))
	require.NoError(t, err)

	second, err := hmntsk.NewSweeper(h.svc, hmntsk.WithSweepOwner("sweeper-2"))
	require.NoError(t, err)

	var (
		mu      sync.Mutex
		results []hmntsk.SweepResult
		wg      sync.WaitGroup
	)

	for _, sweeper := range []*hmntsk.Sweeper{first, second} {
		wg.Go(func() {
			result, err := sweeper.Sweep(t.Context())
			if err != nil {
				t.Error(err)

				return
			}

			mu.Lock()
			defer mu.Unlock()

			results = append(results, result)
		})
	}

	wg.Wait()

	escalated := 0
	for _, result := range results {
		escalated += result.Escalated
	}

	assert.Equal(t, tasks, escalated, "every overdue task must be escalated")

	page, err := h.svc.Query(t.Context(), hmntsk.Query{})
	require.NoError(t, err)
	require.Len(t, page.Tasks, tasks)

	for _, task := range page.Tasks {
		assert.Equalf(t, 1, task.EscalationCount,
			"task %s was escalated %d times", task.ID, task.EscalationCount)
	}

	events := 0

	for _, event := range h.store.Events() {
		if event.Type == hmntsk.EventTypeEscalated {
			events++
		}
	}

	assert.Equal(t, tasks, events, "one escalation event per task, never two")
}

func TestALeaseKeepsTheNextSweepOffAndThenExpires(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())
	task := h.create(t)

	h.advance(2 * time.Hour)

	sweeper, err := hmntsk.NewSweeper(h.svc,
		hmntsk.WithSweepOwner("sweeper-1"),
		hmntsk.WithLeaseDuration(10*time.Minute))
	require.NoError(t, err)

	first, err := sweeper.Sweep(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, first.Escalated)

	// Widening does not move the deadline, so the task is overdue again the
	// instant the escalation commits. The lease is what stops it being
	// escalated over and over.
	again, err := sweeper.Sweep(t.Context())
	require.NoError(t, err)
	assert.Zero(t, again.Claimed, "a leased task is not claimable")

	h.advance(11 * time.Minute)

	afterExpiry, err := sweeper.Sweep(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, afterExpiry.Claimed, "an expired lease makes the task available again")

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, stored.EscalationCount)
}

func TestACrashedSweeperDoesNotStrandATaskForever(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())
	task := h.create(t)

	h.advance(2 * time.Hour)

	// A sweeper takes a lease and is never heard from again.
	claimed, err := h.svc.ClaimOverdue(t.Context(), hmntsk.LeaseRequest{
		Now: h.now(), Owner: "crashed-sweeper", Duration: 5 * time.Minute, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	survivor, err := hmntsk.NewSweeper(h.svc,
		hmntsk.WithSweepOwner("sweeper-2"),
		hmntsk.WithLeaseDuration(time.Minute))
	require.NoError(t, err)

	blocked, err := survivor.Sweep(t.Context())
	require.NoError(t, err)
	assert.Zero(t, blocked.Claimed, "the abandoned lease still holds")

	h.advance(6 * time.Minute)

	recovered, err := survivor.Sweep(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, recovered.Escalated, "once the lease expires the task is picked up again")

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, stored.EscalationCount)
	assert.Equal(t, "sweeper-2", stored.LockedBy)
}

func TestSweepExclusions(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		spec    hmntsk.TypeSpec
		prepare func(t *testing.T, h *sweepHarness, id hmntsk.TaskID)
		assert  func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task)
	}

	untouched := func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
		assert.Zero(t, result.Escalated)
		assert.Zero(t, stored.EscalationCount)
	}

	cases := []testCase{
		{
			name: "a completed task is never escalated",
			spec: overdueSpec(),
			prepare: func(t *testing.T, h *sweepHarness, id hmntsk.TaskID) {
				claimAndFinish(t, h, id, func(t *testing.T, h *sweepHarness, id hmntsk.TaskID) {
					_, err := h.svc.Complete(t.Context(), hmntsk.CompleteRequest{
						TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: "alice"},
						Output:      json.RawMessage(`{"approved":true}`),
					})
					require.NoError(t, err)
				})
			},
			assert: func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
				assert.Zero(t, result.Claimed, "a closed task is not even claimable")
				assert.Equal(t, hmntsk.StatusCompleted, stored.Status)
				untouched(t, result, stored)
			},
		},
		{
			name: "a cancelled task is never escalated",
			spec: overdueSpec(),
			prepare: func(t *testing.T, h *sweepHarness, id hmntsk.TaskID) {
				_, err := h.svc.Cancel(t.Context(), hmntsk.TaskRequest{TaskID: id, Actor: "owner"})
				require.NoError(t, err)
			},
			assert: func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
				assert.Zero(t, result.Claimed)
				assert.Equal(t, hmntsk.StatusExited, stored.Status)
				untouched(t, result, stored)
			},
		},
		{
			name: "a suspended task is never escalated",
			spec: overdueSpec(),
			prepare: func(t *testing.T, h *sweepHarness, id hmntsk.TaskID) {
				_, err := h.svc.Suspend(t.Context(), hmntsk.TaskRequest{TaskID: id, Actor: "owner"})
				require.NoError(t, err)
			},
			assert: func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
				assert.Zero(t, result.Claimed, "a suspended task is out of circulation")
				assert.Equal(t, hmntsk.StatusSuspended, stored.Status)
				untouched(t, result, stored)
			},
		},
		{
			name: "a policy may exempt work already started",
			spec: overdueSpec(func(spec *hmntsk.TypeSpec) {
				spec.DefaultEscalation.ExemptInProgress = true
			}),
			prepare: func(t *testing.T, h *sweepHarness, id hmntsk.TaskID) {
				claimAndFinish(t, h, id, nil)
			},
			assert: func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
				assert.Equal(t, 1, result.Claimed, "it is overdue and claimable")
				assert.Equal(t, 1, result.Exempted, "but the policy says leave it alone")
				assert.Equal(t, hmntsk.StatusInProgress, stored.Status)
				untouched(t, result, stored)
			},
		},
		{
			name: "without the exemption, work already started is escalated",
			spec: overdueSpec(),
			prepare: func(t *testing.T, h *sweepHarness, id hmntsk.TaskID) {
				claimAndFinish(t, h, id, nil)
			},
			assert: func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
				assert.Equal(t, 1, result.Escalated)
				assert.Equal(t, hmntsk.StatusInProgress, stored.Status)
				assert.Equal(t, 1, stored.EscalationCount)
			},
		},
		{
			name: "a policy may cap how often a task is escalated",
			spec: overdueSpec(func(spec *hmntsk.TypeSpec) {
				spec.DefaultEscalation.MaxEscalations = 1
			}),
			prepare: func(t *testing.T, h *sweepHarness, _ hmntsk.TaskID) {
				sweeper, err := hmntsk.NewSweeper(h.svc, hmntsk.WithLeaseDuration(time.Minute))
				require.NoError(t, err)

				result, err := sweeper.Sweep(t.Context())
				require.NoError(t, err)
				require.Equal(t, 1, result.Escalated)

				h.advance(2 * time.Minute)
			},
			assert: func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
				assert.Equal(t, 1, result.Claimed)
				assert.Equal(t, 1, result.Exempted)
				assert.Equal(t, 1, stored.EscalationCount, "the cap holds")
			},
		},
		{
			name:    "a task with no deadline is never swept",
			spec:    overdueSpec(func(spec *hmntsk.TypeSpec) { spec.DefaultDeadline = 0 }),
			prepare: func(*testing.T, *sweepHarness, hmntsk.TaskID) {},
			assert: func(t *testing.T, result hmntsk.SweepResult, stored hmntsk.Task) {
				assert.Zero(t, result.Claimed)
				assert.Nil(t, stored.DueAt)
				untouched(t, result, stored)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newSweepHarness(t, tc.spec)
			task := h.create(t)

			h.advance(2 * time.Hour)
			tc.prepare(t, h, task.ID)

			sweeper, err := hmntsk.NewSweeper(h.svc, hmntsk.WithSweepOwner("sweeper-1"))
			require.NoError(t, err)

			result, err := sweeper.Sweep(t.Context())
			require.NoError(t, err)

			stored, err := h.svc.Get(t.Context(), task.ID)
			require.NoError(t, err)

			tc.assert(t, result, stored)
		})
	}
}

// claimAndFinish claims and starts a task, then optionally does one more thing
// with it.
func claimAndFinish(t *testing.T, h *sweepHarness, id hmntsk.TaskID,
	then func(t *testing.T, h *sweepHarness, id hmntsk.TaskID),
) {
	t.Helper()

	_, err := h.svc.Claim(t.Context(), hmntsk.TaskRequest{TaskID: id, Actor: "alice"})
	require.NoError(t, err)

	_, err = h.svc.Start(t.Context(), hmntsk.TaskRequest{TaskID: id, Actor: "alice"})
	require.NoError(t, err)

	if then != nil {
		then(t, h, id)
	}
}

func TestEscalationAnnouncesAndSendsNothingItself(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())
	task := h.create(t)

	h.advance(2 * time.Hour)

	sweeper, err := hmntsk.NewSweeper(h.svc, hmntsk.WithSweepOwner("sweeper-1"))
	require.NoError(t, err)

	before := len(h.recorder.delivered())

	_, err = sweeper.Sweep(t.Context())
	require.NoError(t, err)

	delivered := h.recorder.delivered()
	require.Len(t, delivered, before+1, "exactly one event, and nothing else")

	event := delivered[len(delivered)-1]
	assert.Equal(t, hmntsk.EventTypeEscalated, event.Type)
	assert.Equal(t, task.ID, event.TaskID)
	assert.Equal(t, "deadline passed", event.Transition.Comment)
	assert.Equal(t, "sweeper-1", event.Actor)

	// The engine has no notion of a notification channel at all: the whole of
	// what it does about a late task is say so. Delivery is a consumer's job,
	// which is why the callback target rides along on the event untouched.
	require.NotNil(t, event.Callback)
	assert.Equal(t, "https://host.example/hook", event.Callback.Address)
	assertVerbatim(t, `{"corr":"abc"}`, event.Callback.ReferenceParameters,
		"the reference parameters ride along untouched, for whoever does deliver")
}

func TestManualEscalationTakesTheSamePathAsASweep(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())
	task := h.create(t)

	result, err := h.svc.Escalate(t.Context(), hmntsk.TaskRequest{
		TaskID: task.ID, Actor: "operator", Comment: "customer complained",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"finance-approvers", "managers"}, result.Task.Candidates.Groups)
	require.Len(t, result.Events, 1)
	assert.Equal(t, hmntsk.EventTypeEscalated, result.Events[0].Type)

	history, err := h.svc.History(t.Context(), task.ID)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, hmntsk.OpEscalate, history[1].Operation)
	assert.Equal(t, "operator", history[1].Actor)
	assert.Equal(t, "customer complained", history[1].Comment)
}

func TestSupersessionClosesTheTaskAsObsolete(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec(func(spec *hmntsk.TypeSpec) {
		spec.DefaultEscalation = &hmntsk.EscalationPolicy{Action: hmntsk.EscalationSupersede}
	}))
	task := h.create(t)

	h.advance(2 * time.Hour)

	sweeper, err := hmntsk.NewSweeper(h.svc, hmntsk.WithSweepOwner("sweeper-1"))
	require.NoError(t, err)

	result, err := sweeper.Sweep(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, result.Escalated)
	require.Len(t, result.Events, 1)
	assert.Equal(t, hmntsk.EventTypeObsoleted, result.Events[0].Type)

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusObsolete, stored.Status)

	_, err = h.svc.Claim(t.Context(), hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	assert.ErrorIs(t, err, hmntsk.ErrConflict, "a superseded task accepts nothing further")
}

// TestNothingStartsOnItsOwn is the assertion behind "the host controls when
// sweeps run".
//
// Constructing an engine and a sweeper must start no goroutine, no timer and no
// polling. An embedded library does not get to decide that the process it lives
// in now has a background thread.
func TestNothingStartsOnItsOwn(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := newSweepHarness(t, overdueSpec())
	h.create(t)

	sweeper, err := hmntsk.NewSweeper(h.svc)
	require.NoError(t, err)
	require.NotEmpty(t, sweeper.Owner())

	h.advance(2 * time.Hour)

	// Time has passed and a task is overdue, and still nothing has happened,
	// because nobody asked.
	page, err := h.svc.Query(t.Context(), hmntsk.Query{})
	require.NoError(t, err)
	require.Len(t, page.Tasks, 1)
	assert.Zero(t, page.Tasks[0].EscalationCount)
	assert.Empty(t, page.Tasks[0].LockedBy, "no sweep has taken a lease")
}

// TestRunStopsWhenTheHostStopsIt pins the other half: when the host does start
// a sweep loop, cancelling its context ends it and leaves nothing running.
func TestRunStopsWhenTheHostStopsIt(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := newSweepHarness(t, overdueSpec())
	h.create(t)
	h.advance(2 * time.Hour)

	sweeper, err := hmntsk.NewSweeper(h.svc, hmntsk.WithSweepOwner("sweeper-1"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() { done <- sweeper.Run(ctx, time.Millisecond) }()

	assert.Eventually(t, func() bool {
		stored, err := h.svc.Query(t.Context(), hmntsk.Query{})

		return err == nil && len(stored.Tasks) == 1 && stored.Tasks[0].EscalationCount > 0
	}, 5*time.Second, 5*time.Millisecond, "a running sweeper must escalate the overdue task")

	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep loop did not stop when its context was cancelled")
	}
}

func TestRunRefusesANonPositiveInterval(t *testing.T) {
	t.Parallel()

	h := newSweepHarness(t, overdueSpec())

	sweeper, err := hmntsk.NewSweeper(h.svc)
	require.NoError(t, err)

	assert.ErrorIs(t, sweeper.Run(t.Context(), 0), hmntsk.ErrConfiguration)
}
