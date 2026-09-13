package notify_test

import (
	"testing"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/notifytest"
)

func TestMemoryStoreConformance(t *testing.T) {
	t.Parallel()

	notifytest.Run(t, func(*testing.T) notify.Store { return notify.NewMemoryStore() })
}
