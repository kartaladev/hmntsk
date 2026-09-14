package redis_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/redis"
)

// How long a test waits for something asynchronous, and how often it looks.
const (
	testWait = 10 * time.Second
	testTick = 50 * time.Millisecond
)

// listening is a broadcaster running Listen in the background, with what it
// delivered and reported collected on channels.
type listening struct {
	broadcaster *redis.Broadcaster
	signals     chan notify.Signal
	decodeErrs  chan error
	done        chan error
	cancel      context.CancelFunc
}

// listen starts Listen on a channel. The test must cancel it; a case that does
// not is cancelled when the case ends.
func listen(t *testing.T, client goredis.UniversalClient, channel string) *listening {
	t.Helper()

	l := &listening{
		signals:    make(chan notify.Signal, 64),
		decodeErrs: make(chan error, 64),
		done:       make(chan error, 1),
	}

	b, err := redis.NewBroadcaster(client,
		redis.WithChannel(channel),
		redis.WithDecodeErrorHandler(func(_ context.Context, err error) { l.decodeErrs <- err }),
	)
	require.NoError(t, err)

	l.broadcaster = b

	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel

	go func() { l.done <- b.Listen(ctx, func(s notify.Signal) { l.signals <- s }) }()

	t.Cleanup(cancel)

	return l
}

// awaitLive broadcasts a signal until the listener delivers one, which proves
// its subscription is live, and drains it.
func (l *listening) awaitLive(t *testing.T) {
	t.Helper()

	require.Eventually(t, func() bool {
		if err := l.broadcaster.Broadcast(t.Context(), signalsFor(1)); err != nil {
			return false
		}

		select {
		case <-l.signals:
			return true
		case <-time.After(testTick):
			return false
		}
	}, testWait, testTick, "the listener receives a broadcast")

	// Broadcasts that crossed with the one delivered may still arrive.
	for {
		select {
		case <-l.signals:
		case <-time.After(2 * testTick):
			return
		}
	}
}

// TestListen runs its cases one at a time and not in parallel with the rest of
// the package, so that goleak can compare the goroutines left afterwards with
// the ones that existed before the broadcaster started listening.
//
//nolint:paralleltest // goleak needs the package quiet; see above.
func TestListen(t *testing.T) {
	client := redis.RunTestRedis(t)

	type testCase struct {
		name string
		// act drives the listener and returns what assert checks.
		act    func(t *testing.T, l *listening) error
		assert func(t *testing.T, l *listening, err error)
	}

	cases := []testCase{
		{
			name: "a broadcast signal is delivered decoded",
			act: func(t *testing.T, l *listening) error {
				l.awaitLive(t)

				return l.broadcaster.Broadcast(t.Context(), signalsFor(3))
			},
			assert: func(t *testing.T, l *listening, err error) {
				require.NoError(t, err)

				var got []notify.Signal
				for range 3 {
					select {
					case s := <-l.signals:
						got = append(got, s)
					case <-time.After(testWait):
						require.FailNow(t, "a broadcast signal was not delivered")
					}
				}

				assert.Equal(t, signalsFor(3), got)
			},
		},
		{
			name: "a message in an unknown format version is reported and receiving continues",
			act: func(t *testing.T, l *listening) error {
				l.awaitLive(t)
				require.NoError(t, client.Publish(t.Context(), l.broadcaster.Channel(),
					`{"v":99,"signals":[{"recipient":"bob","change":"created","at":"2026-09-14T08:30:00Z"}]}`).Err())

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
			name: "a malformed message is reported and receiving continues",
			act: func(t *testing.T, l *listening) error {
				l.awaitLive(t)
				require.NoError(t, client.Publish(t.Context(), l.broadcaster.Channel(), "not json").Err())

				return l.broadcaster.Broadcast(t.Context(), signalsFor(1))
			},
			assert: func(t *testing.T, l *listening, err error) {
				require.NoError(t, err)
				assert.Equal(t, signalsFor(1)[0], receive(t, l.signals))
				require.Error(t, receive(t, l.decodeErrs))
				assertNothing(t, l.signals, "no signal is delivered for the malformed message")
			},
		},
		{
			name: "cancelling stops listening and unsubscribes",
			act: func(t *testing.T, l *listening) error {
				l.awaitLive(t)
				l.cancel()

				select {
				case err := <-l.done:
					return err
				case <-time.After(testWait):
					require.FailNow(t, "Listen did not return after its context was cancelled")

					return nil
				}
			},
			assert: func(t *testing.T, l *listening, err error) {
				require.ErrorIs(t, err, context.Canceled, "Listen returns its context's error, as the notify contract says")

				counts, countErr := client.PubSubNumSub(t.Context(), l.broadcaster.Channel()).Result()
				require.NoError(t, countErr)
				assert.Zero(t, counts[l.broadcaster.Channel()], "the channel has no subscriber left")
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := goleak.IgnoreCurrent()

			l := listen(t, client, fmt.Sprintf("test.listen.%d", i))
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
