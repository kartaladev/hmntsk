package notify_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// listener collects what one Listen call delivers.
type listener struct {
	mu       sync.Mutex
	received []notify.Signal
	done     chan error
	cancel   context.CancelFunc
}

// listen starts a Listen call on its own goroutine.
func listen(t *testing.T, broadcaster notify.Broadcaster) *listener {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	l := &listener{done: make(chan error, 1), cancel: cancel}

	go func() {
		l.done <- broadcaster.Listen(ctx, func(signal notify.Signal) {
			l.mu.Lock()
			defer l.mu.Unlock()

			l.received = append(l.received, signal)
		})
	}()

	t.Cleanup(func() {
		cancel()
		<-l.done
	})

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

// broadcastUntil broadcasts a probe until every listener has received it, since
// a Listen call registers asynchronously, and then broadcasts signals once.
func broadcastUntil(t *testing.T, broadcaster notify.Broadcaster, listeners []*listener, signals []notify.Signal) {
	t.Helper()

	probe := notify.Signal{Recipient: "probe", Change: notify.ChangeCreated, At: serviceAt}
	deadline := time.Now().Add(2 * time.Second)

	for {
		require.NoError(t, broadcaster.Broadcast(t.Context(), []notify.Signal{probe}))

		ready := true

		for _, l := range listeners {
			if len(l.signals()) == 0 {
				ready = false
			}
		}

		if ready {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("a listener never received a broadcast")
		}

		time.Sleep(5 * time.Millisecond)
	}

	require.NoError(t, broadcaster.Broadcast(t.Context(), signals))
}

// withoutProbes drops the probes broadcastUntil sent.
func withoutProbes(signals []notify.Signal) []notify.Signal {
	var out []notify.Signal

	for _, signal := range signals {
		if signal.Recipient != "probe" {
			out = append(out, signal)
		}
	}

	return out
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

				broadcastUntil(t, broadcaster, []*listener{one, two}, []notify.Signal{alice, bob})

				assert.Equal(t, []notify.Signal{alice, bob}, withoutProbes(one.signals()))
				assert.Equal(t, []notify.Signal{alice, bob}, withoutProbes(two.signals()))
			},
		},
		{
			name: "a cancelled listener returns and receives nothing more",
			assert: func(t *testing.T, broadcaster *notify.InProcessBroadcaster) {
				stopped, running := listen(t, broadcaster), listen(t, broadcaster)
				broadcastUntil(t, broadcaster, []*listener{stopped, running}, nil)

				assert.ErrorIs(t, stopped.stop(t), context.Canceled)

				before := len(stopped.signals())
				broadcastUntil(t, broadcaster, []*listener{running}, []notify.Signal{alice})

				assert.Len(t, stopped.signals(), before)
				assert.Contains(t, running.signals(), alice)
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
