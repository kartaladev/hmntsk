package websocket_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	cws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// publish stores a notification for a recipient carrying everything a signal
// must never repeat: a title, links, data, a kind and a subject.
func (s *server) publish(t *testing.T, recipient, source string) {
	t.Helper()

	_, err := s.svc.Publish(t.Context(), notify.Draft{
		Recipient: recipient,
		SourceID:  source,
		Subject:   "task-42",
		Kind:      "offer",
		Title:     "Approve invoice INV-42",
		Links:     map[string]string{"task": "/v1/tasks/42"},
		Data:      json.RawMessage(`{"amount":1200}`),
	})
	require.NoError(t, err)
}

// readFrame reads one message within the test wait.
func readFrame(t *testing.T, conn *cws.Conn) map[string]any {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()

	kind, data, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, cws.MessageText, kind, "every message is a JSON text frame")

	var frame map[string]any
	require.NoError(t, json.Unmarshal(data, &frame), "a frame is a JSON object: %s", data)

	return frame
}

func TestWriteLoop(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		server  serverConfig
		request dialRequest
		// act runs against the open connection and returns what assert checks.
		act    func(t *testing.T, s *server, conn *cws.Conn) any
		assert func(t *testing.T, s *server, result any)
	}

	var pings atomic.Int32

	cases := []testCase{
		{
			name:    "a published notification yields one unread-changed frame with only the change and its time",
			request: dialRequest{actor: "alice"},
			act: func(t *testing.T, s *server, conn *cws.Conn) any {
				s.publish(t, "alice", "event-1")

				return readFrame(t, conn)
			},
			assert: func(t *testing.T, _ *server, result any) {
				frame, ok := result.(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "unread-changed", frame["type"])
				assert.Equal(t, string(notify.ChangeCreated), frame["change"])

				at, ok := frame["at"].(string)
				require.True(t, ok, "the frame says when the change happened")
				_, err := time.Parse(time.RFC3339Nano, at)
				require.NoError(t, err)

				assert.Len(t, frame, 3, "nothing but type, change and at: no title, links, data, kind or subject")
			},
		},
		{
			name:    "an idle connection is pinged every interval",
			server:  serverConfig{ws: []websocket.Option{websocket.WithPingInterval(50 * time.Millisecond)}},
			request: dialRequest{actor: "alice", onPing: func() bool { pings.Add(1); return true }},
			act: func(t *testing.T, _ *server, conn *cws.Conn) any {
				// Reading is what answers pings; this client reads and discards.
				conn.CloseRead(t.Context())

				return nil
			},
			assert: func(t *testing.T, _ *server, _ any) {
				require.Eventually(t, func() bool { return pings.Load() >= 2 }, testWait, testTick,
					"the server pings an idle connection repeatedly")
			},
		},
		{
			name: "a client that stops reading is closed without slowing publishers",
			server: serverConfig{
				hub: []notify.HubOption{notify.WithMaxStreamsPerRecipient(1)},
				ws: []websocket.Option{
					websocket.WithPingInterval(50 * time.Millisecond),
					websocket.WithWriteTimeout(200 * time.Millisecond),
				},
			},
			// The client never reads, so it never answers a ping.
			request: dialRequest{actor: "alice"},
			act: func(t *testing.T, s *server, _ *cws.Conn) any {
				start := time.Now()

				for i := range 1000 {
					s.publish(t, "alice", fmt.Sprintf("event-%d", i))
				}

				return time.Since(start)
			},
			assert: func(t *testing.T, s *server, result any) {
				took, ok := result.(time.Duration)
				require.True(t, ok)
				assert.Less(t, took, 5*time.Second, "1,000 publishes never wait on the stalled client")

				// With a cap of one, a second connection is accepted only once
				// the stalled one was closed and its subscription released.
				require.Eventually(t, func() bool {
					d, err := s.dial(t, dialRequest{actor: "alice"})
					if err != nil {
						return false
					}

					_ = d.conn.CloseNow()

					return d.status == http.StatusSwitchingProtocols
				}, testWait, testTick, "the stalled connection is closed and frees its slot")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := startServer(t, tc.server)

			d, err := s.dial(t, tc.request)
			require.NoError(t, err)

			result := tc.act(t, s, d.conn)
			tc.assert(t, s, result)
		})
	}
}
