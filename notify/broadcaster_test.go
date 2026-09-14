package notify_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/notifytest"
)

// listener collects what one Listen call delivers.
type listener struct {
	mu       sync.Mutex
	received []notify.Signal
	done     chan error
	cancel   context.CancelFunc
}

// listen starts a Listen call on its own goroutine and waits until it is ready.
func listen(t *testing.T, broadcaster notify.Broadcaster) *listener {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	l := &listener{done: make(chan error, 1), cancel: cancel}
	ready := make(chan struct{})

	go func() {
		l.done <- broadcaster.Listen(ctx, func(signal notify.Signal) {
			l.mu.Lock()
			defer l.mu.Unlock()

			l.received = append(l.received, signal)
		}, func() { close(ready) })
	}()

	t.Cleanup(func() {
		cancel()
		<-l.done
	})

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen never became ready")
	}

	return l
}

// signals returns what the listener received.
func (l *listener) signals() []notify.Signal {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]notify.Signal(nil), l.received...)
}

// stop cancels the listener and waits for Listen to return its error.
func (l *listener) stop(t *testing.T) error {
	t.Helper()

	l.cancel()

	select {
	case err := <-l.done:
		l.done <- err // leave it for cleanup

		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not return after its context was cancelled")

		return nil
	}
}

func TestInProcessBroadcasterConformance(t *testing.T) {
	t.Parallel()

	notifytest.RunBroadcasterSuite(t, func(*testing.T) (notify.Broadcaster, notify.Broadcaster) {
		broadcaster := notify.NewInProcessBroadcaster()

		return broadcaster, broadcaster
	})
}

// The conformance suite checks the sentinel; this checks the in-process
// broadcaster's own refusal is the notify ConfigurationError type.
func TestInProcessBroadcasterListenRefusesMissingFunctions(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		deliver func(notify.Signal)
		ready   func()
		assert  func(t *testing.T, err error)
	}

	isConfiguration := func(t *testing.T, err error) {
		t.Helper()

		var configuration *notify.ConfigurationError
		assert.ErrorAs(t, err, &configuration)
	}

	cases := []testCase{
		{name: "a nil deliver", ready: func() {}, assert: isConfiguration},
		{name: "a nil ready", deliver: func(notify.Signal) {}, assert: isConfiguration},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, notify.NewInProcessBroadcaster().Listen(t.Context(), tc.deliver, tc.ready))
		})
	}
}

func TestInProcessBroadcaster(t *testing.T) {
	t.Parallel()

	alice := notify.Signal{Recipient: "alice", Change: notify.ChangeCreated, At: serviceAt}
	bob := notify.Signal{Recipient: "bob", Change: notify.ChangeRead, At: serviceAt}

	type testCase struct {
		name   string
		assert func(t *testing.T, broadcaster *notify.InProcessBroadcaster)
	}

	cases := []testCase{
		{
			name: "a broadcast reaches every listener, in order",
			assert: func(t *testing.T, broadcaster *notify.InProcessBroadcaster) {
				one, two := listen(t, broadcaster), listen(t, broadcaster)

				require.NoError(t, broadcaster.Broadcast(t.Context(), []notify.Signal{alice, bob}))

				assert.Equal(t, []notify.Signal{alice, bob}, one.signals())
				assert.Equal(t, []notify.Signal{alice, bob}, two.signals())
			},
		},
		{
			name: "a cancelled listener returns and receives nothing more",
			assert: func(t *testing.T, broadcaster *notify.InProcessBroadcaster) {
				stopped, running := listen(t, broadcaster), listen(t, broadcaster)

				assert.ErrorIs(t, stopped.stop(t), context.Canceled)

				require.NoError(t, broadcaster.Broadcast(t.Context(), []notify.Signal{alice}))

				assert.Empty(t, stopped.signals())
				assert.Equal(t, []notify.Signal{alice}, running.signals())
			},
		},
		{
			name: "a broadcast with no listener succeeds",
			assert: func(t *testing.T, broadcaster *notify.InProcessBroadcaster) {
				assert.NoError(t, broadcaster.Broadcast(t.Context(), []notify.Signal{alice}))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, notify.NewInProcessBroadcaster())
		})
	}
}
