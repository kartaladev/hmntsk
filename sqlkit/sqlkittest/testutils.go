package sqlkittest

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Pinned images. A moving tag would let a remote image update change what a
// conformance suite means overnight, on a Tuesday, for no reason anybody could
// find.
const (
	// PostgresImage is the PostgreSQL image the suites run against.
	PostgresImage = "postgres:17.6-alpine"
	// MySQLImage is the MySQL image the suites run against. It is 8.0 or later
	// because an identifier column needs the utf8mb4_0900_as_cs collation.
	MySQLImage = "mysql:8.4.6"
)

// These helpers return a DSN rather than a connection.
//
// That is a deliberate departure from returning the highest-level client a
// consumer would want, and the reason is the driver matrix: three drivers
// share these two databases, and they want a *sql.DB, a *pgxpool.Pool and a
// *gorm.DB respectively. A DSN is the highest level all three can share, so it
// is what keeps this one helper rather than three.

// testConfig is the provisioning a caller can vary.
type testConfig struct {
	image    string
	database string
	user     string
	password string
	startup  time.Duration
}

// TestOption varies how a test database is provisioned.
type TestOption func(*testConfig)

// WithImage overrides the pinned image, for testing against another version.
func WithImage(image string) TestOption {
	return func(c *testConfig) {
		if image != "" {
			c.image = image
		}
	}
}

// WithDatabase overrides the database name, which defaults to "sqlkit". For
// SQLite it names the database file.
func WithDatabase(name string) TestOption {
	return func(c *testConfig) {
		if name != "" {
			c.database = name
		}
	}
}

// WithStartupTimeout overrides how long the helper waits for readiness.
func WithStartupTimeout(timeout time.Duration) TestOption {
	return func(c *testConfig) {
		if timeout > 0 {
			c.startup = timeout
		}
	}
}

// defaults returns the configuration before options are applied.
func defaults(image string) *testConfig {
	return &testConfig{
		image:    image,
		database: "sqlkit",
		user:     "sqlkit",
		password: "sqlkit",
		startup:  2 * time.Minute,
	}
}

// apply folds the options into a configuration.
func apply(cfg *testConfig, opts []TestOption) *testConfig {
	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// RunTestPostgres starts a PostgreSQL container and returns a DSN for it.
//
// The container lives as long as the test: termination is registered with
// t.Cleanup the instant the container starts, before anything that could fail,
// so a failing migration cannot leak it.
func RunTestPostgres(t *testing.T, opts ...TestOption) string {
	t.Helper()

	cfg := apply(defaults(PostgresImage), opts)

	container, err := postgres.Run(t.Context(), cfg.image,
		postgres.WithDatabase(cfg.database),
		postgres.WithUsername(cfg.user),
		postgres.WithPassword(cfg.password),
		testcontainers.WithWaitStrategy(
			// The readiness line appears once while the image initialises its
			// data directory and once after the real server starts. Only the
			// second one means the database will accept a connection.
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(cfg.startup),
		),
	)
	require.NoError(t, err, "start the PostgreSQL test container")

	terminate(t, container)

	dsn, err := container.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err, "read the PostgreSQL connection string")

	return dsn
}

// RunTestMySQL starts a MySQL container and returns a DSN for it.
//
// The DSN asks for three things a portable store needs and MySQL does not do
// by default:
//
//   - parsed times in UTC, because timestamps are stored as UTC at microsecond
//     precision and a driver left to its own devices hands back a string in
//     whatever zone the session happens to carry;
//   - clientFoundRows, because MySQL otherwise reports how many rows a write
//     changed rather than how many it matched, and a conditional update that
//     matched its row but wrote the same values would look like a conflict;
//   - multiStatements, so a published schema can be applied in one go.
func RunTestMySQL(t *testing.T, opts ...TestOption) string {
	t.Helper()

	cfg := apply(defaults(MySQLImage), opts)

	container, err := mysql.Run(t.Context(), cfg.image,
		mysql.WithDatabase(cfg.database),
		mysql.WithUsername(cfg.user),
		mysql.WithPassword(cfg.password),
		testcontainers.WithWaitStrategy(
			// "ready for connections" alone is a trap: the image's entrypoint
			// starts a throwaway server to initialise the data directory, and
			// that one announces itself too. The line that carries the real
			// port belongs only to the server that will still be there
			// afterwards.
			wait.ForAll(
				wait.ForLog("port: 3306  MySQL Community Server").
					WithStartupTimeout(cfg.startup),
				wait.ForListeningPort("3306/tcp").
					WithStartupTimeout(cfg.startup),
			),
		),
	)
	require.NoError(t, err, "start the MySQL test container")

	terminate(t, container)

	host, err := container.Host(t.Context())
	require.NoError(t, err)

	port, err := container.MappedPort(t.Context(), "3306/tcp")
	require.NoError(t, err)

	return fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?parseTime=true&loc=UTC&time_zone=%%27%%2B00%%3A00%%27"+
			"&clientFoundRows=true&multiStatements=true",
		cfg.user, cfg.password, host, port.Port(), cfg.database)
}

// RunTestSQLite returns a DSN for a fresh SQLite database file, named after the
// database, in the test's own temporary directory.
//
// There is no container: SQLite runs in the process. That is exactly why it is
// in the matrix — it makes a conformance suite cheap enough to run on every
// commit, which is what keeps the other six combinations honest.
//
// Foreign keys are switched on explicitly, because SQLite leaves them off by
// default.
func RunTestSQLite(t *testing.T, opts ...TestOption) string {
	t.Helper()

	cfg := apply(defaults(""), opts)

	path := filepath.Join(t.TempDir(), cfg.database+".db")

	// Write-ahead logging so that a read on one connection is not blocked by an
	// open write transaction on another — a suite has to be able to look at
	// committed state while a caller-led transaction is still open. Immediate
	// transactions so that two writers queue on the busy timeout rather than
	// discovering the conflict at commit.
	return "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_txlock=immediate"
}

// terminate ties a container's life to the test's.
func terminate(t *testing.T, container testcontainers.Container) {
	t.Helper()

	t.Cleanup(func() {
		// Not t.Context(): it is already cancelled by the time cleanup runs,
		// and Terminate would quietly do nothing.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminate the test container: %s", err)
		}
	})
}
