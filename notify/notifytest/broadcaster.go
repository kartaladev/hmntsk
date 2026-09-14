package notifytest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// PairFactory returns two broadcasters that reach each other: a signal the
// publisher broadcasts is delivered to the listener's Listen calls. For a
// broadcaster that reaches only its own process they are the same value; for
// one that crosses instances they are two broadcasters on one broker, each with
// its own client. It registers any cleanup they need on t.
//
// The suite's cases run in parallel, so every call must return a pair that
// shares nothing with another call's, such as a channel or subject of its own.
type PairFactory func(t *testing.T) (publisher, listener notify.Broadcaster)

// BroadcasterIterations is how many fresh Listen calls the readiness case makes.
// A broadcaster that reports ready before its subscription is live loses the
// first signal on some of them, and this many finds it in almost every run.
const BroadcasterIterations = 50

// broadcasterWait bounds every wait in the suite. A conforming broadcaster
// answers far sooner; the bound only turns a hang into a failure.
const broadcasterWait = 5 * time.Second

// RunBroadcasterSuite checks a [notify.Broadcaster] against its contract:
//
//   - a nil deliver or ready is an error matching [notify.ErrConfiguration], and
//     ready is not called. An adapter may return its own configuration error
//     type, as long as it also matches that sentinel;
//   - ready is called exactly once, before Listen returns;
//   - a signal broadcast from inside ready, the earliest moment a host could
//     act on it, is delivered, with no sleep and no retry;
//   - Listen returns ctx's error once ctx is cancelled, and delivers nothing
//     after returning.
func RunBroadcasterSuite(t *testing.T, newPair PairFactory) {
	t.Helper()

	type testCase struct {
		name   string
		assert func(t *testing.T, publisher, listener notify.Broadcaster)
	}

	cases := []testCase{
		{name: "a nil deliver or ready is a configuration error", assert: assertRefusesMissingFunctions},
		{name: "ready is called exactly once, before Listen returns", assert: assertReadyOnce},
		{name: "a signal broadcast right after ready is delivered", assert: assertDeliveredAfterReady},
		{name: "a cancelled Listen returns ctx's error and delivers nothing more", assert: assertCancelled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			publisher, listener := newPair(t)
			tc.assert(t, publisher, listener)
		})
	}
}

// listening is one Listen call running on its own goroutine.
type listening struct {
	ready   chan struct{}
	readies atomic.Int32
	late    atomic.Int32
	cancel  context.CancelFunc

	// exited is closed once Listen has returned, after err is set.
	exited chan struct{}
	err    error

	mu       sync.Mutex
	received []notify.Signal
	arrived  chan struct{}
}

// startListening starts Listen with a counting ready and a recording deliver.
// onReady, when not nil, runs inside the first ready call, before ready
// returns.
func startListening(t *testing.T, listener notify.Broadcaster, onReady func()) *listening {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	l := &listening{
		ready:   make(chan struct{}),
		cancel:  cancel,
		exited:  make(chan struct{}),
		arrived: make(chan struct{}, 1),
	}

	deliver := func(signal notify.Signal) {
		select {
		case <-l.exited:
			l.late.Add(1)
		default:
		}

		l.mu.Lock()
		l.received = append(l.received, signal)
		l.mu.Unlock()

		select {
		case l.arrived <- struct{}{}:
		default:
		}
	}

	ready := func() {
		if l.readies.Add(1) != 1 {
			return
		}

		if onReady != nil {
			onReady()
		}

		close(l.ready)
	}

	go func() {
		l.err = listener.Listen(ctx, deliver, ready)
		close(l.exited)
	}()

	t.Cleanup(func() {
		cancel()

		select {
		case <-l.exited:
		case <-time.After(broadcasterWait):
			t.Error("Listen did not return after its context was cancelled")
		}
	})

	return l
}

// awaitReady waits for ready, failing when Listen returns or hangs first.
func (l *listening) awaitReady(t *testing.T) {
	t.Helper()

	select {
	case <-l.ready:
	case <-l.exited:
		t.Fatalf("Listen returned before calling ready: %v", l.err)
	case <-time.After(broadcasterWait):
		t.Fatal("Listen never called ready")
	}
}

// stop cancels Listen and returns its error.
func (l *listening) stop(t *testing.T) error {
	t.Helper()

	l.cancel()

	select {
	case <-l.exited:
		return l.err
	case <-time.After(broadcasterWait):
		t.Fatal("Listen did not return after its context was cancelled")

		return nil
	}
}

// hasRecipient reports whether a signal for recipient has been delivered,
// without waiting.
func (l *listening) hasRecipient(recipient string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, signal := range l.received {
		if signal.Recipient == recipient {
			return true
		}
	}

	return false
}

// awaitRecipient waits until a signal for recipient has been delivered.
func (l *listening) awaitRecipient(recipient string) bool {
	deadline := time.After(broadcasterWait)

	for !l.hasRecipient(recipient) {
		select {
		case <-l.arrived:
		case <-deadline:
			return false
		}
	}

	return true
}

func assertRefusesMissingFunctions(t *testing.T, _, listener notify.Broadcaster) {
	t.Helper()

	type testCase struct {
		name       string
		nilDeliver bool
		nilReady   bool
		assert     func(t *testing.T, err error, readyCalls int32)
	}

	refused := func(t *testing.T, err error, readyCalls int32) {
		t.Helper()

		require.ErrorIs(t, err, notify.ErrConfiguration)
		assert.Zero(t, readyCalls, "ready is not called for a refused Listen")
	}

	cases := []testCase{
		{name: "nil deliver", nilDeliver: true, assert: refused},
		{name: "nil ready", nilReady: true, assert: refused},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32

			deliver := func(notify.Signal) {}
			ready := func() { calls.Add(1) }

			if tc.nilDeliver {
				deliver = nil
			}

			if tc.nilReady {
				ready = nil
			}

			ctx, cancel := context.WithTimeout(t.Context(), broadcasterWait)
			defer cancel()

			err := listener.Listen(ctx, deliver, ready)
			tc.assert(t, err, calls.Load())
		})
	}
}

func assertReadyOnce(t *testing.T, _, listener notify.Broadcaster) {
	t.Helper()

	l := startListening(t, listener, nil)
	l.awaitReady(t)

	require.ErrorIs(t, l.stop(t), context.Canceled)
	assert.Equal(t, int32(1), l.readies.Load())
}

func assertDeliveredAfterReady(t *testing.T, publisher, listener notify.Broadcaster) {
	t.Helper()

	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	for i := range BroadcasterIterations {
		recipient := fmt.Sprintf("readiness-%d", i)
		signal := notify.Signal{Recipient: recipient, Change: notify.ChangeCreated, At: at}

		// The broadcast happens inside ready, before it returns: the earliest
		// moment a host could act on readiness. A broadcaster that reports ready
		// before its subscription is live loses this signal.
		var broadcastErr error

		l := startListening(t, listener, func() {
			broadcastErr = publisher.Broadcast(t.Context(), []notify.Signal{signal})
		})
		l.awaitReady(t)
		require.NoError(t, broadcastErr)

		if !l.awaitRecipient(recipient) {
			t.Fatalf("iteration %d: a signal broadcast right after ready was not delivered", i)
		}

		require.ErrorIs(t, l.stop(t), context.Canceled)
	}
}

func assertCancelled(t *testing.T, publisher, listener notify.Broadcaster) {
	t.Helper()

	l := startListening(t, listener, nil)
	l.awaitReady(t)

	require.ErrorIs(t, l.stop(t), context.Canceled)

	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	signal := notify.Signal{Recipient: "after-return", Change: notify.ChangeCreated, At: at}
	require.NoError(t, publisher.Broadcast(t.Context(), []notify.Signal{signal}))

	// A broadcaster that kept delivering would do so promptly; this is long
	// enough to see it and short enough not to slow the suite.
	time.Sleep(100 * time.Millisecond)

	assert.Zero(t, l.late.Load(), "nothing is delivered after Listen returns")
}
