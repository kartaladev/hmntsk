package nats_test

import (
	"io"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// brokerProxy is a switchable TCP relay in front of the real server, so a test
// can take the server away and give it back without restarting a container —
// which would not come back on the same port.
//
// It is delivery/redis's proxy of the same name. That one lives in another
// module's test package, which cannot be imported.
//
// It starts closed: connections are accepted and dropped, which is what a
// server that is no longer there looks like to a client.
type brokerProxy struct {
	listener net.Listener
	upstream string

	mu    sync.Mutex
	open  bool
	conns []net.Conn
}

// newBrokerProxy starts a proxy in front of upstream, closed.
func newBrokerProxy(t *testing.T, upstream string) *brokerProxy {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "start the broker proxy")

	proxy := &brokerProxy{listener: listener, upstream: upstream}

	t.Cleanup(func() {
		_ = listener.Close()
		proxy.drop()
	})

	go proxy.serve()

	return proxy
}

// Addr is the address a client dials to reach the proxy.
func (p *brokerProxy) Addr() string { return p.listener.Addr().String() }

// Open lets the server be reached again.
func (p *brokerProxy) Open() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.open = true
}

// Close takes the server away: new connections are dropped, and every
// connection already wired through is cut, which is what a client sees when the
// server it was talking to goes down.
func (p *brokerProxy) Close() {
	p.mu.Lock()
	p.open = false
	p.mu.Unlock()

	p.drop()
}

// serve accepts connections for the life of the listener.
func (p *brokerProxy) serve() {
	for {
		downstream, err := p.listener.Accept()
		if err != nil {
			return
		}

		p.accept(downstream)
	}
}

// accept either wires a connection through to the server or drops it.
func (p *brokerProxy) accept(downstream net.Conn) {
	p.mu.Lock()
	open := p.open
	p.mu.Unlock()

	if !open {
		_ = downstream.Close()

		return
	}

	upstream, err := net.Dial("tcp", p.upstream)
	if err != nil {
		_ = downstream.Close()

		return
	}

	p.mu.Lock()
	p.conns = append(p.conns, downstream, upstream)
	p.mu.Unlock()

	go func() { _, _ = io.Copy(upstream, downstream) }()
	go func() { _, _ = io.Copy(downstream, upstream) }()
}

// drop closes every connection the proxy has wired up.
func (p *brokerProxy) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conn := range p.conns {
		_ = conn.Close()
	}

	p.conns = nil
}
