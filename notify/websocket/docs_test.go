package websocket_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// TestTheOperationsGuideMatchesTheHandler keeps notify/docs/realtime-operations.md
// from drifting away from the WebSocket handler's defaults.
func TestTheOperationsGuideMatchesTheHandler(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../docs/realtime-operations.md")
	require.NoError(t, err)

	document := string(raw)

	for _, stated := range []string{
		strconv.FormatInt(websocket.DefaultReadLimit, 10) + " bytes",
		"`" + websocket.Subprotocol + "`",
		"GET /v1/notifications/socket",
		notify.DefaultHeartbeat.String(),
		notify.DefaultWriteTimeout.String(),
		strconv.Itoa(notify.DefaultMaxStreamsPerRecipient) + " connections per instance",
		"not available on Fiber",
		"status 1001",
	} {
		assert.Containsf(t, document, stated, "the guide states %q", stated)
	}
}
