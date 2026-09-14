package hmntsk_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDocumentedReleaseOrderMatchesTheTooling keeps docs/releasing.md and the
// Makefile from drifting apart.
//
// A release order that is documented in one place and implemented in another is
// a release order that will be wrong the first time a module is added, and the
// failure mode — tagging a module before something it requires — is discovered
// by whoever tries to `go get` it.
func TestDocumentedReleaseOrderMatchesTheTooling(t *testing.T) {
	t.Parallel()

	documented := documentedReleaseOrder(t)
	tooling := toolingReleaseOrder(t)

	assert.Equal(t, documented, tooling,
		"docs/releasing.md and `make release-order` disagree about the release order")

	require.NotEmpty(t, documented)
	assert.Equal(t, ".", documented[0], "the core module is released first")
	assert.Contains(t, documented, "store/sql")
	assert.Less(t, indexOf(documented, "store/sqlcore"), indexOf(documented, "store/sql"),
		"a module cannot be tagged before something it requires")
	assert.Less(t, indexOf(documented, "transport/core"), indexOf(documented, "transport/fiber"))
	assert.Less(t, indexOf(documented, "storetest"), indexOf(documented, "store/pgx"))
}

// documentedReleaseOrder reads the numbered list out of docs/releasing.md.
func documentedReleaseOrder(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile("docs/releasing.md")
	require.NoError(t, err)

	var (
		order  []string
		inList bool
	)

	for line := range strings.Lines(string(raw)) {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "```") && !inList && len(order) == 0:
			inList = true
		case strings.HasPrefix(trimmed, "```") && inList:
			inList = false
		case inList && trimmed != "":
			fields := strings.Fields(trimmed)
			require.GreaterOrEqualf(t, len(fields), 2, "unreadable release-order line: %q", trimmed)
			order = append(order, fields[1])
		}
	}

	return order
}

// toolingReleaseOrder reads the order the Makefile prints.
func toolingReleaseOrder(t *testing.T) []string {
	t.Helper()

	// --no-print-directory, and a cleared MAKELEVEL/MAKEFLAGS, because this runs
	// as a sub-make whenever the suite itself was started by make. GNU make
	// announces "Entering directory ..." on stdout for a sub-make, and those
	// banners would be parsed here as module names. It passes at make level 0
	// and fails under `make test`, which is the kind of difference that is only
	// ever found in CI.
	cmd := exec.CommandContext(t.Context(), "make", "--no-print-directory", "release-order")
	cmd.Env = append(os.Environ(), "MAKELEVEL=", "MAKEFLAGS=")

	out, err := cmd.Output()
	require.NoError(t, err, "make release-order must work; it is how a release is driven")

	var order []string

	for line := range strings.Lines(string(out)) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			order = append(order, trimmed)
		}
	}

	return order
}

// indexOf returns where a module sits in an order.
func indexOf(order []string, module string) int {
	for i, candidate := range order {
		if candidate == module {
			return i
		}
	}

	return -1
}

// TestReleaseOrderCoversEveryModule asserts that nothing is left untagged.
func TestReleaseOrderCoversEveryModule(t *testing.T) {
	t.Parallel()

	order := toolingReleaseOrder(t)

	modules := []string{
		".", "store/sqlcore", "store/sql", "store/pgx", "store/gorm",
		"transport/core", "transport/http", "transport/gin", "transport/fiber",
		"storetest", "transporttest", "relaytest",
		"delivery/webhook", "delivery/redis", "delivery/nats",
		"tasknotify",
	}

	for _, module := range modules {
		assert.GreaterOrEqualf(t, indexOf(order, module), 0,
			"module %s is in the workspace but not in the release order", module)
	}

	assert.Len(t, order, len(modules), "the release order must not name a module that is not there")
}

// TestExamplesAreBuiltButNeverReleased keeps the examples module in the
// workspace, so every change builds and tests it, and out of the release, so
// nobody ever requires documentation as a dependency.
func TestExamplesAreBuiltButNeverReleased(t *testing.T) {
	t.Parallel()

	assert.Negative(t, indexOf(toolingReleaseOrder(t), "examples"),
		"the examples module must never be tagged")

	// Asking the go command, rather than reading go.work's bytes, survives
	// `go work use` and reformatting.
	inWorkspace := exec.CommandContext(t.Context(), "go", "list", "-m", "github.com/kartaladev/hmntsk/examples")
	out, err := inWorkspace.CombinedOutput()
	assert.NoError(t, err, "the examples module must be in the workspace: %s", out)

	releasing, err := os.ReadFile("docs/releasing.md")
	require.NoError(t, err)
	assert.Contains(t, string(releasing), "`examples` is never tagged",
		"docs/releasing.md must say the examples are never tagged")
}
