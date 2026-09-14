package fibertransport_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fibertransport "github.com/kartaladev/hmntsk/transport/fiber"
	"github.com/kartaladev/hmntsk/transporttest"
)

// TestNotFoundErrorHandlerPassesOtherErrorsOn proves that the handler answers
// only the router's own not-found and method-not-allowed errors, and leaves
// every other error to the host's handling.
func TestNotFoundErrorHandlerPassesOtherErrorsOn(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		path   string
		next   func(seen *error) fiber.ErrorHandler
		assert func(t *testing.T, seen error, got reply)
	}

	// recording is a host error handler that remembers what it was given.
	recording := func(seen *error) fiber.ErrorHandler {
		return func(c fiber.Ctx, err error) error {
			*seen = err

			return c.Status(http.StatusTeapot).SendString("host")
		}
	}

	cases := []testCase{
		{
			name: "another error reaches the host's handler unchanged",
			path: "/broken",
			next: recording,
			assert: func(t *testing.T, seen error, got reply) {
				require.ErrorIs(t, seen, fiber.ErrBadRequest)
				assert.Equal(t, http.StatusTeapot, got.status)
				assert.Equal(t, "host", string(got.body))
			},
		},
		{
			name: "a nil next falls back to Fiber's default handler",
			path: "/broken",
			next: func(*error) fiber.ErrorHandler { return nil },
			assert: func(t *testing.T, _ error, got reply) {
				assert.Equal(t, http.StatusBadRequest, got.status)
			},
		},
		{
			name: "an unknown route never reaches the host's handler",
			path: "/nothing/here",
			next: recording,
			assert: func(t *testing.T, seen error, got reply) {
				require.NoError(t, seen)
				assert.Equal(t, http.StatusNotFound, got.status)
			},
		},
		{
			name: "a route added after Mount on the host's own app is served",
			path: "/late",
			next: recording,
			assert: func(t *testing.T, seen error, got reply) {
				require.NoError(t, seen)
				assert.Equal(t, http.StatusOK, got.status, "%s", got.body)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var seen error

			app := fiber.New(fiber.Config{ErrorHandler: fibertransport.NotFoundErrorHandler(tc.next(&seen))})
			require.NoError(t, fibertransport.Mount(app, transporttest.NewAPI(t)))

			app.Get("/broken", func(fiber.Ctx) error { return fiber.ErrBadRequest })
			app.Get("/late", func(c fiber.Ctx) error { return c.SendString("late") })

			// Sent first: seen is only filled in by the request.
			got := send(t, app, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, http.NoBody))
			tc.assert(t, seen, got)
		})
	}
}
