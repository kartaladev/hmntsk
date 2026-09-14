package redis_test

import (
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/redis"
)

func TestListenRefusesMissingFunctionsWithTheAdapterError(t *testing.T) {
	t.Parallel()

	// Listen refuses before touching the client, so a client that can never
	// connect is enough.
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })

	broadcaster, err := redis.NewBroadcaster(client)
	require.NoError(t, err)

	type testCase struct {
		name    string
		deliver func(notify.Signal)
		ready   func()
		assert  func(t *testing.T, err error)
	}

	matchesBoth := func(t *testing.T, err error) {
		t.Helper()

		var configuration *redis.ConfigurationError
		require.ErrorAs(t, err, &configuration, "the adapter's own type, as NewBroadcaster returns")
		assert.ErrorIs(t, err, redis.ErrConfiguration)
		assert.ErrorIs(t, err, notify.ErrConfiguration)
	}

	cases := []testCase{
		{name: "nil deliver", ready: func() {}, assert: matchesBoth},
		{name: "nil ready", deliver: func(notify.Signal) {}, assert: matchesBoth},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, broadcaster.Listen(t.Context(), tc.deliver, tc.ready))
		})
	}
}
