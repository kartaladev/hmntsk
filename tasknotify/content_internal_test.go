package tasknotify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
)

// newTestProjector builds a projector over in-memory stores.
func newTestProjector(t *testing.T, opts ...Option) *Projector {
	t.Helper()

	engine, err := hmntsk.New(memstore.New())
	require.NoError(t, err)

	notifier, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	projector, err := New(engine, notifier, opts...)
	require.NoError(t, err)

	return projector
}

// claimEvent is a claim carrying everything an event can carry, so that a test
// can assert what the default content leaves out.
func claimEvent() hmntsk.Event {
	return hmntsk.Event{
		ID: "event-1", Type: hmntsk.EventTypeClaimed, TaskID: "task-1", TaskType: "invoice-approval",
		Status: hmntsk.StatusReserved, Version: 3, Actor: "carol", Assignee: "carol",
		Correlation: hmntsk.CorrelationData{
			OwnerType: "invoice", OwnerRef: "INV-42", ActivityKey: "approve",
			Extra: map[string]string{"secret": "do-not-copy"},
		},
		Candidates: hmntsk.CandidatePool{Users: []string{"alice", "bob", "carol"}},
		Output:     json.RawMessage(`{"approved":true}`),
	}
}

func TestTitleFor(t *testing.T) {
	t.Parallel()

	errTitles := errors.New("no title today")

	type testCase struct {
		name   string
		opts   []Option
		kind   string
		assert func(t *testing.T, title string, err error)
	}

	cases := []testCase{
		{
			name: "an offer",
			kind: KindOffer,
			assert: func(t *testing.T, title string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Task available: invoice-approval", title)
			},
		},
		{
			name: "a taken notice names who claimed it",
			kind: KindTaken,
			assert: func(t *testing.T, title string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Task taken by carol: invoice-approval", title)
			},
		},
		{
			name: "an assignment",
			kind: KindAssigned,
			assert: func(t *testing.T, title string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Task assigned to you: invoice-approval", title)
			},
		},
		{
			name: "a kind of the host's own has no default title",
			kind: "reminder",
			assert: func(t *testing.T, title string, err error) {
				require.NoError(t, err)
				assert.Empty(t, title)
			},
		},
		{
			name: "WithTitles replaces the default",
			opts: []Option{WithTitles(func(_ context.Context, in DraftInput) (string, error) {
				return in.Kind + " for " + in.Recipient, nil
			})},
			kind: KindOffer,
			assert: func(t *testing.T, title string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "offer for alice", title)
			},
		},
		{
			name: "a failing title func is returned as is",
			opts: []Option{WithTitles(func(context.Context, DraftInput) (string, error) { return "", errTitles })},
			kind: KindOffer,
			assert: func(t *testing.T, _ string, err error) {
				require.ErrorIs(t, err, errTitles)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			projector := newTestProjector(t, tc.opts...)

			title, err := projector.titleFor(t.Context(), DraftInput{Event: claimEvent(), Kind: tc.kind, Recipient: "alice"})
			tc.assert(t, title, err)
		})
	}
}

func TestDataFor(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		opts   []Option
		event  func() hmntsk.Event
		assert func(t *testing.T, data json.RawMessage, err error)
	}

	cases := []testCase{
		{
			name:  "the default payload describes the event and copies no consumer data",
			event: claimEvent,
			assert: func(t *testing.T, data json.RawMessage, err error) {
				require.NoError(t, err)
				assert.JSONEq(t, `{
					"taskId": "task-1", "taskType": "invoice-approval", "eventType": "task.claimed",
					"status": "RESERVED", "actor": "carol",
					"ownerType": "invoice", "ownerRef": "INV-42", "activityKey": "approve"
				}`, string(data))
				assert.NotContains(t, string(data), "do-not-copy", "correlation extra is never copied")
				assert.NotContains(t, string(data), "approved", "the output is never copied")
				assert.NotContains(t, string(data), "bob", "the candidate pool is never copied")
			},
		},
		{
			name: "a previous holder and no correlation",
			event: func() hmntsk.Event {
				return hmntsk.Event{
					ID: "event-2", Type: hmntsk.EventTypeDelegated, TaskID: "task-1", TaskType: "t",
					Status: hmntsk.StatusReserved, Actor: "dave", Assignee: "erin", PreviousAssignee: "dave",
				}
			},
			assert: func(t *testing.T, data json.RawMessage, err error) {
				require.NoError(t, err)
				assert.JSONEq(t, `{
					"taskId": "task-1", "taskType": "t", "eventType": "task.delegated",
					"status": "RESERVED", "actor": "dave", "previousAssignee": "dave"
				}`, string(data))
			},
		},
		{
			name: "WithData replaces the default",
			opts: []Option{WithData(func(_ context.Context, in DraftInput) (json.RawMessage, error) {
				return json.RawMessage(`{"kind":"` + in.Kind + `"}`), nil
			})},
			event: claimEvent,
			assert: func(t *testing.T, data json.RawMessage, err error) {
				require.NoError(t, err)
				assert.JSONEq(t, `{"kind":"taken"}`, string(data))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			projector := newTestProjector(t, tc.opts...)

			event := tc.event()

			data, err := projector.dataFor(t.Context(), DraftInput{Event: event, Kind: KindTaken, Recipient: "alice"},
				projector.contentFor(event))
			tc.assert(t, data, err)
		})
	}
}
