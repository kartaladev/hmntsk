package nats_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/nats"
)

// How long a test waits for something asynchronous, and how often it looks.
const (
	testWait = 10 * time.Second
	testTick = 50 * time.Millisecond
)

// listening is a broadcaster running Listen in the background, with what it
// delivered and reported collected on channels.
type listening struct {
	broadcaster *nats.Broadcaster
	signals     chan notify.Signal
	decodeErrs  chan error
	done        chan error
	cancel      context.CancelFunc
}

// listen starts Listen on a subject and returns once the broadcaster reports its
// subscription confirmed, so a signal broadcast afterwards is delivered. It is
// cancelled when the case ends.
func listen(t *testing.T, conn *natsgo.Conn, subject string) *listening {
	t.Helper()

	l := &listening{
		signals:    make(chan notify.Signal, 64),
		decodeErrs: make(chan error, 64),
		done:       make(chan error, 1),
	}

	b, err := nats.NewBroadcaster(conn,
		nats.WithSubject(subject),
		nats.WithDecodeErrorHandler(func(_ context.Context, err error) { l.decodeErrs <- err }),
	)
	require.NoError(t, err)

	l.broadcaster = b

	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel

	ready := make(chan struct{})

	go func() {
		l.done <- b.Listen(ctx, func(s notify.Signal) { l.signals <- s }, func() { close(ready) })
	}()

	t.Cleanup(cancel)

	select {
	case <-ready:
	case err := <-l.done:
		require.FailNowf(t, "Listen returned before it was ready", "%v", err)
	case <-time.After(testWait):
		require.FailNow(t, "Listen did not become ready")
	}

	return l
}

// TestListen runs its cases one at a time and not in parallel with the rest of
// the package, so that goleak can compare the goroutines left afterwards with
// the ones that existed before the broadcaster started listening. The
// connection's own goroutines exist before that snapshot.
//
//nolint:paralleltest // goleak needs the package quiet; see above.
func TestListen(t *testing.T) {
	conn := nats.RunTestNATS(t)

	type testCase struct {
		name   string
		act    func(t *testing.T, l *listening) error
		assert func(t *testing.T, l *listening, err error)
	}

	cases := []testCase{
		{
			name: "a broadcast signal is delivered decoded",
			act: func(t *testing.T, l *listening) error {
				return l.broadcaster.Broadcast(t.Context(), signalsFor(3))
			},
			assert: func(t *testing.T, l *listening, err error) {
				require.NoError(t, err)

				var got []notify.Signal
				for range 3 {
					got = append(got, receive(t, l.signals))
				}

				assert.Equal(t, signalsFor(3), got)
			},
		},
		{
			name: "a message in an unknown format version is reported and receiving continues",
			act: func(t *testing.T, l *listening) error {
				require.NoError(t, conn.Publish(l.broadcaster.Subject(),
					[]byte(`{"v":99,"signals":[{"recipient":"bob","change":"created","at":"2026-09-14T08:30:00Z"}]}`)))

				return l.broadcaster.Broadcast(t.Context(), signalsFor(1))
			},
			assert: func(t *testing.T, l *listening, err error) {
				require.NoError(t, err)
				assert.Equal(t, signalsFor(1)[0], receive(t, l.signals), "the valid message after it is delivered")
				require.ErrorIs(t, receive(t, l.decodeErrs), notify.ErrUnknownSignalFormat)
				assertNothing(t, l.signals, "no signal is delivered for the unknown message")
			},
		},
		{
			name: "cancelling stops listening and unsubscribes",
			act: func(t *testing.T, l *listening) error {
				l.cancel()

				return receive(t, l.done)
			},
			assert: func(t *testing.T, l *listening, err error) {
				require.ErrorIs(t, err, context.Canceled, "Listen returns its context's error, as the notify contract says")
				require.NoError(t, l.broadcaster.Broadcast(t.Context(), signalsFor(1)))
				assertNothing(t, l.signals, "nothing is delivered after Listen returns")
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := goleak.IgnoreCurrent()

			l := listen(t, conn, fmt.Sprintf("test.listen.%d", i))
			err := tc.act(t, l)
			tc.assert(t, l, err)

			l.cancel()

			// A case that already consumed the result leaves nothing to read.
			select {
			case listenErr := <-l.done:
				if !errors.Is(listenErr, context.Canceled) {
					require.NoError(t, listenErr)
				}
			case <-time.After(testWait):
			}

			goleak.VerifyNone(t, before)
		})
	}
}

// receive waits for one value.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	select {
	case v := <-ch:
		return v
	case <-time.After(testWait):
		require.FailNow(t, "nothing arrived")

		var zero T

		return zero
	}
}

// assertNothing checks that nothing more arrives shortly.
func assertNothing[T any](t *testing.T, ch <-chan T, msg string) {
	t.Helper()

	select {
	case v := <-ch:
		assert.Failf(t, msg, "got %v", v)
	case <-time.After(5 * testTick):
	}
}
