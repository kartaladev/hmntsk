package websocket_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// How long a test waits for something asynchronous, and how often it looks.
const (
	testWait = 5 * time.Second
	testTick = 10 * time.Millisecond
)

// supervisorPolicy lets "sup" follow anyone and everyone else only themselves.
var supervisorPolicy = notify.SubscriptionAuthorizerFunc(func(ctx context.Context, actor, recipient string) error {
	if actor == "sup" {
		return nil
	}

	return notify.SelfOnly.AuthorizeSubscription(ctx, actor, recipient)
})

// refused asserts a handshake answered as an HTTP status with the notify error
// body, and not upgraded.
func refused(status int, code string) func(t *testing.T, d dialed, err error) {
	return func(t *testing.T, d dialed, err error) {
		t.Helper()

		require.Error(t, err)
		assert.Nil(t, d.conn)
		assert.Equal(t, status, d.status)

		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		require.NoError(t, json.Unmarshal(d.body, &envelope), "the refusal body is the notify error body: %s", d.body)
		assert.Equal(t, code, envelope.Error.Code)
	}
}

// upgraded asserts a handshake that succeeded.
func upgraded(t *testing.T, d dialed, err error) {
	t.Helper()

	require.NoError(t, err)
	require.NotNil(t, d.conn)
	assert.Equal(t, http.StatusSwitchingProtocols, d.status)
}

func TestHandshakeRefusals(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		server  serverConfig
		request dialRequest
		// before runs on the started server ahead of the dial under test.
		before func(t *testing.T, s *server)
		assert func(t *testing.T, d dialed, err error)
	}

	cases := []testCase{
		{
			name:    "a user connects for their own notifications",
			request: dialRequest{actor: "alice"},
			assert:  upgraded,
		},
		{
			name:    "no acting user is forbidden",
			request: dialRequest{},
			assert:  refused(http.StatusForbidden, "forbidden"),
		},
		{
			name:    "following someone else under the default policy is forbidden",
			request: dialRequest{actor: "alice", recipient: "bob"},
			assert:  refused(http.StatusForbidden, "forbidden"),
		},
		{
			name:    "a host policy permitting a supervisor upgrades",
			server:  serverConfig{ws: []websocket.Option{websocket.WithSubscriptionAuthorizer(supervisorPolicy)}},
			request: dialRequest{actor: "sup", recipient: "bob"},
			assert:  upgraded,
		},
		{
			name:    "a hub that is not receiving signals is unavailable",
			server:  serverConfig{stopped: true},
			request: dialRequest{actor: "alice"},
			assert:  refused(http.StatusServiceUnavailable, "unavailable"),
		},
		{
			name:    "the ninth connection, mixing streams and sockets, is too many",
			request: dialRequest{actor: "alice"},
			before: func(t *testing.T, s *server) {
				for range 4 {
					s.openStream(t, "alice")

					_, err := s.dial(t, dialRequest{actor: "alice"})
					require.NoError(t, err)
				}
			},
			assert: refused(http.StatusTooManyRequests, "too_many_streams"),
		},
		{
			name:    "a foreign browser origin is forbidden",
			request: dialRequest{actor: "alice", origin: "http://evil.example"},
			assert:  refused(http.StatusForbidden, "forbidden"),
		},
		{
			name:    "a listed origin pattern is permitted",
			server:  serverConfig{ws: []websocket.Option{websocket.WithOriginPatterns("app.example.com")}},
			request: dialRequest{actor: "alice", origin: "http://app.example.com"},
			assert:  upgraded,
		},
		{
			name:    "an origin outside the listed patterns is still forbidden",
			server:  serverConfig{ws: []websocket.Option{websocket.WithOriginPatterns("app.example.com")}},
			request: dialRequest{actor: "alice", origin: "http://evil.example"},
			assert:  refused(http.StatusForbidden, "forbidden"),
		},
		{
			name:    "any origin is permitted after the explicit opt-out",
			server:  serverConfig{ws: []websocket.Option{websocket.WithAnyOrigin()}},
			request: dialRequest{actor: "alice", origin: "http://evil.example"},
			assert:  upgraded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := startServer(t, tc.server)
			if tc.before != nil {
				tc.before(t, s)
			}

			d, err := s.dial(t, tc.request)
			tc.assert(t, d, err)
		})
	}
}

// TestSameHostOriginIsAccepted is separate because the origin it sends depends
// on the address the server was given.
func TestSameHostOriginIsAccepted(t *testing.T) {
	t.Parallel()

	s := startServer(t, serverConfig{})

	d, err := s.dial(t, dialRequest{actor: "alice", origin: s.url})
	upgraded(t, d, err)
}
