package nats_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/nats"
)

// TestListenReturnsErrorWhenSubscriptionIsNotConfirmed takes the server away
// from a connection before Listen subscribes, so the server can never confirm
// the subscription, and proves Listen reports that instead of calling ready.
func TestListenReturnsErrorWhenSubscriptionIsNotConfirmed(t *testing.T) {
	t.Parallel()

	var serverURL string

	nats.RunTestNATS(t, nats.WithTestURL(&serverURL))

	type testCase struct {
		name    string
		timeout time.Duration
		// ctx derives the context Listen runs under from the test's.
		ctx    func(parent context.Context) (context.Context, context.CancelFunc)
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name:    "an unconfirmed subscription is an error once the subscribe timeout passes",
			timeout: 200 * time.Millisecond,
			ctx:     context.WithCancel,
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Contains(t, err.Error(), "confirm subscription")
			},
		},
		{
			name:    "a listen cancelled while waiting for confirmation returns its context's error",
			timeout: time.Minute,
			ctx: func(parent context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(parent)
				time.AfterFunc(100*time.Millisecond, cancel)

				return ctx, cancel
			},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, context.Canceled)
				assert.NotContains(t, err.Error(), "confirm subscription")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			conn := disconnected(t, serverURL)

			b, err := nats.NewBroadcaster(conn, nats.WithSubject("test.unconfirmed"), nats.WithSubscribeTimeout(tc.timeout))
			require.NoError(t, err)

			ctx, cancel := tc.ctx(t.Context())
			defer cancel()

			var readies atomic.Int32

			done := make(chan error, 1)

			go func() { done <- b.Listen(ctx, func(notify.Signal) {}, func() { readies.Add(1) }) }()

			select {
			case err := <-done:
				tc.assert(t, err)
			case <-time.After(testWait):
				require.FailNow(t, "Listen neither confirmed nor failed")
			}

			assert.Zero(t, readies.Load(), "ready is not called for an unconfirmed subscription")
		})
	}
}

// disconnected returns a connection that reached the server through a proxy
// and has since lost it for good: the proxy stops accepting and drops every
// forwarded connection, so the client keeps trying to reconnect.
func disconnected(t *testing.T, serverURL string) *natsgo.Conn {
	t.Helper()

	p := startProxy(t, strings.TrimPrefix(serverURL, "nats://"))

	conn, err := natsgo.Connect(p.url(),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(50*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	require.NoError(t, p.listener.Close())
	p.dropAll()

	require.Eventually(t, conn.IsReconnecting, testWait, testTick, "the connection loses the server")

	return conn
}
