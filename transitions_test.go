package hmntsk_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

const (
	testActor = "alice"
	testOther = "bob"
)

var testNow = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// fixture returns a task in the given state, held by testActor whenever the
// state implies somebody holds it.
func fixture(status hmntsk.Status) hmntsk.Task {
	task := hmntsk.Task{
		ID:         "019243af-9f1c-7000-8000-0123456789ab",
		Type:       "approval",
		Version:    4,
		Status:     status,
		Priority:   hmntsk.PriorityDefault,
		Candidates: hmntsk.CandidatePool{Users: []string{testActor, testOther}},
		Correlation: hmntsk.CorrelationData{
			OwnerType: "process", OwnerRef: "p-1", ActivityKey: "approve",
		},
		Callback:  &hmntsk.CallbackTarget{Address: "https://host.example/hook"},
		Input:     json.RawMessage(`{"amount":10}`),
		CreatedAt: testNow.Add(-time.Hour),
		UpdatedAt: testNow.Add(-time.Hour),
	}

	switch status {
	case hmntsk.StatusReserved, hmntsk.StatusInProgress:
		task.Assignee = testActor
	case hmntsk.StatusCreated, hmntsk.StatusReady, hmntsk.StatusSuspended,
		hmntsk.StatusCompleted, hmntsk.StatusFailed, hmntsk.StatusError,
		hmntsk.StatusExited, hmntsk.StatusObsolete:
	}

	if status == hmntsk.StatusInProgress {
		started := testNow.Add(-30 * time.Minute)
		task.StartedAt = &started
		task.Progress = json.RawMessage(`{"note":"halfway"}`)
	}

	return task
}

// suspended returns a task suspended out of the given state.
func suspended(from hmntsk.Status) hmntsk.Task {
	task := fixture(from)
	task.SuspendedFrom = from
	task.Status = hmntsk.StatusSuspended

	return task
}

type transitionCase struct {
	name   string
	task   hmntsk.Task
	invoke func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error)
	assert func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error)
}

// runTransitionCases executes the shared checks that hold for every transition —
// purity, exactly one event per accepted transition, one complete history
// record, a version that advanced — and then the case's own assertions.
func runTransitionCases(t *testing.T, cases []transitionCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			before, err := json.Marshal(tc.task)
			require.NoError(t, err)

			next, events, err := tc.invoke(tc.task)

			after, marshalErr := json.Marshal(tc.task)
			require.NoError(t, marshalErr)
			assert.JSONEq(t, string(before), string(after),
				"the receiver must not be mutated, on success or on failure")

			if err == nil {
				require.Len(t, events, 1, "exactly one event per accepted transition")

				event := events[0]
				assert.True(t, event.Type.Valid(), "event type must be in the closed catalogue")
				assert.Equal(t, next.Status, event.Status)
				assert.Equal(t, next.Version, event.Version)
				assert.Equal(t, tc.task.Version+1, next.Version, "version must advance")
				assert.Equal(t, hmntsk.NormalizeTime(testNow), next.UpdatedAt)

				record := event.Transition
				assert.Equal(t, tc.task.ID, record.TaskID)
				assert.Equal(t, tc.task.Status, record.From)
				assert.Equal(t, next.Status, record.To)
				assert.Equal(t, next.Version, record.Version)
				assert.Equal(t, hmntsk.NormalizeTime(testNow), record.At)
				assert.NotEmpty(t, record.Operation)
			} else {
				assert.Empty(t, events, "a refused operation produces no event")
			}

			tc.assert(t, next, events, err)
		})
	}
}

func TestTransitionsHappyPaths(t *testing.T) {
	t.Parallel()

	runTransitionCases(t, []transitionCase{
		{
			name: "activate with no assignee pools the task",
			task: fixture(hmntsk.StatusCreated),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Activate("system", "", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReady, next.Status)
				assert.Empty(t, next.Assignee)
				assert.Equal(t, hmntsk.EventTypeCreated, events[0].Type)
			},
		},
		{
			name: "activate with a single candidate reserves the task",
			task: fixture(hmntsk.StatusCreated),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Activate("system", testActor, testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, next.Status)
				assert.Equal(t, testActor, next.Assignee)
				assert.Equal(t, testActor, events[0].Assignee)
			},
		},
		{
			name: "claim reserves a pooled task",
			task: fixture(hmntsk.StatusReady),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Claim(testActor, testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, next.Status)
				assert.Equal(t, testActor, next.Assignee)
				assert.Equal(t, hmntsk.EventTypeClaimed, events[0].Type)
			},
		},
		{
			name: "release returns the task to an unchanged pool",
			task: fixture(hmntsk.StatusReserved),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Release(testActor, "handing back", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReady, next.Status)
				assert.Empty(t, next.Assignee)
				assert.Equal(t, []string{testActor, testOther}, next.Candidates.Users)
				assert.Equal(t, "handing back", events[0].Transition.Comment)
			},
		},
		{
			name: "start records when work began",
			task: fixture(hmntsk.StatusReserved),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Start(testActor, testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusInProgress, next.Status)
				require.NotNil(t, next.StartedAt)
				assert.Equal(t, hmntsk.NormalizeTime(testNow), *next.StartedAt)
				assert.Equal(t, hmntsk.EventTypeStarted, events[0].Type)
			},
		},
		{
			name: "delegate keeps saved progress and reserves for the new assignee",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Delegate(testActor, testOther, "over to you", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, next.Status)
				assert.Equal(t, testOther, next.Assignee)
				assertVerbatim(t, `{"note":"halfway"}`, next.Progress,
					"delegation must not lose the work already done")
				assert.NotNil(t, next.StartedAt)
				assert.Equal(t, hmntsk.EventTypeDelegated, events[0].Type)
			},
		},
		{
			name: "cancel closes an in-flight task",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Cancel("owner", "no longer needed", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusExited, next.Status)
				require.NotNil(t, next.ClosedAt)
				assert.Equal(t, hmntsk.EventTypeCancelled, events[0].Type)
				assert.Equal(t, "no longer needed", events[0].Reason)
			},
		},
		{
			name: "obsolete closes a superseded task",
			task: fixture(hmntsk.StatusReady),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Obsolete("system", "superseded", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusObsolete, next.Status)
				assert.Equal(t, hmntsk.EventTypeObsoleted, events[0].Type)
			},
		},
		{
			name: "a fault during creation errors the task",
			task: fixture(hmntsk.StatusCreated),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Fault("no eligible actor", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusError, next.Status)
				assert.Equal(t, "no eligible actor", next.Reason)
				assert.Equal(t, hmntsk.EventTypeErrored, events[0].Type)
				assert.Equal(t, "no eligible actor", events[0].Transition.Comment)
			},
		},
	})
}

func TestTransitionsRefusals(t *testing.T) {
	t.Parallel()

	runTransitionCases(t, []transitionCase{
		{
			name: "completing a ready task is a conflict",
			task: fixture(hmntsk.StatusReady),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Complete(testActor, json.RawMessage(`{"approved":true}`), "", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConflict)
				assert.Equal(t, hmntsk.StatusReady, next.Status, "the task is returned unchanged")
			},
		},
		{
			name: "claiming a reserved task is a conflict, not a silent takeover",
			task: fixture(hmntsk.StatusReserved),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Claim(testOther, testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrIllegalTransition)
				assert.Equal(t, testActor, next.Assignee)
			},
		},
		{
			name: "a non-assignee cannot complete",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Complete(testOther, json.RawMessage(`{"approved":true}`), "", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnauthorized)
				assert.NotErrorIs(t, err, hmntsk.ErrConflict)
			},
		},
		{
			name: "assignee comparison is case-sensitive",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Fail("Alice", "cannot do it", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnauthorized)
			},
		},
		{
			name: "delegation with no delegate named is a validation failure",
			task: fixture(hmntsk.StatusReserved),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Delegate(testActor, "", "", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrValidation)
			},
		},
		{
			name: "releasing a pooled task is a conflict, not a permission problem",
			task: fixture(hmntsk.StatusReady),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Release(testActor, "", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrIllegalTransition)
				assert.NotErrorIs(t, err, hmntsk.ErrUnauthorized)
			},
		},
		{
			name: "an illegal transition outranks a wrong actor",
			task: fixture(hmntsk.StatusReady),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Complete("mallory", json.RawMessage(`{}`), "", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConflict)
				assert.NotErrorIs(t, err, hmntsk.ErrUnauthorized)
			},
		},
		{
			name: "an in-progress task cannot be superseded",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Obsolete("system", "superseded", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrIllegalTransition)
			},
		},
		{
			name: "a suspended task is not escalated",
			task: suspended(hmntsk.StatusReserved),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Escalate("system", &hmntsk.EscalationPolicy{Action: hmntsk.EscalationWiden}, "", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrIllegalTransition)
			},
		},
	})
}

func TestTerminalStatesRejectEveryOperation(t *testing.T) {
	t.Parallel()

	operations := map[string]func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error){
		"Claim": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Claim(testActor, testNow) },
		"Release": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Release(testActor, "", testNow)
		},
		"Start": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Start(testActor, testNow) },
		"Complete": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Complete(testActor, json.RawMessage(`{}`), "", testNow)
		},
		"Fail": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Fail(testActor, "x", testNow) },
		"Delegate": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Delegate(testActor, testOther, "", testNow)
		},
		"Suspend": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Suspend(testActor, "", testNow)
		},
		"Resume": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Resume(testActor, "", testNow)
		},
		"Escalate": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Escalate("system", nil, "", testNow)
		},
		"Cancel": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Cancel("owner", "", testNow) },
		"Obsolete": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Obsolete("system", "", testNow)
		},
		"Activate": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Activate("system", "", testNow)
		},
		"Fault": func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Fault("x", testNow) },
	}

	cases := make([]transitionCase, 0, len(operations)*5)

	for _, status := range hmntsk.Statuses() {
		if !status.IsTerminal() {
			continue
		}

		for name, invoke := range operations {
			task := fixture(status)
			task.Assignee = testActor

			cases = append(cases, transitionCase{
				name:   name + " on " + status.String(),
				task:   task,
				invoke: invoke,
				assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
					require.Error(t, err)
					assert.ErrorIs(t, err, hmntsk.ErrConflict,
						"every operation on a terminal task must fail with a conflict")
					assert.Empty(t, events)
					assert.Equal(t, status, next.Status, "the stored task must be unchanged")
				},
			})
		}
	}

	runTransitionCases(t, cases)
}
