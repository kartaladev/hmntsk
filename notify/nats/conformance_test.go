package nats_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/nats"
	"github.com/kartaladev/hmntsk/notify/notifytest"
)

// TestBroadcasterConformance holds the NATS broadcaster to the notify
// broadcaster contract. Publisher and listener are two broadcasters with their
// own connections to one server, as two instances would be; each pair gets a
// subject of its own, so the suite's cases cannot see each other's signals.
func TestBroadcasterConformance(t *testing.T) {
	t.Parallel()

	var serverURL string

	nats.RunTestNATS(t, nats.WithTestURL(&serverURL))

	var pairs atomic.Int64

	notifytest.RunBroadcasterSuite(t, func(t *testing.T) (publisher, listener notify.Broadcaster) {
		t.Helper()

		subject := fmt.Sprintf("test.conformance.%d", pairs.Add(1))

		return broadcasterOn(t, serverURL, subject), broadcasterOn(t, serverURL, subject)
	})
}

// broadcasterOn opens a connection of its own to the server, with any connect
// options, and builds a broadcaster on subject over it, as one instance would.
// The connection is closed when the test ends.
func broadcasterOn(t *testing.T, serverURL, subject string, connect ...natsgo.Option) *nats.Broadcaster {
	t.Helper()

	conn, err := natsgo.Connect(serverURL, connect...)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	b, err := nats.NewBroadcaster(conn, nats.WithSubject(subject))
	require.NoError(t, err)

	return b
}
