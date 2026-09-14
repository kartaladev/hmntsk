package tasknotify_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
	"github.com/kartaladev/hmntsk/tasknotify"
)

// TestWithRulesExtendsTheDefaults shows a host deriving its rules from the
// defaults: the creator is told when their task completes, and the default close
// still runs.
func TestWithRulesExtendsTheDefaults(t *testing.T) {
	t.Parallel()

	const kindDone = "done"

	creatorOnCompletion := tasknotify.RulesFunc(func(ctx context.Context, in tasknotify.Input) (tasknotify.Plan, error) {
		plan, err := tasknotify.DefaultRules.Plan(ctx, in)
		if err != nil {
			return tasknotify.Plan{}, err
		}

		if in.Event.Type != hmntsk.EventTypeCompleted || in.Event.CreatedBy == "" {
			return plan, nil
		}

		draft, err := in.Draft(ctx, in.Event.CreatedBy, kindDone)
		if err != nil {
			return tasknotify.Plan{}, err
		}

		// The close runs first and moves every kind's watermark to this version;
		// a draft at the same version is still created.
		plan.Steps = append(plan.Steps, tasknotify.Step{Publish: []notify.Draft{draft}})

		return plan, nil
	})

	notifier := newNotifier(t)

	projector, err := tasknotify.New(newEngine(t), notifier, tasknotify.WithRules(creatorOnCompletion))
	require.NoError(t, err)

	deliver := func(event hmntsk.Event) {
		t.Helper()

		outcome := projector.Deliver(t.Context(), relay.Attempt{Event: event, DeliveryID: event.ID, Number: 1})
		require.Equal(t, relay.OutcomeDelivered, outcome.Status, outcome.Err)
	}

	deliver(hmntsk.Event{
		ID: "event-1", Type: hmntsk.EventTypeCreated, TaskID: "task-1", TaskType: "approval",
		Status: hmntsk.StatusReserved, Version: 1, Actor: "owner", Assignee: "dave", CreatedBy: "owner",
	})

	deliver(hmntsk.Event{
		ID: "event-2", Type: hmntsk.EventTypeCompleted, TaskID: "task-1", TaskType: "approval",
		Status: hmntsk.StatusCompleted, Version: 3, Actor: "dave", Assignee: "dave", CreatedBy: "owner",
	})

	assigned, err := notifier.List(t.Context(), notify.ListQuery{Recipient: "dave"})
	require.NoError(t, err)
	require.Len(t, assigned.Notifications, 1)
	assert.Equal(t, notify.StateClosed, assigned.Notifications[0].State, "the default close still runs")
	assert.Equal(t, "completed", assigned.Notifications[0].ClosedReason)

	creator, err := notifier.List(t.Context(), notify.ListQuery{Recipient: "owner"})
	require.NoError(t, err)
	require.Len(t, creator.Notifications, 1)
	assert.Equal(t, kindDone, creator.Notifications[0].Kind)
	assert.Equal(t, notify.StateActive, creator.Notifications[0].State)
}
