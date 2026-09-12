package hmntsk_test

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forbiddenPrefixes lists the import paths the core module must never reach,
// directly or transitively, in a non-test build. Drivers belong to the store/*
// modules and web frameworks to the transport/* modules; the core owns ports
// only. net/http and database/sql are included deliberately: the transport seam
// sits below http.Handler and the repository seam above the driver.
var forbiddenPrefixes = []string{
	"database/sql",
	"net/http",
	"github.com/jackc/pgx",
	"github.com/lib/pq",
	"github.com/go-sql-driver/mysql",
	"github.com/mattn/go-sqlite3",
	"modernc.org/sqlite",
	"gorm.io/",
	"github.com/gin-gonic/gin",
	"github.com/gofiber/fiber",
	"github.com/valyala/fasthttp",
	"github.com/labstack/echo",
	"github.com/go-chi/chi",
	// Test doubles must never reach a consumer's binary.
	"go.uber.org/mock",
	"github.com/gorilla/mux",
}

func TestCoreModuleDependencyDirection(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "./...")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	require.NoErrorf(t, err, "go list -deps failed: %s", stderr.String())

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		dep := strings.TrimSpace(scanner.Text())
		if dep == "" {
			continue
		}

		for _, prefix := range forbiddenPrefixes {
			assert.Falsef(t, matchesPrefix(dep, prefix),
				"core module must not depend on %q (matched forbidden prefix %q)", dep, prefix)
		}
	}

	require.NoError(t, scanner.Err())
}

// matchesPrefix reports whether dep is prefix itself or a package beneath it.
// A trailing slash in prefix means "anything under this path".
func matchesPrefix(dep, prefix string) bool {
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(dep, prefix)
	}

	return dep == prefix || strings.HasPrefix(dep, prefix+"/")
}
