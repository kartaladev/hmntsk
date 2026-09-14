package redis_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/notifytest"
	"github.com/kartaladev/hmntsk/notify/redis"
)

// TestBroadcasterConformance holds the broadcaster to the notify.Broadcaster
// contract, readiness included: a publisher and a listener are two
// broadcasters on one broker, each with its own client, as two instances are.
func TestBroadcasterConformance(t *testing.T) {
	t.Parallel()

	broker := redis.RunTestRedis(t)

	var pairs atomic.Int64

	notifytest.RunBroadcasterSuite(t, func(t *testing.T) (notify.Broadcaster, notify.Broadcaster) {
		// A channel per pair, so that no case sees another's signals.
		channel := fmt.Sprintf("test.conformance.%d", pairs.Add(1))

		return newBroadcaster(t, broker, channel), newBroadcaster(t, broker, channel)
	})
}

// newBroadcaster builds a broadcaster on channel over a client of its own to
// broker, as one instance would. The client is closed when the test ends.
func newBroadcaster(t *testing.T, broker *goredis.Client, channel string, opts ...redis.Option) *redis.Broadcaster {
	t.Helper()

	client := goredis.NewClient(broker.Options())
	t.Cleanup(func() { _ = client.Close() })

	b, err := redis.NewBroadcaster(client, append([]redis.Option{redis.WithChannel(channel)}, opts...)...)
	require.NoError(t, err)

	return b
}
