package websocket_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	cws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// send writes one text message.
func send(t *testing.T, conn *cws.Conn, message string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()

	require.NoError(t, conn.Write(ctx, cws.MessageText, []byte(message)))
}

// readRaw reads one message's bytes.
func readRaw(t *testing.T, conn *cws.Conn) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()

	_, data, err := conn.Read(ctx)
	require.NoError(t, err)

	return data
}

// idOf returns the identifier of a recipient's only notification.
func (s *server) idOf(t *testing.T, recipient string) string {
	t.Helper()

	page, err := s.svc.List(t.Context(), notify.ListQuery{Recipient: recipient})
	require.NoError(t, err)
	require.Len(t, page.Notifications, 1)

	return page.Notifications[0].ID
}

// framesByType reads n frames and indexes them by their type.
func framesByType(t *testing.T, conn *cws.Conn, n int) map[string]map[string]any {
	t.Helper()

	frames := make(map[string]map[string]any, n)

	for range n {
		frame := readFrame(t, conn)

		kind, ok := frame["type"].(string)
		require.True(t, ok)

		frames[kind] = frame
	}

	return frames
}

func TestReadLoop(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		server serverConfig
		// before runs ahead of alice's connection, so that it produces no
		// signal on it.
		before func(t *testing.T, s *server)
		act    func(t *testing.T, s *server, conn *cws.Conn)
	}

	cases := []testCase{
		{
			name:   "mark-read marks the notification and answers with the reference, then signals the read",
			before: func(t *testing.T, s *server) { s.publish(t, "alice", "event-1") },
			act: func(t *testing.T, s *server, conn *cws.Conn) {
				send(t, conn, `{"type":"mark-read","ref":"r1","ids":["`+s.idOf(t, "alice")+`"]}`)

				frames := framesByType(t, conn, 2)
				assert.Equal(t, map[string]any{"type": "marked", "ref": "r1", "marked": float64(1)}, frames["marked"])
				assert.Equal(t, string(notify.ChangeRead), frames["unread-changed"]["change"])

				count, err := s.svc.CountActive(t.Context(), "alice")
				require.NoError(t, err)
				assert.Zero(t, count)
			},
		},
		{
			name:   "mark-all-read without through marks everything so far",
			before: func(t *testing.T, s *server) { s.publish(t, "alice", "event-1") },
			act: func(t *testing.T, _ *server, conn *cws.Conn) {
				send(t, conn, `{"type":"mark-all-read","ref":"r2"}`)

				frames := framesByType(t, conn, 2)
				assert.Equal(t, map[string]any{"type": "marked", "ref": "r2", "marked": float64(1)}, frames["marked"])
			},
		},
		{
			name:   "mark-all-read through an earlier instant marks nothing newer",
			before: func(t *testing.T, s *server) { s.publish(t, "alice", "event-1") },
			act: func(t *testing.T, s *server, conn *cws.Conn) {
				through := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
				send(t, conn, `{"type":"mark-all-read","ref":"r3","through":"`+through+`"}`)

				assert.Equal(t, map[string]any{"type": "marked", "ref": "r3", "marked": float64(0)}, readFrame(t, conn))

				count, err := s.svc.CountActive(t.Context(), "alice")
				require.NoError(t, err)
				assert.EqualValues(t, 1, count)
			},
		},
		{
			name:   "another recipient's notification is not found exactly like an unknown one",
			before: func(t *testing.T, s *server) { s.publish(t, "bob", "event-1") },
			act: func(t *testing.T, s *server, conn *cws.Conn) {
				bobs := s.idOf(t, "bob")

				send(t, conn, `{"type":"mark-read","ref":"r1","ids":["`+bobs+`"]}`)
				forBob := readRaw(t, conn)

				send(t, conn, `{"type":"mark-read","ref":"r1","ids":["`+bobs[:len(bobs)-1]+`x"]}`)
				forUnknown := readRaw(t, conn)

				assert.JSONEq(t, `{"type":"error","ref":"r1","code":"not_found","message":"notify: not found"}`, string(forBob))
				assert.Equal(t, forUnknown, forBob, "the two replies are byte for byte identical")

				count, err := s.svc.CountActive(t.Context(), "bob")
				require.NoError(t, err)
				assert.EqualValues(t, 1, count, "bob's notification is unchanged")
			},
		},
		{
			name: "a malformed message is a validation error and the connection stays open",
			act: func(t *testing.T, _ *server, conn *cws.Conn) {
				send(t, conn, `{"type":"shout","ref":"r9"}`)

				frame := readFrame(t, conn)
				assert.Equal(t, "error", frame["type"])
				assert.Equal(t, "r9", frame["ref"])
				assert.Equal(t, "validation_failed", frame["code"])

				send(t, conn, `not json at all`)
				assert.Equal(t, "validation_failed", readFrame(t, conn)["code"])

				send(t, conn, `{"type":"mark-all-read","ref":"r10"}`)
				assert.Equal(t, map[string]any{"type": "marked", "ref": "r10", "marked": float64(0)}, readFrame(t, conn))
			},
		},
		{
			name:   "a message over the read limit closes the connection as too big",
			server: serverConfig{ws: []websocket.Option{websocket.WithReadLimit(64)}},
			act: func(t *testing.T, _ *server, conn *cws.Conn) {
				send(t, conn, `{"type":"mark-read","ref":"r1","ids":["`+string(make([]byte, 128))+`"]}`)

				ctx, cancel := context.WithTimeout(t.Context(), testWait)
				defer cancel()

				_, _, err := conn.Read(ctx)

				var closeErr cws.CloseError
				require.ErrorAs(t, err, &closeErr, "the connection was closed")
				assert.Equal(t, cws.StatusMessageTooBig, closeErr.Code)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := startServer(t, tc.server)
			if tc.before != nil {
				tc.before(t, s)
			}

			d, err := s.dial(t, dialRequest{actor: "alice"})
			require.NoError(t, err)

			tc.act(t, s, d.conn)
		})
	}
}

// TestFramesAreJSON guards the reply shapes the protocol documents against a
// stray field.
func TestFramesAreJSON(t *testing.T) {
	t.Parallel()

	var frame map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"type":"marked","ref":"r1","marked":1}`), &frame))
	assert.Len(t, frame, 3)
}
