package redis_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/redis"
)

// TestTheOperationsGuideMatchesTheBroadcaster keeps
// notify/docs/realtime-operations.md from drifting away from the Redis
// broadcaster's defaults.
func TestTheOperationsGuideMatchesTheBroadcaster(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../docs/realtime-operations.md")
	require.NoError(t, err)

	document := string(raw)

	for _, stated := range []string{
		"| Redis channel | `" + redis.DefaultChannel + "` |",
		"| Redis publish timeout | " + redis.DefaultPublishTimeout.String() + " |",
		"| Signals per message | " + strconv.Itoa(redis.MaxSignalsPerMessage) + " |",
		"not the task engine's `delivery/redis`",
	} {
		assert.Containsf(t, document, stated, "the guide states %q", stated)
	}
}
