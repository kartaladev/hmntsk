package hmntsk_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// TestStatusCarriesNoBusinessOutcome pins design decision D16: status says
// where the task got to, the payload says what happened. A denied approval is
// a COMPLETED task, and FAILED and ERROR are reachable only by their own paths.
func TestStatusCarriesNoBusinessOutcome(t *testing.T) {
	t.Parallel()

	runTransitionCases(t, []transitionCase{
		{
			name: "a denied approval completes",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Complete(testActor, json.RawMessage(`{"approved":false,"reason":"over budget"}`), "", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusCompleted, next.Status,
					"a negative business outcome is still a completion")
				assert.NotEqual(t, hmntsk.StatusFailed, next.Status)
				assertVerbatim(t, `{"approved":false,"reason":"over budget"}`, next.Output)
				assert.Equal(t, hmntsk.EventTypeCompleted, events[0].Type)
				assertVerbatim(t, `{"approved":false,"reason":"over budget"}`, events[0].Output,
					"the denial is readable only from the output payload")
			},
		},
		{
			name: "a granted approval completes identically",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Complete(testActor, json.RawMessage(`{"approved":true}`), "", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusCompleted, next.Status)
				assert.Equal(t, hmntsk.EventTypeCompleted, events[0].Type)
			},
		},
		{
			name: "FAILED comes only from the actor saying they cannot do the work",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Fail(testActor, "I do not have the authority", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusFailed, next.Status)
				assert.Empty(t, next.Output, "failure requires no output payload")
				assert.Equal(t, "I do not have the authority", next.Reason)
				assert.Equal(t, "I do not have the authority", events[0].Transition.Comment)
				assert.Equal(t, hmntsk.EventTypeFailed, events[0].Type)
			},
		},
		{
			name: "ERROR comes only from a system fault",
			task: fixture(hmntsk.StatusCreated),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Fault("candidate resolution failed", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusError, next.Status)
				assert.Empty(t, events[0].Actor, "a fault has no acting actor")
				assert.Equal(t, hmntsk.EventTypeErrored, events[0].Type)
			},
		},
	})
}

// TestNoOperationReachesFailedOrErrorOffItsOwnPath walks every operation from
// every state and asserts that FAILED is produced only by Fail and ERROR only
// by Fault.
func TestNoOperationReachesFailedOrErrorOffItsOwnPath(t *testing.T) {
	t.Parallel()

	operations := map[hmntsk.Operation]func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error){
		hmntsk.OpCreate: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Activate("system", "", testNow)
		},
		hmntsk.OpClaim: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Claim(testOther, testNow) },
		hmntsk.OpRelease: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Release(taskActor(task), "", testNow)
		},
		hmntsk.OpStart: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Start(taskActor(task), testNow)
		},
		hmntsk.OpComplete: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Complete(taskActor(task), json.RawMessage(`{}`), "", testNow)
		},
		hmntsk.OpFail: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Fail(taskActor(task), "x", testNow)
		},
		hmntsk.OpDelegate: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Delegate(taskActor(task), testOther, "", testNow)
		},
		hmntsk.OpSuspend: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Suspend(taskActor(task), "", testNow)
		},
		hmntsk.OpResume: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Resume(taskActor(task), "", testNow)
		},
		hmntsk.OpEscalate: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Escalate("system", nil, "", testNow)
		},
		hmntsk.OpCancel: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Cancel("owner", "", testNow) },
		hmntsk.OpObsolete: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Obsolete("system", "", testNow)
		},
		hmntsk.OpFault: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) { return task.Fault("x", testNow) },
	}

	for _, from := range hmntsk.Statuses() {
		for op, invoke := range operations {
			t.Run(op.String()+" from "+from.String(), func(t *testing.T) {
				t.Parallel()

				task := fixture(from)
				if from == hmntsk.StatusSuspended {
					task = suspended(hmntsk.StatusInProgress)
				}

				next, _, err := invoke(task)
				if err != nil {
					return
				}

				if next.Status == hmntsk.StatusFailed {
					assert.Equal(t, hmntsk.OpFail, op, "only Fail may produce FAILED")
				}

				if next.Status == hmntsk.StatusError {
					assert.Equal(t, hmntsk.OpFault, op, "only Fault may produce ERROR")
				}
			})
		}
	}
}

func TestEscalateWidensWithoutMovingTheTask(t *testing.T) {
	t.Parallel()

	runTransitionCases(t, []transitionCase{
		{
			name: "widening keeps existing candidates eligible",
			task: fixture(hmntsk.StatusReady),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Escalate("system", &hmntsk.EscalationPolicy{
					Action:    hmntsk.EscalationWiden,
					AddGroups: []string{"managers"},
					AddUsers:  []string{"carol"},
				}, "past due", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReady, next.Status, "widening does not move the task")
				assert.Equal(t, []string{testActor, testOther, "carol"}, next.Candidates.Users,
					"previously eligible actors must remain eligible")
				assert.Equal(t, []string{"managers"}, next.Candidates.Groups)
				assert.Equal(t, 1, next.EscalationCount)
				require.NotNil(t, next.EscalatedAt)
				assert.Equal(t, hmntsk.EventTypeEscalated, events[0].Type)
			},
		},
		{
			name: "widening is idempotent on a group already present",
			task: func() hmntsk.Task {
				task := fixture(hmntsk.StatusReady)
				task.Candidates.Groups = []string{"managers"}

				return task
			}(),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Escalate("system", &hmntsk.EscalationPolicy{
					Action:    hmntsk.EscalationWiden,
					AddGroups: []string{"managers"},
				}, "", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"managers"}, next.Candidates.Groups)
			},
		},
		{
			name: "a nil policy only announces",
			task: fixture(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Escalate("system", nil, "", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusInProgress, next.Status)
				assert.Equal(t, hmntsk.EventTypeEscalated, events[0].Type)
			},
		},
		{
			name: "a superseding policy closes the task as obsolete",
			task: fixture(hmntsk.StatusReady),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Escalate("system", &hmntsk.EscalationPolicy{
					Action: hmntsk.EscalationSupersede,
				}, "replaced", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusObsolete, next.Status)
				assert.Equal(t, hmntsk.EventTypeObsoleted, events[0].Type,
					"supersession announces obsolescence, not escalation")
			},
		},
		{
			name: "escalation keeps the sweeper's lease as its back-off",
			task: func() hmntsk.Task {
				task := fixture(hmntsk.StatusReady)
				until := testNow.Add(time.Minute)
				task.LockedBy = "sweeper-1"
				task.LockedUntil = &until

				return task
			}(),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Escalate("system", &hmntsk.EscalationPolicy{Action: hmntsk.EscalationWiden}, "", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, "sweeper-1", next.LockedBy,
					"widening does not move the deadline, so without the lease the next sweep "+
						"would escalate the same task again a moment later")
				require.NotNil(t, next.LockedUntil)
				require.NotNil(t, next.EscalatedAt)
			},
		},
	})
}
