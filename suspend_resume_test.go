package hmntsk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

func TestSuspendRecordsTheStateToReturnTo(t *testing.T) {
	t.Parallel()

	cases := make([]transitionCase, 0, 3)

	for _, from := range hmntsk.Statuses() {
		if !from.IsSuspendable() {
			continue
		}

		cases = append(cases, transitionCase{
			name: "suspending from " + from.String(),
			task: fixture(from),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Suspend(taskActor(task), "paused", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusSuspended, next.Status)
				assert.Equal(t, from, next.SuspendedFrom,
					"the resume target is not derivable, so it must be recorded")
				assert.Equal(t, hmntsk.EventTypeSuspended, events[0].Type)
			},
		})
	}

	require.Len(t, cases, 3, "READY, RESERVED and IN_PROGRESS are the suspendable states")

	runTransitionCases(t, cases)
}

func TestResumeRestoresTheExactPriorState(t *testing.T) {
	t.Parallel()

	cases := make([]transitionCase, 0, 3)

	for _, from := range hmntsk.Statuses() {
		if !from.IsSuspendable() {
			continue
		}

		cases = append(cases, transitionCase{
			name: "resuming to " + from.String(),
			task: suspended(from),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Resume(taskActor(task), "back on it", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, from, next.Status)
				assert.Empty(t, next.SuspendedFrom, "the resume target is consumed on resume")
				assert.Equal(t, hmntsk.EventTypeResumed, events[0].Type)

				original := fixture(from)
				assert.Equal(t, original.Assignee, next.Assignee, "the assignee must survive suspension")
				assert.Equal(t, string(original.Progress), string(next.Progress),
					"saved progress must survive suspension")
			},
		})
	}

	require.Len(t, cases, 3)

	runTransitionCases(t, cases)
}

func TestResumeWithoutARecordedPriorStateIsRefused(t *testing.T) {
	t.Parallel()

	runTransitionCases(t, []transitionCase{
		{
			name: "a suspended task with no recorded prior state cannot resume",
			task: func() hmntsk.Task {
				task := fixture(hmntsk.StatusSuspended)
				task.SuspendedFrom = ""

				return task
			}(),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Resume(testActor, "", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrIllegalTransition)
			},
		},
		{
			name: "a suspended task cannot resume into a terminal state",
			task: func() hmntsk.Task {
				task := fixture(hmntsk.StatusSuspended)
				task.SuspendedFrom = hmntsk.StatusCompleted

				return task
			}(),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Resume(testActor, "", testNow)
			},
			assert: func(t *testing.T, _ hmntsk.Task, _ []hmntsk.Event, err error) {
				require.ErrorIs(t, err, hmntsk.ErrIllegalTransition)
			},
		},
		{
			name: "a suspended task may still be cancelled",
			task: suspended(hmntsk.StatusInProgress),
			invoke: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
				return task.Cancel("owner", "abandoned", testNow)
			},
			assert: func(t *testing.T, next hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusExited, next.Status)
			},
		},
	})
}

// taskActor returns the actor entitled to operate on the task: its assignee
// when it has one, and the owner otherwise.
func taskActor(task hmntsk.Task) string {
	if task.Assignee != "" {
		return task.Assignee
	}

	return "owner"
}
