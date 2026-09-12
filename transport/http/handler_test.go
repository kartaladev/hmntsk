package httptransport_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
	httptransport "github.com/kartaladev/hmntsk/transport/http"
	"github.com/kartaladev/hmntsk/transporttest"
)

// TestBindingPassesTheSharedSuite runs the shared transport suite against the
// net/http binding.
func TestBindingPassesTheSharedSuite(t *testing.T) {
	t.Parallel()

	transporttest.RunSuite(t, mount)
}

// mount serves the contract on a real listener.
//
// The middleware here stands in for the host's: it decides who is calling and
// hands that to the binder. In a deployment it would be reading a session or a
// verified token; the engine sees only the answer either way.
func mount(t *testing.T, api *transportcore.API) transporttest.Binding {
	t.Helper()

	handler, err := httptransport.Handler(api)
	require.NoError(t, err)

	server := httptest.NewServer(withActor(handler))
	t.Cleanup(server.Close)

	return transporttest.Binding{BaseURL: server.URL}
}

// withActor is the host middleware the suite expects.
func withActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := r.Header.Get(transporttest.ActorHeader)
		next.ServeHTTP(w, r.WithContext(httptransport.ContextWithActor(r.Context(), actor)))
	})
}
