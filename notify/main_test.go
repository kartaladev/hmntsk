package notify_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when any test leaves a goroutine running: the hub,
// the pruner loop and every stream must stop when their context does.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
