package nats_test

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/nats"
)

// proxy forwards TCP connections to a server and can drop every one of them at
// once, which is how a test takes a server away from a client without
// restarting a container, whose mapped port would change.
type proxy struct {
	listener net.Listener
	target   string

	mu    sync.Mutex
	conns []net.Conn
	wg    sync.WaitGroup
}

// startProxy forwards to target, a host:port, until the test ends.
func startProxy(t *testing.T, target string) *proxy {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	p := &proxy{listener: listener, target: target}

	p.wg.Add(1)

	go p.accept()

	t.Cleanup(func() {
		_ = listener.Close()
		p.dropAll()
		p.wg.Wait()
	})

	return p
}

// url is the proxy's NATS URL.
func (p *proxy) url() string { return "nats://" + p.listener.Addr().String() }

// accept forwards every accepted connection.
func (p *proxy) accept() {
	defer p.wg.Done()

	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}

		server, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = client.Close()

			continue
		}

		p.mu.Lock()
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()

		p.wg.Add(2)

		go p.pipe(client, server)
		go p.pipe(server, client)
	}
}

// pipe copies one direction, closing both ends when either stops.
func (p *proxy) pipe(dst, src net.Conn) {
	defer p.wg.Done()

	if _, err := io.Copy(dst, src); err != nil && !errors.Is(err, net.ErrClosed) {
		_ = err
	}

	_ = dst.Close()
	_ = src.Close()
}

// dropAll closes every forwarded connection; the proxy keeps accepting.
func (p *proxy) dropAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conn := range p.conns {
		_ = conn.Close()
	}

	p.conns = nil
}

// TestListenResumesAfterTheConnectionDrops drops the listener's connection to
// the server, and proves a signal broadcast after it reconnects is delivered to
// the same Listen, without the host restarting it.
func TestListenResumesAfterTheConnectionDrops(t *testing.T) {
	t.Parallel()

	var serverURL string

	publisher := nats.RunTestNATS(t, nats.WithTestURL(&serverURL))

	p := startProxy(t, strings.TrimPrefix(serverURL, "nats://"))

	reconnected := make(chan struct{}, 1)

	conn, err := natsgo.Connect(p.url(),
		natsgo.ReconnectWait(100*time.Millisecond),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectHandler(func(*natsgo.Conn) { reconnected <- struct{}{} }),
	)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	l := listen(t, conn, "test.reconnect")
	p.dropAll()

	select {
	case <-reconnected:
	case <-time.After(3 * testWait):
		require.FailNow(t, "the listener's connection did not reconnect")
	}

	publishing, err := nats.NewBroadcaster(publisher, nats.WithSubject("test.reconnect"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		if err := publishing.Broadcast(t.Context(), signalsFor(1)); err != nil {
			return false
		}

		select {
		case <-l.signals:
			return true
		case <-time.After(testTick):
			return false
		}
	}, 3*testWait, testTick, "a signal broadcast after the connection recovers reaches the same Listen")

	select {
	case err := <-l.done:
		require.FailNowf(t, "Listen returned instead of resuming", "%v", err)
	default:
	}
}
