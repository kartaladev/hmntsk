package gintransport_test

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
	gintransport "github.com/kartaladev/hmntsk/transport/gin"
	"github.com/kartaladev/hmntsk/transporttest"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// TestBindingPassesTheSharedSuite runs the shared transport suite against the
// gin binding. Passing it is the claim that a client cannot tell this binding
// from the others.
func TestBindingPassesTheSharedSuite(t *testing.T) {
	t.Parallel()

	transporttest.RunSuite(t, mount)
}

// mount serves the contract on a real listener, behind the middleware that
// stands in for the host's own.
func mount(t *testing.T, api *transportcore.API) transporttest.Binding {
	t.Helper()

	// The middleware is registered before the routes, because gin applies only
	// the middleware a route was registered under.
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(gintransport.ActorKey, c.GetHeader(transporttest.ActorHeader))
		c.Next()
	})

	require.NoError(t, gintransport.Mount(engine, api))

	engine.NoRoute(gintransport.NotFoundHandler())
	engine.NoMethod(gintransport.NotFoundHandler())

	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	return transporttest.Binding{BaseURL: server.URL}
}
