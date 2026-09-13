package hmntsk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// TestEventCatalogueCoversEveryTransition pins the catalogue closed: every
// operation that moves a task has exactly one event type, every event type
// belongs to exactly one operation, and the only operation without one is the
// progress save, which is deliberately silent.
func TestEventCatalogueCoversEveryTransition(t *testing.T) {
	t.Parallel()

	operations := []hmntsk.Operation{
		hmntsk.OpCreate, hmntsk.OpClaim, hmntsk.OpRelease, hmntsk.OpStart,
		hmntsk.OpSaveProgress, hmntsk.OpComplete, hmntsk.OpFail, hmntsk.OpDelegate,
		hmntsk.OpSuspend, hmntsk.OpResume, hmntsk.OpEscalate, hmntsk.OpCancel,
		hmntsk.OpObsolete, hmntsk.OpFault,
	}

	seen := make(map[hmntsk.EventType]hmntsk.Operation, len(operations))

	for _, op := range operations {
		eventType, ok := hmntsk.EventTypeForOperation(op)

		if op == hmntsk.OpSaveProgress {
			assert.False(t, ok, "a progress save must produce no event")

			continue
		}

		require.Truef(t, ok, "operation %s has no event type", op)
		assert.Truef(t, eventType.Valid(), "operation %s maps to %q, which is outside the catalogue", op, eventType)

		previous, duplicate := seen[eventType]
		assert.Falsef(t, duplicate, "%s and %s both map to %s", previous, op, eventType)

		seen[eventType] = op
	}

	assert.Len(t, seen, len(hmntsk.EventTypes()),
		"every published event type must be reachable from exactly one operation")

	for _, eventType := range hmntsk.EventTypes() {
		_, reachable := seen[eventType]
		assert.Truef(t, reachable, "event type %s is published but no operation produces it", eventType)
	}
}

func TestEventTypeValid(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name      string
		eventType hmntsk.EventType
		assert    func(t *testing.T, valid bool)
	}

	cases := []testCase{
		{
			name:      "a catalogue member is valid",
			eventType: hmntsk.EventTypeCompleted,
			assert:    func(t *testing.T, valid bool) { assert.True(t, valid) },
		},
		{
			name:      "an invented type is not",
			eventType: hmntsk.EventType("task.approved"),
			assert:    func(t *testing.T, valid bool) { assert.False(t, valid) },
		},
		{
			name:      "the empty type is not",
			eventType: hmntsk.EventType(""),
			assert:    func(t *testing.T, valid bool) { assert.False(t, valid) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.eventType.Valid())
		})
	}
}

// TestEventAudienceDoesNotAliasTheTask proves an event's audience snapshot is a
// copy: changing the task afterwards never changes what the event says.
func TestEventAudienceDoesNotAliasTheTask(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		mutate func(task *hmntsk.Task)
		assert func(t *testing.T, event hmntsk.Event)
	}

	cases := []testCase{
		{
			name:   "changing the task's users leaves the snapshot alone",
			mutate: func(task *hmntsk.Task) { task.Candidates.Users[0] = "changed" },
			assert: func(t *testing.T, event hmntsk.Event) {
				assert.Equal(t, []string{testActor, testOther}, event.Candidates.Users)
			},
		},
		{
			name:   "changing the task's groups leaves the snapshot alone",
			mutate: func(task *hmntsk.Task) { task.Candidates.Groups[0] = "changed" },
			assert: func(t *testing.T, event hmntsk.Event) {
				assert.Equal(t, []string{"managers"}, event.Candidates.Groups)
			},
		},
		{
			name:   "changing the task's exclusions leaves the snapshot alone",
			mutate: func(task *hmntsk.Task) { task.Candidates.Excluded[0] = "changed" },
			assert: func(t *testing.T, event hmntsk.Event) {
				assert.Equal(t, []string{testExcluded}, event.Candidates.Excluded)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			task := fixture(hmntsk.StatusReady)
			task.Candidates.Groups = []string{"managers"}

			next, events, err := task.Claim(testActor, testNow)
			require.NoError(t, err)
			require.Len(t, events, 1)

			tc.mutate(&next)
			tc.assert(t, events[0])
		})
	}
}

// TestEveryEventCarriesCorrelation walks every lifecycle operation and asserts
// the event it produces carries the task's correlation data and names no
// caller-specific type.
func TestEveryEventCarriesCorrelation(t *testing.T) {
	t.Parallel()

	correlation := hmntsk.CorrelationData{
		OwnerType: "process", OwnerRef: "p-1", ActivityKey: "approve",
		Extra: map[string]string{"tenant": "acme"},
	}

	produce := map[hmntsk.Operation]func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error){
		hmntsk.OpCreate: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			task.Status = hmntsk.StatusCreated

			return task.Activate("system", "", testNow)
		},
		hmntsk.OpClaim: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			task.Status = hmntsk.StatusReady

			return task.Claim(testActor, testNow)
		},
		hmntsk.OpRelease: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Release(testActor, "", testNow)
		},
		hmntsk.OpStart: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Start(testActor, testNow)
		},
		hmntsk.OpComplete: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			task.Status = hmntsk.StatusInProgress

			return task.Complete(testActor, nil, "", testNow)
		},
		hmntsk.OpFail: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			task.Status = hmntsk.StatusInProgress

			return task.Fail(testActor, "cannot", testNow)
		},
		hmntsk.OpDelegate: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Delegate(testActor, testOther, "", testNow)
		},
		hmntsk.OpSuspend: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Suspend(testActor, "", testNow)
		},
		hmntsk.OpResume: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			task.Status = hmntsk.StatusSuspended
			task.SuspendedFrom = hmntsk.StatusReserved

			return task.Resume(testActor, "", testNow)
		},
		hmntsk.OpEscalate: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Escalate("system", nil, "", testNow)
		},
		hmntsk.OpCancel: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Cancel("owner", "", testNow)
		},
		hmntsk.OpObsolete: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			return task.Obsolete("system", "", testNow)
		},
		hmntsk.OpFault: func(task hmntsk.Task) (hmntsk.Task, []hmntsk.Event, error) {
			task.Status = hmntsk.StatusInProgress

			return task.Fault("broken", testNow)
		},
	}

	for op, invoke := range produce {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()

			task := fixture(hmntsk.StatusReserved)
			task.Correlation = correlation

			_, events, err := invoke(task)
			require.NoError(t, err)
			require.Len(t, events, 1)

			event := events[0]
			assert.Equal(t, correlation, event.Correlation,
				"a consumer must be able to route on correlation alone")
			assert.Equal(t, task.ID, event.TaskID)
			assert.Equal(t, task.Type, event.TaskType)
			assert.NotZero(t, event.OccurredAt)
			require.NotNil(t, event.Callback)
			assert.Equal(t, "https://host.example/hook", event.Callback.Address)
		})
	}
}
