package fibertransport_test

import (
	"net"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
	fibertransport "github.com/kartaladev/hmntsk/transport/fiber"
	"github.com/kartaladev/hmntsk/transporttest"
)

// TestBindingPassesTheSharedSuite runs the shared transport suite against the
// Fiber binding.
//
// This is the binding the seam exists for. Fiber is fasthttp, not net/http, and
// the suite reaches it over a real socket precisely so that passing means what
// it says: a client cannot tell this binding from the standard-library one.
func TestBindingPassesTheSharedSuite(t *testing.T) {
	t.Parallel()

	transporttest.RunSuite(t, mount)
}

// mount serves the contract on a real listener, behind the middleware that
// stands in for the host's own.
func mount(t *testing.T, api *transportcore.API) transporttest.Binding {
	t.Helper()

	app := fiber.New()

	app.Use(func(c fiber.Ctx) error {
		c.Locals(fibertransport.ActorKey, c.Get(transporttest.ActorHeader))

		return c.Next()
	})

	require.NoError(t, fibertransport.Mount(app, api))

	app.Use(fibertransport.NotFoundHandler())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()

	t.Cleanup(func() {
		shutdownErr := app.ShutdownWithTimeout(10 * time.Second)
		if shutdownErr != nil {
			t.Logf("shut down the fiber app: %s", shutdownErr)
		}
	})

	base := "http://" + listener.Addr().String()
	waitUntilReachable(t, listener.Addr().String())

	return transporttest.Binding{BaseURL: base}
}

// waitUntilReachable blocks until the listener answers, so that the first case
// does not race the server's startup.
func waitUntilReachable(t *testing.T, address string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("the fiber app never became reachable at %s", address)
}
