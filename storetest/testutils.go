package storetest

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
)

// The container helpers live in sqlkittest, which the task stores and every
// other sqlkit-based store share, so that one wait strategy, one image pin and
// one cleanup discipline serve them all. These names are kept as delegates so
// that no store test changes; they provision the database the engine's suites
// have always used, named hmntsk.

// Pinned images, as sqlkittest pins them.
const (
	// PostgresImage is the PostgreSQL image the suite runs against.
	PostgresImage = sqlkittest.PostgresImage
	// MySQLImage is the MySQL image the suite runs against. It is 8.0 or later
	// because the engine's schema needs the utf8mb4_0900_as_cs collation.
	MySQLImage = sqlkittest.MySQLImage
)

// TestOption varies how a test database is provisioned. It is
// [sqlkittest.TestOption].
type TestOption = sqlkittest.TestOption

// WithImage overrides the pinned image, for testing against another version.
func WithImage(image string) TestOption { return sqlkittest.WithImage(image) }

// WithDatabase overrides the database name, which defaults to hmntsk here.
func WithDatabase(name string) TestOption { return sqlkittest.WithDatabase(name) }

// WithStartupTimeout overrides how long the helper waits for readiness.
func WithStartupTimeout(timeout time.Duration) TestOption {
	return sqlkittest.WithStartupTimeout(timeout)
}

// engineDatabase puts the engine's own database name ahead of the caller's
// options, which may still override it.
func engineDatabase(opts []TestOption) []TestOption {
	return append([]TestOption{sqlkittest.WithDatabase("hmntsk")}, opts...)
}

// RunTestPostgres starts a PostgreSQL container and returns a DSN for it. It is
// [sqlkittest.RunTestPostgres] with the engine's database name.
func RunTestPostgres(t *testing.T, opts ...TestOption) string {
	t.Helper()

	return sqlkittest.RunTestPostgres(t, engineDatabase(opts)...)
}

// RunTestMySQL starts a MySQL container and returns a DSN for it. It is
// [sqlkittest.RunTestMySQL] with the engine's database name.
func RunTestMySQL(t *testing.T, opts ...TestOption) string {
	t.Helper()

	return sqlkittest.RunTestMySQL(t, engineDatabase(opts)...)
}

// RunTestSQLite returns a DSN for a fresh SQLite database in the test's own
// temporary directory. It is [sqlkittest.RunTestSQLite] with the engine's
// database name.
func RunTestSQLite(t *testing.T, opts ...TestOption) string {
	t.Helper()

	return sqlkittest.RunTestSQLite(t, engineDatabase(opts)...)
}

// prefixes hands out a distinct table prefix per call.
var prefixes atomic.Uint64

// NextTablePrefix returns a table prefix no other caller in this test binary
// will use.
//
// It is how one database serves a whole suite: each case builds the engine's
// schema under its own prefix, so two cases can never see each other's rows,
// and starting a container per case — the suite has more than sixty — never
// arises. It also means every run of the suite against a real engine is a run
// against a prefixed deployment, which is the only way that option gets
// exercised as thoroughly as the default one.
func NextTablePrefix() string {
	return fmt.Sprintf("hmntsk%d_", prefixes.Add(1))
}
