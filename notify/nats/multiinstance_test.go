package nats_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cws "github.com/coder/websocket"
	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/nats"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// instance is one application instance: its own connection, store, service and
// hub, sharing a server with the others, serving WebSocket connections.
type instance struct {
	svc          *notify.Service
	url          string
	signalErrors chan error
	published    atomic.Int64
}

// startInstance starts an instance on its own connection to the server, with
// its hub receiving signals on subject.
func startInstance(t *testing.T, serverURL, subject string, connect ...natsgo.Option) *instance {
	t.Helper()

	conn, err := natsgo.Connect(serverURL, connect...)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	b, err := nats.NewBroadcaster(conn, nats.WithSubject(subject))
	require.NoError(t, err)

	in := &instance{signalErrors: make(chan error, 64)}

	in.svc, err = notify.New(notify.NewMemoryStore(),
		notify.WithBroadcaster(b),
		notify.WithSignalErrorHandler(func(_ context.Context, err error) { in.signalErrors <- err }),
	)
	require.NoError(t, err)

	hub, err := notify.NewHub(in.svc.Broadcaster())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = hub.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	handler, err := websocket.NewHandler(in.svc, hub,
		websocket.WithActor(func(r *http.Request) (string, error) { return r.Header.Get("X-Actor"), nil }))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(handler.Pattern(), handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	in.url = server.URL

	require.Eventually(t, hub.Running, testWait, testTick)

	return in
}

// publish stores a new notification for a recipient on the instance.
func (in *instance) publish(t *testing.T, recipient string) error {
	t.Helper()

	_, err := in.svc.Publish(t.Context(), notify.Draft{
		Recipient: recipient,
		SourceID:  fmt.Sprintf("event-%d", in.published.Add(1)),
		Subject:   "task-42",
		Kind:      "offer",
		Title:     "Approve invoice INV-42",
	})

	return err
}

// dial opens a recipient's WebSocket connection on the instance.
func (in *instance) dial(t *testing.T, recipient string) *cws.Conn {
	t.Helper()

	conn, resp, err := cws.Dial(t.Context(), "ws"+strings.TrimPrefix(in.url, "http")+"/v1/notifications/socket",
		&cws.DialOptions{HTTPHeader: http.Header{"X-Actor": []string{recipient}}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

// TestSignalsCrossInstances proves a notification published on one instance
// reaches a WebSocket connection on another over NATS.
func TestSignalsCrossInstances(t *testing.T) {
	t.Parallel()

	var serverURL string

	nats.RunTestNATS(t, nats.WithTestURL(&serverURL))

	a := startInstance(t, serverURL, "shared.signals")
	b := startInstance(t, serverURL, "shared.signals")

	conn := b.dial(t, "alice")

	frames := make(chan string, 64)
	readCtx, cancelRead := context.WithCancel(context.Background())
	readDone := make(chan struct{})

	go func() {
		defer close(readDone)

		for {
			_, data, err := conn.Read(readCtx)
			if err != nil {
				return
			}

			frames <- string(data)
		}
	}()

	t.Cleanup(func() {
		cancelRead()
		_ = conn.CloseNow()
		<-readDone
	})

	var frame string

	require.Eventually(t, func() bool {
		if a.publish(t, "alice") != nil {
			return false
		}

		select {
		case frame = <-frames:
			return true
		case <-time.After(testTick):
			return false
		}
	}, testWait, testTick, "a notification published on A reaches alice's WebSocket connection on B")

	assert.Contains(t, frame, `"type":"unread-changed"`)
	assert.Contains(t, frame, `"change":"created"`)
}

// TestPublishingSurvivesABrokerOutage stops the server and proves a publish
// still stores the notification and succeeds, with the broadcast failure handed
// to the service's signal error handler. The connection keeps no reconnect
// buffer, so a publish while the server is away fails rather than waiting in
// memory.
func TestPublishingSurvivesABrokerOutage(t *testing.T) {
	t.Parallel()

	var (
		container testcontainers.Container
		serverURL string
	)

	nats.RunTestNATS(t,
		nats.WithTestContainer(func(c testcontainers.Container) { container = c }),
		nats.WithTestURL(&serverURL),
	)

	disconnected := make(chan struct{}, 1)

	in := startInstance(t, serverURL, "outage.signals",
		natsgo.ReconnectBufSize(-1),
		natsgo.DisconnectErrHandler(func(*natsgo.Conn, error) {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}),
	)

	stop := 5 * time.Second
	require.NoError(t, container.Stop(t.Context(), &stop))

	select {
	case <-disconnected:
	case <-time.After(testWait):
		require.FailNow(t, "the instance did not notice the server went away")
	}

	require.NoError(t, in.publish(t, "alice"), "the publish succeeds while the server is down")

	count, err := in.svc.CountActive(t.Context(), "alice")
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "the notification is stored")

	select {
	case signalErr := <-in.signalErrors:
		require.ErrorIs(t, signalErr, nats.ErrPublish)
	case <-time.After(testWait):
		require.FailNow(t, "the broadcast failure was not reported to the signal error handler")
	}
}
