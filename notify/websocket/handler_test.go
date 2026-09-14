package websocket_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// someActor establishes a fixed acting user, for tests where who acts does not
// matter.
func someActor(*http.Request) (string, error) { return "alice", nil }

func TestNewHandler(t *testing.T) {
	t.Parallel()

	svc, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	hub, err := notify.NewHub(svc.Broadcaster())
	require.NoError(t, err)

	type testCase struct {
		name   string
		svc    *notify.Service
		hub    *notify.Hub
		opts   []websocket.Option
		assert func(t *testing.T, handler *websocket.Handler, err error)
	}

	configurationError := func(t *testing.T, handler *websocket.Handler, err error) {
		t.Helper()

		require.ErrorIs(t, err, notify.ErrConfiguration)
		assert.Nil(t, handler)
	}

	cases := []testCase{
		{
			name: "only the actor is required, and the route is under the default base path",
			svc:  svc, hub: hub,
			opts: []websocket.Option{websocket.WithActor(someActor)},
			assert: func(t *testing.T, handler *websocket.Handler, err error) {
				require.NoError(t, err)
				assert.Equal(t, "GET /v1/notifications/socket", handler.Pattern())
			},
		},
		{
			name: "a base path replaces the default, ignoring a trailing slash",
			svc:  svc, hub: hub,
			opts: []websocket.Option{websocket.WithActor(someActor), websocket.WithBasePath("/api/")},
			assert: func(t *testing.T, handler *websocket.Handler, err error) {
				require.NoError(t, err)
				assert.Equal(t, "GET /api/notifications/socket", handler.Pattern())
			},
		},
		{
			name: "every setting can be replaced at once",
			svc:  svc, hub: hub,
			opts: []websocket.Option{
				websocket.WithActor(someActor),
				websocket.WithSubscriptionAuthorizer(notify.AllowAll),
				websocket.WithOriginPatterns("app.example.com"),
				websocket.WithReadLimit(1024),
				websocket.WithPingInterval(time.Second),
				websocket.WithWriteTimeout(time.Second),
			},
			assert: func(t *testing.T, handler *websocket.Handler, err error) {
				require.NoError(t, err)
				assert.NotNil(t, handler)
			},
		},
		{name: "a missing actor", svc: svc, hub: hub, assert: configurationError},
		{name: "a missing service", hub: hub, opts: []websocket.Option{websocket.WithActor(someActor)}, assert: configurationError},
		{name: "a missing hub", svc: svc, opts: []websocket.Option{websocket.WithActor(someActor)}, assert: configurationError},
		{
			name: "a nil subscription policy", svc: svc, hub: hub,
			opts:   []websocket.Option{websocket.WithActor(someActor), websocket.WithSubscriptionAuthorizer(nil)},
			assert: configurationError,
		},
		{
			name: "a read limit of zero", svc: svc, hub: hub,
			opts:   []websocket.Option{websocket.WithActor(someActor), websocket.WithReadLimit(0)},
			assert: configurationError,
		},
		{
			name: "a negative ping interval", svc: svc, hub: hub,
			opts:   []websocket.Option{websocket.WithActor(someActor), websocket.WithPingInterval(-time.Second)},
			assert: configurationError,
		},
		{
			name: "a write timeout of zero", svc: svc, hub: hub,
			opts:   []websocket.Option{websocket.WithActor(someActor), websocket.WithWriteTimeout(0)},
			assert: configurationError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler, err := websocket.NewHandler(tc.svc, tc.hub, tc.opts...)
			tc.assert(t, handler, err)
		})
	}
}

func TestDefaultReadLimit(t *testing.T) {
	t.Parallel()

	assert.EqualValues(t, 4096, websocket.DefaultReadLimit)
	assert.Equal(t, "notify.v1", websocket.Subprotocol)
}
