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

// TestAppPassesTheSharedSuite runs the shared suite against the application
// [fibertransport.App] builds, rather than one the harness assembles with Mount.
//
// The two answer unknown routes by different means — App through the
// application's error handler, the Mount harness through a trailing catch-all —
// and a client must not be able to tell them apart either.
func TestAppPassesTheSharedSuite(t *testing.T) {
	t.Parallel()

	transporttest.RunSuite(t, mountApp)
}

// TestRequestValuesOutliveTheirRequest guards against fasthttp's buffer reuse.
//
// Fiber hands out strings and byte slices that point into buffers fasthttp
// reuses for the next request. The engine keeps what it is given — who created
// a task, its payload — long after the handler returns, so a binder passing
// those values on uncopied lets the next caller overwrite a stored creator with
// their own name. A same-length actor is what makes that visible.
func TestRequestValuesOutliveTheirRequest(t *testing.T) {
	t.Parallel()

	client, api := transporttest.NewClient(t, mount)

	task := client.CreateApproval(t)

	// Any later request by an actor whose name is as long as the creator's.
	require.Len(t, transporttest.Carol, len(transporttest.Owner))
	client.Do(t, "GET", "/tasks/count?candidate=me", transporttest.Carol, nil)

	stored, err := api.Service().Get(t.Context(), task.ID)
	require.NoError(t, err)

	require.Equal(t, transporttest.Owner, stored.CreatedBy,
		"the creator the engine stored must not change when the next request arrives")
	require.JSONEq(t, `{"amount":100,"justification":"new laptop"}`, string(stored.Input),
		"the payload the engine stored must not change either")
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

	return serve(t, app)
}

// mountApp serves the application App builds, reading the actor from the
// suite's header through the binding's own actor rule.
func mountApp(t *testing.T, api *transportcore.API) transporttest.Binding {
	t.Helper()

	app, err := fibertransport.App(api, fibertransport.WithActorFunc(func(c fiber.Ctx) string {
		return c.Get(transporttest.ActorHeader)
	}))
	require.NoError(t, err)

	return serve(t, app)
}

// serve starts an app on a real listener and returns where it is reachable.
func serve(t *testing.T, app *fiber.App) transporttest.Binding {
	t.Helper()

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
