package websocket_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when any test leaves a goroutine running: every
// connection's read and write loops must stop when the connection or its
// server does.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
