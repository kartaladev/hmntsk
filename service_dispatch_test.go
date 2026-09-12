package hmntsk_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// cancelAfterCommit is a store that cancels the caller's context the instant a
// transaction it began has committed. It reproduces the case the engine has to
// survive: a client hanging up in the gap between the commit and the
// notification.
type cancelAfterCommit struct {
	*memstore.Store

	cancel context.CancelFunc
}

func (s cancelAfterCommit) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	err := s.Store.Do(ctx, fn)

	if s.cancel != nil {
		s.cancel()
	}

	return err
}

type contextProbeKey struct{}

func TestServiceDispatchSurvivesARequestThatHasEnded(t *testing.T) {
	t.Parallel()

	registry := hmntsk.NewRegistry()
	require.NoError(t, registry.Register(hmntsk.TypeSpec{Name: "freeform"}))

	inner := memstore.New()

	ctx, cancel := context.WithCancel(t.Context())
	ctx = context.WithValue(ctx, contextProbeKey{}, "trace-42")

	type delivery struct {
		event      hmntsk.Event
		contextErr error
		traceValue any
	}

	delivered := make([]delivery, 0, 1)

	handler := hmntsk.EventHandlerFunc(func(ctx context.Context, event hmntsk.Event) error {
		delivered = append(delivered, delivery{
			event:      event,
			contextErr: ctx.Err(),
			traceValue: ctx.Value(contextProbeKey{}),
		})

		return nil
	})

	svc, err := hmntsk.New(
		cancelAfterCommit{Store: inner, cancel: cancel},
		hmntsk.WithRegistry(registry),
		hmntsk.WithGroupResolver(testDirectory()),
		hmntsk.WithClock(hmntsk.ClockFunc(func() time.Time { return testNow })),
		hmntsk.WithEventHandlers(handler),
	)
	require.NoError(t, err)

	_, err = svc.Create(ctx, hmntsk.CreateRequest{
		Type:       "freeform",
		Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
	})
	require.NoError(t, err)

	require.Error(t, ctx.Err(), "the request context really was cancelled before dispatch")
	require.Len(t, delivered, 1, "the notification must still be delivered")
	assert.Equal(t, hmntsk.EventTypeCreated, delivered[0].event.Type)
	assert.NoError(t, delivered[0].contextErr,
		"the handler's context must be detached from the request's cancellation")
	assert.Equal(t, "trace-42", delivered[0].traceValue,
		"but it must keep the request's values, so logging and tracing still work")
}

func TestServiceHostLedTransactionWithholdsDispatch(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	task := h.createApproval(t)

	before := len(h.recorder.delivered())

	scoped, done := h.store.ContextWithTx(t.Context())

	result, err := h.svc.Claim(scoped, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)
	assert.True(t, result.Pending(),
		"the engine cannot observe a commit it did not perform, so it hands the dispatch back")
	assert.Len(t, h.recorder.delivered(), before,
		"nothing may be delivered while the host's transaction is still open")

	done(true)

	assert.Len(t, h.recorder.delivered(), before,
		"committing alone does not dispatch; the host must ask")

	require.NoError(t, result.Dispatch(t.Context()))

	types := h.recorder.types()
	require.Len(t, types, before+1)
	assert.Equal(t, hmntsk.EventTypeClaimed, types[len(types)-1])

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReserved, stored.Status)
}

func TestServiceHostLedRollbackDeliversNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	task := h.createApproval(t)

	before := len(h.recorder.delivered())

	scoped, done := h.store.ContextWithTx(t.Context())

	result, err := h.svc.Claim(scoped, hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"})
	require.NoError(t, err)

	done(false)

	assert.Len(t, h.recorder.delivered(), before)

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReady, stored.Status,
		"a rolled-back claim must not appear to have happened")
	assert.Empty(t, stored.Assignee)

	// The host discarded its transaction, so it must not go on to dispatch.
	assert.True(t, result.Pending())
}

func TestServiceEngineLedRollbackLeavesNoTraceAtAll(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	task := h.createApproval(t)

	before := len(h.store.Events())

	_, err := h.svc.Complete(t.Context(), hmntsk.CompleteRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: task.ID, Actor: "alice"},
		Output:      json.RawMessage(`{"approved":true}`),
	})
	require.ErrorIs(t, err, hmntsk.ErrConflict, "a READY task cannot be completed")

	stored, err := h.svc.Get(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusReady, stored.Status)
	assert.Len(t, h.store.Events(), before, "a refused operation records no event")

	history, err := h.svc.History(t.Context(), task.ID)
	require.NoError(t, err)
	assert.Len(t, history, 1, "only the creation is in history")
}
