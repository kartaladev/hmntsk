package websocket_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	cws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// actorHeader carries the acting user in tests, standing in for a host's
// authentication middleware.
const actorHeader = "X-Actor"

// headerActor establishes the acting user from actorHeader.
func headerActor(r *http.Request) (string, error) { return r.Header.Get(actorHeader), nil }

// server is one instance under test: a service over the memory store, its hub,
// and both realtime transports mounted on one listener.
type server struct {
	url string
	svc *notify.Service
	hub *notify.Hub
}

// serverConfig is what a test varies about a server.
type serverConfig struct {
	stopped bool
	hub     []notify.HubOption
	ws      []websocket.Option
	service []notify.Option
}

// startServer starts an instance whose hub is running, unless the config says
// it is stopped, and tears everything down when the test ends.
func startServer(t *testing.T, cfg serverConfig) *server {
	t.Helper()

	svc, err := notify.New(notify.NewMemoryStore(), cfg.service...)
	require.NoError(t, err)

	hub, err := notify.NewHub(svc.Broadcaster(), cfg.hub...)
	require.NoError(t, err)

	if !cfg.stopped {
		runHub(t, hub)
	}

	wsHandler, err := websocket.NewHandler(svc, hub, append([]websocket.Option{websocket.WithActor(headerActor)}, cfg.ws...)...)
	require.NoError(t, err)

	sseHandler, err := notify.NewHandler(svc, hub, notify.WithActor(headerActor))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(wsHandler.Pattern(), wsHandler)
	mux.Handle("/v1/notifications/", sseHandler)

	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	return &server{url: httpServer.URL, svc: svc, hub: hub}
}

// runHub runs a hub until the test ends, and waits for it to stop.
func runHub(t *testing.T, hub *notify.Hub) {
	t.Helper()

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

	require.Eventually(t, hub.Running, testWait, testTick)
}

// dialRequest is who connects, for whom, and from which page.
type dialRequest struct {
	actor     string
	recipient string
	origin    string
	// onPing, when set, sees every ping the client receives. Returning false
	// withholds the pong, as a stalled client would.
	onPing func() bool
}

// dialed is what a handshake came to: the connection when it was upgraded, and
// the status and body the server answered with.
type dialed struct {
	conn   *cws.Conn
	status int
	body   []byte
}

// dial opens a WebSocket connection, closing it when the test ends. A refused
// handshake's body is read and closed here, so a test inspects it as bytes.
func (s *server) dial(t *testing.T, req dialRequest) (dialed, error) {
	t.Helper()

	target := "ws" + strings.TrimPrefix(s.url, "http") + "/v1/notifications/socket"
	if req.recipient != "" {
		target += "?recipient=" + url.QueryEscape(req.recipient)
	}

	header := http.Header{}
	if req.actor != "" {
		header.Set(actorHeader, req.actor)
	}

	if req.origin != "" {
		header.Set("Origin", req.origin)
	}

	options := &cws.DialOptions{HTTPHeader: header}
	if req.onPing != nil {
		options.OnPingReceived = func(context.Context, []byte) bool { return req.onPing() }
	}

	conn, resp, err := cws.Dial(t.Context(), target, options)

	var d dialed

	if resp != nil {
		d.status = resp.StatusCode

		if err != nil && resp.Body != nil {
			d.body, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	}

	if conn != nil {
		d.conn = conn
		t.Cleanup(func() { _ = conn.CloseNow() })
	}

	return d, err
}

// openStream opens a recipient's server-sent event stream on the instance,
// closing it when the test ends.
func (s *server) openStream(t *testing.T, actor string) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/v1/notifications/stream", http.NoBody)
	require.NoError(t, err)

	req.Header.Set(actorHeader, actor)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
}
