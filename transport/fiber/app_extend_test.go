package fibertransport_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fibertransport "github.com/kartaladev/hmntsk/transport/fiber"
	"github.com/kartaladev/hmntsk/transporttest"
)

// TestAppServesRoutesAddedAfterConstruction proves that the application App
// returns stays open to the host. Unknown paths and methods are the shared
// suite's to check, through TestAppPassesTheSharedSuite.
func TestAppServesRoutesAddedAfterConstruction(t *testing.T) {
	t.Parallel()

	app, err := fibertransport.App(transporttest.NewAPI(t))
	require.NoError(t, err)

	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })

	got := send(t, app, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))
	require.Equal(t, http.StatusOK, got.status, "%s", got.body)
	assert.Equal(t, "ok", string(got.body))
}

// reply is one in-process answer, read whole.
type reply struct {
	status int
	body   []byte
}

// send runs one request through the app in process and reads the whole answer.
func send(t *testing.T, app *fiber.App, request *http.Request) reply {
	t.Helper()

	response, err := app.Test(request)
	require.NoError(t, err)

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return reply{status: response.StatusCode, body: body}
}
