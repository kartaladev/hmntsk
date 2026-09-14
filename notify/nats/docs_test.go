package nats_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/nats"
)

// TestTheOperationsGuideMatchesTheBroadcaster keeps
// notify/docs/realtime-operations.md from drifting away from the NATS
// broadcaster's defaults.
func TestTheOperationsGuideMatchesTheBroadcaster(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../docs/realtime-operations.md")
	require.NoError(t, err)

	document := string(raw)

	for _, stated := range []string{
		"| NATS subject | `" + nats.DefaultSubject + "` |",
		"| Signals per message | " + strconv.Itoa(nats.MaxSignalsPerMessage) + " |",
		"no queue group",
		"`delivery/nats`",
	} {
		assert.Containsf(t, document, stated, "the guide states %q", stated)
	}
}
