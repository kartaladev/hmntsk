package redis_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/redis"
)

// TestListenResumesAfterTheConnectionDrops kills the listener's subscription
// connection on the broker, and proves a signal broadcast after it recovers is
// delivered to the same Listen, without the host restarting it.
func TestListenResumesAfterTheConnectionDrops(t *testing.T) {
	t.Parallel()

	client := redis.RunTestRedis(t)

	l := listen(t, client, "test.reconnect")
	l.awaitLive(t)

	killed, err := client.Do(t.Context(), "CLIENT", "KILL", "TYPE", "pubsub").Int()
	require.NoError(t, err)
	require.Positive(t, killed, "the listener's subscription connection was killed")

	require.Eventually(t, func() bool {
		if err := l.broadcaster.Broadcast(t.Context(), signalsFor(1)); err != nil {
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
