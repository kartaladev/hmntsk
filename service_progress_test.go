package hmntsk_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

func TestServiceSaveProgress(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := t.Context()
	task := h.createApproval(t)

	_, err := h.svc.Claim(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)

	eventsBefore := len(h.store.Events())
	historyBefore := 2 // creation and claim

	first, err := h.svc.SaveProgress(ctx, hmntsk.SaveProgressRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"},
		Patch:       json.RawMessage(`[{"op":"add","path":"/amount","value":100}]`),
	})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusInProgress, first.Task.Status,
		"the first save on a reserved task starts it")
	assertVerbatim(t, `{"amount":100}`, first.Task.Progress)

	history, err := h.svc.History(ctx, task.ID)
	require.NoError(t, err)
	assert.Len(t, history, historyBefore+1, "the implicit start is one transition record")
	assert.Equal(t, hmntsk.OpSaveProgress, history[len(history)-1].Operation)
	assert.Equal(t, hmntsk.StatusReserved, history[len(history)-1].From)
	assert.Equal(t, hmntsk.StatusInProgress, history[len(history)-1].To)

	second, err := h.svc.SaveProgress(ctx, hmntsk.SaveProgressRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"},
		Patch:       json.RawMessage(`[{"op":"add","path":"/justification","value":"new laptop"}]`),
	})
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusInProgress, second.Task.Status)
	assert.JSONEq(t, `{"amount":100,"justification":"new laptop"}`, string(second.Task.Progress))

	history, err = h.svc.History(ctx, task.ID)
	require.NoError(t, err)
	assert.Len(t, history, historyBefore+1,
		"a save that does not transition writes no further history record")

	assert.Len(t, h.store.Events(), eventsBefore, "no save of any kind produces an event")
	assert.NotContains(t, h.recorder.types(), hmntsk.EventTypeStarted,
		"the implicit start is silent, because no consumer is blocked on a draft")
}

func TestServiceSaveProgressValidationAndRefusals(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		actor  string
		patch  string
		assert func(t *testing.T, h *harness, result hmntsk.Result, err error)
	}

	ok := func(expected string) func(t *testing.T, h *harness, result hmntsk.Result, err error) {
		return func(t *testing.T, _ *harness, result hmntsk.Result, err error) {
			require.NoError(t, err)
			assert.JSONEq(t, expected, string(result.Task.Progress))
		}
	}

	refused := func(target error) func(t *testing.T, h *harness, result hmntsk.Result, err error) {
		return func(t *testing.T, h *harness, _ hmntsk.Result, err error) {
			require.ErrorIs(t, err, target)

			stored, getErr := h.svc.Get(t.Context(), h.currentID)
			require.NoError(t, getErr)
			assert.Empty(t, stored.Progress, "nothing is persisted when a save is refused")
		}
	}

	cases := []testCase{
		{
			name:   "a half-filled draft is accepted",
			actor:  "alice",
			patch:  `[{"op":"add","path":"/amount","value":100}]`,
			assert: ok(`{"amount":100}`),
		},
		{
			name:   "an empty patch is accepted and changes nothing",
			actor:  "alice",
			patch:  `[]`,
			assert: ok(`{}`),
		},
		{
			name:   "a nested value can be set",
			actor:  "alice",
			patch:  `[{"op":"add","path":"/requester","value":{}},{"op":"add","path":"/requester/id","value":"u-1"}]`,
			assert: ok(`{"requester":{"id":"u-1"}}`),
		},
		{
			name:   "a wrongly typed field is refused",
			actor:  "alice",
			patch:  `[{"op":"add","path":"/amount","value":"a lot"}]`,
			assert: refused(hmntsk.ErrValidation),
		},
		{
			name:   "a malformed patch is a validation failure, not a fault",
			actor:  "alice",
			patch:  `{"op":"add"}`,
			assert: refused(hmntsk.ErrValidation),
		},
		{
			name:   "a patch that cannot be applied is refused",
			actor:  "alice",
			patch:  `[{"op":"replace","path":"/nothing/here","value":1}]`,
			assert: refused(hmntsk.ErrValidation),
		},
		{
			name:   "a non-assignee cannot save progress",
			actor:  "bob",
			patch:  `[{"op":"add","path":"/amount","value":100}]`,
			assert: refused(hmntsk.ErrUnauthorized),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			ctx := t.Context()
			task := h.createApproval(t)
			h.currentID = task.ID

			_, err := h.svc.Claim(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
			require.NoError(t, err)

			result, err := h.svc.SaveProgress(ctx, hmntsk.SaveProgressRequest{
				TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: tc.actor},
				Patch:       json.RawMessage(tc.patch),
			})
			tc.assert(t, h, result, err)
		})
	}
}

func TestServiceSaveProgressOnAPooledTaskIsAConflict(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	task := h.createApproval(t)

	_, err := h.svc.SaveProgress(t.Context(), hmntsk.SaveProgressRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"},
		Patch:       json.RawMessage(`[{"op":"add","path":"/amount","value":1}]`),
	})
	require.ErrorIs(t, err, hmntsk.ErrConflict)
}

func TestServiceProgressSurvivesDelegation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := t.Context()
	task := h.createApproval(t)

	_, err := h.svc.Claim(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)

	_, err = h.svc.SaveProgress(ctx, hmntsk.SaveProgressRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"},
		Patch:       json.RawMessage(`[{"op":"add","path":"/amount","value":100}]`),
	})
	require.NoError(t, err)

	delegated, err := h.svc.Delegate(ctx, hmntsk.DelegateRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"},
		Target:      "bob",
	})
	require.NoError(t, err)

	assert.Equal(t, hmntsk.StatusReserved, delegated.Task.Status)
	assert.JSONEq(t, `{"amount":100}`, string(delegated.Task.Progress))
}
