package nats_test

import (
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/nats"
)

// The broadcaster is a notify.Broadcaster.
var _ notify.Broadcaster = (*nats.Broadcaster)(nil)

func TestNewBroadcaster(t *testing.T) {
	t.Parallel()

	// Construction never publishes, so an unconnected connection value is
	// enough to tell a present connection from a missing one.
	conn := &natsgo.Conn{}

	type testCase struct {
		name   string
		conn   *natsgo.Conn
		opts   []nats.Option
		assert func(t *testing.T, b *nats.Broadcaster, err error)
	}

	configurationError := func(t *testing.T, b *nats.Broadcaster, err error) {
		t.Helper()

		require.ErrorIs(t, err, nats.ErrConfiguration)
		assert.Nil(t, b)
	}

	subject := func(s string) []nats.Option { return []nats.Option{nats.WithSubject(s)} }

	cases := []testCase{
		{
			name: "the default subject applies",
			conn: conn,
			assert: func(t *testing.T, b *nats.Broadcaster, err error) {
				require.NoError(t, err)
				assert.Equal(t, nats.DefaultSubject, b.Subject())
			},
		},
		{
			name: "a subject replaces the default",
			conn: conn,
			opts: []nats.Option{nats.WithSubject("app-one.signals"), nats.WithDecodeErrorHandler(nil)},
			assert: func(t *testing.T, b *nats.Broadcaster, err error) {
				require.NoError(t, err)
				assert.Equal(t, "app-one.signals", b.Subject())
			},
		},
		{name: "no connection", assert: configurationError},
		{name: "an empty subject", conn: conn, opts: subject(""), assert: configurationError},
		{name: "a single-token wildcard", conn: conn, opts: subject("notify.*"), assert: configurationError},
		{name: "a full wildcard", conn: conn, opts: subject("notify.>"), assert: configurationError},
		{name: "an empty token", conn: conn, opts: subject("notify..signals"), assert: configurationError},
		{name: "a trailing dot", conn: conn, opts: subject("notify.signals."), assert: configurationError},
		{name: "whitespace", conn: conn, opts: subject("notify signals"), assert: configurationError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b, err := nats.NewBroadcaster(tc.conn, tc.opts...)
			tc.assert(t, b, err)
		})
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "notify.signals", nats.DefaultSubject)
	assert.Equal(t, 500, nats.MaxSignalsPerMessage)
}
