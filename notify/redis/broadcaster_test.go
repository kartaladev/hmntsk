package redis_test

import (
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/redis"
)

// The broadcaster is a notify.Broadcaster.
var _ notify.Broadcaster = (*redis.Broadcaster)(nil)

func TestNewBroadcaster(t *testing.T) {
	t.Parallel()

	// Construction never dials, so a client pointed nowhere is enough.
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })

	type testCase struct {
		name   string
		client goredis.UniversalClient
		opts   []redis.Option
		assert func(t *testing.T, b *redis.Broadcaster, err error)
	}

	configurationError := func(t *testing.T, b *redis.Broadcaster, err error) {
		t.Helper()

		require.ErrorIs(t, err, redis.ErrConfiguration)
		assert.Nil(t, b)
	}

	cases := []testCase{
		{
			name:   "the defaults apply",
			client: client,
			assert: func(t *testing.T, b *redis.Broadcaster, err error) {
				require.NoError(t, err)
				assert.Equal(t, redis.DefaultChannel, b.Channel())
				assert.Equal(t, redis.DefaultPublishTimeout, b.PublishTimeout())
			},
		},
		{
			name:   "a channel and a timeout replace the defaults",
			client: client,
			opts: []redis.Option{
				redis.WithChannel("app-one.signals"),
				redis.WithPublishTimeout(time.Second),
				redis.WithDecodeErrorHandler(nil),
			},
			assert: func(t *testing.T, b *redis.Broadcaster, err error) {
				require.NoError(t, err)
				assert.Equal(t, "app-one.signals", b.Channel())
				assert.Equal(t, time.Second, b.PublishTimeout())
			},
		},
		{name: "no client", assert: configurationError},
		{name: "an empty channel", client: client, opts: []redis.Option{redis.WithChannel("")}, assert: configurationError},
		{name: "a zero timeout", client: client, opts: []redis.Option{redis.WithPublishTimeout(0)}, assert: configurationError},
		{name: "a negative timeout", client: client, opts: []redis.Option{redis.WithPublishTimeout(-time.Second)}, assert: configurationError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b, err := redis.NewBroadcaster(tc.client, tc.opts...)
			tc.assert(t, b, err)
		})
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "notify.signals", redis.DefaultChannel)
	assert.Equal(t, 5*time.Second, redis.DefaultPublishTimeout)
	assert.Equal(t, 500, redis.MaxSignalsPerMessage)
}
