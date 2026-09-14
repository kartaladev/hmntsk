package websocket_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// TestShutdownClosesConnectionsAsGoingAway cancels the base context of the
// server a connection was accepted on, the way a host shuts WebSockets down,
// and proves the connection is closed with status 1001 and its slot in the
// hub's cap released. A second server over the same hub, with a cap of one,
// accepts the recipient again only once that slot is free; goleak in TestMain
// proves the connection's goroutines stopped.
func TestShutdownClosesConnectionsAsGoingAway(t *testing.T) {
	t.Parallel()

	svc, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	hub, err := notify.NewHub(svc.Broadcaster(), notify.WithMaxStreamsPerRecipient(1))
	require.NoError(t, err)

	runHub(t, hub)

	handler, err := websocket.NewHandler(svc, hub, websocket.WithActor(headerActor))
	require.NoError(t, err)

	baseCtx, shutdown := context.WithCancel(context.Background())
	t.Cleanup(shutdown)

	shuttingDown := httptest.NewUnstartedServer(handler)
	shuttingDown.Config.BaseContext = func(net.Listener) context.Context { return baseCtx }
	shuttingDown.Start()
	t.Cleanup(shuttingDown.Close)

	staying := httptest.NewServer(handler)
	t.Cleanup(staying.Close)

	conn := dialURL(t, shuttingDown.URL)

	readErr := make(chan error, 1)

	go func() {
		_, _, err := conn.Read(context.Background())
		readErr <- err
	}()

	shutdown()

	err = receiveErr(t, readErr)

	var closeErr cws.CloseError
	require.ErrorAs(t, err, &closeErr, "the connection was closed with a status")
	assert.Equal(t, cws.StatusGoingAway, closeErr.Code)

	require.Eventually(t, func() bool {
		again, resp, err := cws.Dial(t.Context(), "ws"+strings.TrimPrefix(staying.URL, "http")+"/v1/notifications/socket",
			&cws.DialOptions{HTTPHeader: http.Header{actorHeader: []string{"alice"}}})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}

		if err != nil {
			return false
		}

		_ = again.CloseNow()

		return true
	}, testWait, testTick, "the closed connection's slot is released")
}

// dialURL opens alice's connection on a server, closing it when the test ends.
func dialURL(t *testing.T, serverURL string) *cws.Conn {
	t.Helper()

	conn, resp, err := cws.Dial(t.Context(), "ws"+strings.TrimPrefix(serverURL, "http")+"/v1/notifications/socket",
		&cws.DialOptions{HTTPHeader: http.Header{actorHeader: []string{"alice"}}})
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}

	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

// receiveErr waits for an error on a channel.
func receiveErr(t *testing.T, ch <-chan error) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-t.Context().Done():
		require.FailNow(t, "the test ended first")

		return nil
	}
}
