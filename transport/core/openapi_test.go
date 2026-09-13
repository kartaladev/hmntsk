package transportcore_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// update rewrites the committed OpenAPI document instead of comparing against
// it: go test ./transport/core -run TestOpenAPIDocument -update
var update = flag.Bool("update", false, "rewrite the committed OpenAPI document")

// openAPIPath is where the generated document lives.
const openAPIPath = "openapi.json"

// newAPI wires a contract over an engine with two registered types.
func newAPI(t *testing.T, opts ...transportcore.Option) *transportcore.API {
	t.Helper()

	registry := hmntsk.NewRegistry()
	require.NoError(t, registry.Register(hmntsk.TypeSpec{
		Name:         "approval",
		Title:        "Approval",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"amount":{"type":"number"}},"required":["amount"]}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"approved":{"type":"boolean"}},"required":["approved"]}`),
	}))

	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithRegistry(registry),
		hmntsk.WithGroupResolver(hmntsk.NewStaticAssignment(map[string][]string{
			"finance-approvers": {"alice", "bob"},
		})),
	)
	require.NoError(t, err)

	api, err := transportcore.New(svc, opts...)
	require.NoError(t, err)

	return api
}

// TestOpenAPIDocumentIsGeneratedAndCommitted keeps the published document and
// the route table in step.
//
// The document is generated from the table, so it cannot describe a route that
// does not exist; committing it and failing when it drifts is what stops it
// describing a route that no longer does. CI runs this, so a change to the
// contract that forgets the document does not merge.
func TestOpenAPIDocumentIsGeneratedAndCommitted(t *testing.T) {
	generated, err := newAPI(t).OpenAPI()
	require.NoError(t, err)

	if *update {
		require.NoError(t, os.WriteFile(openAPIPath, generated, 0o600))
		t.Log("rewrote " + openAPIPath)

		return
	}

	committed, err := os.ReadFile(openAPIPath)
	require.NoErrorf(t, err, "the generated document must be committed; run with -update")

	assert.Equal(t, string(committed), string(generated),
		"the committed OpenAPI document has drifted from the route table; "+
			"regenerate it with: go test ./transport/core -run TestOpenAPIDocument -update")
}

func TestOpenAPIDocumentDescribesEveryRoute(t *testing.T) {
	t.Parallel()

	api := newAPI(t)

	raw, err := api.OpenAPI()
	require.NoError(t, err)

	var document struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
		Comps   struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}

	require.NoError(t, json.Unmarshal(raw, &document))
	assert.Equal(t, transportcore.OpenAPIVersion, document.OpenAPI)

	operations := 0

	for _, route := range api.Routes() {
		item, ok := document.Paths[route.Pattern]
		require.Truef(t, ok, "route %s is not in the document", route.Pattern)

		_, ok = item[lower(route.Method)]
		assert.Truef(t, ok, "%s %s is not in the document", route.Method, route.Pattern)
	}

	for _, item := range document.Paths {
		operations += len(item)
	}

	assert.Len(t, api.Routes(), operations,
		"the document must describe exactly the routes the table declares, no more")

	for _, schema := range []string{
		"CreateTaskRequest", "OperationRequest", "Task", "PageResponse", "CountResponse",
		"HistoryResponse", "TaskTypeResponse", "TaskTypeListResponse", "ErrorResponse",
	} {
		assert.Containsf(t, document.Comps.Schemas, schema, "schema %s is missing", schema)
	}
}

func TestOpenAPIDocumentFollowsTheBasePath(t *testing.T) {
	t.Parallel()

	api := newAPI(t, transportcore.WithBasePath("/api/v1"))

	raw, err := api.OpenAPI()
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"/api/v1/tasks/{id}/claim"`)
	assert.NotContains(t, string(raw), `"/v1/tasks/{id}/claim"`)
}

// lower is strings.ToLower without the import, for one call site.
func lower(value string) string {
	out := []byte(value)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}

	return string(out)
}
