package demo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/notify"
)

// listenFunc is a broadcaster whose Listen the test supplies. Nothing here
// broadcasts.
type listenFunc func(ctx context.Context, deliver func(notify.Signal), ready func()) error

func (listenFunc) Broadcast(context.Context, []notify.Signal) error { return nil }

func (f listenFunc) Listen(ctx context.Context, deliver func(notify.Signal), ready func()) error {
	return f(ctx, deliver, ready)
}

func TestRunHub(t *testing.T) {
	t.Parallel()

	errSubscribe := errors.New("subscription refused")

	// neverReady subscribes to nothing and waits for the end of the run.
	neverReady := listenFunc(func(ctx context.Context, _ func(notify.Signal), _ func()) error {
		<-ctx.Done()

		return ctx.Err()
	})

	type testCase struct {
		name        string
		broadcaster notify.Broadcaster
		ctx         func(ctx context.Context) context.Context
		assert      func(t *testing.T, hub *notify.Hub, err error)
	}

	cases := []testCase{
		{
			name:        "returns once the hub's broadcaster has subscribed",
			broadcaster: notify.NewInProcessBroadcaster(),
			assert: func(t *testing.T, hub *notify.Hub, err error) {
				require.NoError(t, err)
				assert.True(t, hub.Running())
			},
		},
		{
			name: "returns the broadcaster's error when it cannot subscribe",
			broadcaster: listenFunc(func(context.Context, func(notify.Signal), func()) error {
				return errSubscribe
			}),
			assert: func(t *testing.T, hub *notify.Hub, err error) {
				require.ErrorIs(t, err, errSubscribe)
				assert.False(t, hub.Running())
			},
		},
		{
			name:        "gives up when the broadcaster never confirms",
			broadcaster: neverReady,
			assert: func(t *testing.T, hub *notify.Hub, err error) {
				require.Error(t, err)
				assert.False(t, hub.Running())
			},
		},
		{
			name:        "a cancelled context ends the run before it is ready",
			broadcaster: neverReady,
			ctx: func(ctx context.Context) context.Context {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				return cancelled
			},
			assert: func(t *testing.T, hub *notify.Hub, err error) {
				require.ErrorIs(t, err, context.Canceled)
				assert.False(t, hub.Running())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}

			hub, err := notify.NewHub(tc.broadcaster)
			require.NoError(t, err)

			stop, err := demo.RunHub(ctx, hub, 200*time.Millisecond)
			require.NotNil(t, stop, "stop is always safe to call")
			t.Cleanup(stop)

			tc.assert(t, hub, err)
		})
	}
}
