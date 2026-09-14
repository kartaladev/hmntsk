package tasknotify_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
	"github.com/kartaladev/hmntsk/tasknotify"
)

// Compile-time proof that a projector is a relay sink.
var _ relay.Sink = (*tasknotify.Projector)(nil)

// newEngine builds an engine over the in-memory store.
func newEngine(t *testing.T, opts ...hmntsk.Option) *hmntsk.Service {
	t.Helper()

	engine, err := hmntsk.New(memstore.New(), opts...)
	require.NoError(t, err)

	return engine
}

// newNotifier builds a notification service over the in-memory store.
func newNotifier(t *testing.T) *notify.Service {
	t.Helper()

	notifier, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	return notifier
}

func TestNew(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		// engine and notifier, when set, replace the defaults; nilEngine and
		// nilNotifier wire none.
		nilEngine   bool
		nilNotifier bool
		opts        []tasknotify.Option
		assert      func(t *testing.T, projector *tasknotify.Projector, err error)
	}

	refused := func(t *testing.T, projector *tasknotify.Projector, err error) {
		t.Helper()

		require.ErrorIs(t, err, hmntsk.ErrConfiguration)
		assert.Nil(t, projector)
	}

	cases := []testCase{
		{
			name: "no options builds a sink named tasknotify",
			assert: func(t *testing.T, projector *tasknotify.Projector, err error) {
				require.NoError(t, err)
				assert.Equal(t, tasknotify.DefaultSinkName, projector.Name())
				assert.Equal(t, "tasknotify", projector.Name())
			},
		},
		{
			name: "a sink name replaces the default",
			opts: []tasknotify.Option{tasknotify.WithSinkName("notifications")},
			assert: func(t *testing.T, projector *tasknotify.Projector, err error) {
				require.NoError(t, err)
				assert.Equal(t, "notifications", projector.Name())
			},
		},
		{
			name: "a narrowed closing set keeping completed and cancelled is accepted",
			opts: []tasknotify.Option{tasknotify.WithClosingStatuses(
				hmntsk.StatusCompleted, hmntsk.StatusExited, hmntsk.StatusExited,
			)},
			assert: func(t *testing.T, _ *tasknotify.Projector, err error) {
				require.NoError(t, err, "a duplicate status is ignored")
			},
		},
		{name: "no engine", nilEngine: true, assert: refused},
		{name: "no notifier", nilNotifier: true, assert: refused},
		{
			name:   "a closing set without completed",
			opts:   []tasknotify.Option{tasknotify.WithClosingStatuses(hmntsk.StatusExited, hmntsk.StatusFailed)},
			assert: refused,
		},
		{
			name:   "a closing set without cancelled",
			opts:   []tasknotify.Option{tasknotify.WithClosingStatuses(hmntsk.StatusCompleted, hmntsk.StatusFailed)},
			assert: refused,
		},
		{
			name: "a closing set with a status that is not final",
			opts: []tasknotify.Option{tasknotify.WithClosingStatuses(
				hmntsk.StatusCompleted, hmntsk.StatusExited, hmntsk.StatusReady,
			)},
			assert: refused,
		},
		{
			name:   "an empty closing set",
			opts:   []tasknotify.Option{tasknotify.WithClosingStatuses()},
			assert: refused,
		},
		{name: "a zero publish batch", opts: []tasknotify.Option{tasknotify.WithPublishBatch(0)}, assert: refused},
		{name: "a negative publish batch", opts: []tasknotify.Option{tasknotify.WithPublishBatch(-1)}, assert: refused},
		{name: "an empty sink name", opts: []tasknotify.Option{tasknotify.WithSinkName("")}, assert: refused},
		{
			name:   "an empty task link template",
			opts:   []tasknotify.Option{tasknotify.WithTaskLinkTemplate("")},
			assert: refused,
		},
		{name: "no rules", opts: []tasknotify.Option{tasknotify.WithRules(nil)}, assert: refused},
		{name: "no title func", opts: []tasknotify.Option{tasknotify.WithTitles(nil)}, assert: refused},
		{name: "no data func", opts: []tasknotify.Option{tasknotify.WithData(nil)}, assert: refused},
		{name: "no links func", opts: []tasknotify.Option{tasknotify.WithLinks(nil)}, assert: refused},
		{name: "no error handler", opts: []tasknotify.Option{tasknotify.WithErrorHandler(nil)}, assert: refused},
		{
			name: "replacement funcs are accepted",
			opts: []tasknotify.Option{
				tasknotify.WithTitles(func(context.Context, tasknotify.DraftInput) (string, error) { return "t", nil }),
				tasknotify.WithData(func(context.Context, tasknotify.DraftInput) (json.RawMessage, error) {
					return json.RawMessage(`{}`), nil
				}),
				tasknotify.WithLinks(func(context.Context, tasknotify.DraftInput) (map[string]string, error) {
					return nil, nil
				}),
				tasknotify.WithRules(tasknotify.DefaultRules),
				tasknotify.WithErrorHandler(func(context.Context, error) {}),
			},
			assert: func(t *testing.T, _ *tasknotify.Projector, err error) {
				require.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				engine   *hmntsk.Service
				notifier *notify.Service
			)

			if !tc.nilEngine {
				engine = newEngine(t)
			}

			if !tc.nilNotifier {
				notifier = newNotifier(t)
			}

			projector, err := tasknotify.New(engine, notifier, tc.opts...)
			tc.assert(t, projector, err)
		})
	}
}
