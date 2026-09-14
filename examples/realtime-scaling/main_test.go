package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	notifynats "github.com/kartaladev/hmntsk/notify/nats"
	notifyredis "github.com/kartaladev/hmntsk/notify/redis"
)

func TestRun(t *testing.T) {
	// Real brokers, started in containers by the helpers notify's own adapter
	// tests use. Never a fake: what this scenario shows is the broker carrying
	// signals between instances.
	redisClient := notifyredis.RunTestRedis(t)
	natsConn := notifynats.RunTestNATS(t)

	// Not parallel, and checked for leaked goroutines once run returns: every
	// hub, stream, socket and server it started must be gone. The brokers'
	// client goroutines belong to the helpers, which close them at cleanup.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var out bytes.Buffer

	require.NoError(t, run(t.Context(), &out, brokers{redis: redisClient, nats: natsConn}))

	assert.Equal(t, `
== default: the in-process broadcaster reaches only its own instance
instances A and B share one notification store
alice's stream is open on A, bob's on B
notifications for alice and bob published through A
alice's stream on A: unread-changed (created)
bob's stream on B got a signal: false
bob GET /v1/notifications/count on B → 200 count=1

== override: a Redis broadcaster on every instance
instances A and B listen on Redis channel notify.signals
bob's stream is open on B
a notification for bob published through A
bob's stream on B: unread-changed (created)
the signal's data holds only: at, change
bob GET /v1/notifications on B → 200 approval-needed ACTIVE "Approve INV-61"

== override: a NATS broadcaster on every instance
instances A and B listen on NATS subject notify.signals
bob's stream is open on B
a notification for bob published through A
bob's stream on B: unread-changed (created)

== override: the WebSocket endpoint, and who may open it
bob opens a socket on B from origin https://evil.example → 403
bob opens a socket on B from origin https://app.example.com → 101, subprotocol notify.v1
a notification for bob published through A
bob's socket on B: unread-changed (created)
bob sends mark-read over the socket → marked 1
bob GET /v1/notifications/count on B → 200 count=0
`, out.String())
}
