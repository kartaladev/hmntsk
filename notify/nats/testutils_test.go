package nats_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/nats"
)

// TestRunTestNATS proves the helper hands back a connected connection, and that
// its container is gone once the test ends.
func TestRunTestNATS(t *testing.T) {
	t.Parallel()

	conn := nats.RunTestNATS(t)

	require.True(t, conn.IsConnected())
	require.NoError(t, conn.Flush())
}
