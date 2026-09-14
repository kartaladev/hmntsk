package redis_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/redis"
)

// TestRunTestRedis proves the helper hands back a client that answers, and
// that its container is gone once the test ends.
func TestRunTestRedis(t *testing.T) {
	t.Parallel()

	client := redis.RunTestRedis(t)

	require.NoError(t, client.Ping(t.Context()).Err())
}
