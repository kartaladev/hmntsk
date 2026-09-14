package sqlstore_test

// The database/sql and pgx conformance entry points. The GORM ones run in
// internal/gormtest, as a separate test binary: GORM's SQLite driver registers
// under the same name as the modernc driver database/sql uses here, and two
// registrations of one name panic at start-up.

import (
	"database/sql"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/kartaladev/hmntsk/notify/notifytest"
	"github.com/kartaladev/hmntsk/notify/sqlstore/internal/harness"
	"github.com/kartaladev/hmntsk/sqlkit"
	pgxexec "github.com/kartaladev/hmntsk/sqlkit/pgx"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
)

// openSQL returns a database/sql pool for a DSN, closed when the test ends.
func openSQL(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open(driver, dsn)
	require.NoErrorf(t, err, "open a %s pool", driver)

	t.Cleanup(func() { _ = db.Close() })

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)

	harness.Reach(t, driver, db.PingContext)

	return db
}

// stdsqlExecutor builds a database/sql executor over a pool.
func stdsqlExecutor(t *testing.T, db *sql.DB, dialect sqlkit.Dialect) sqlkit.Executor {
	t.Helper()

	executor, err := stdsqlexec.New(db, dialect)
	require.NoError(t, err)

	return executor
}

// openPgx returns a pgx pool for a DSN, closed when the test ends.
func openPgx(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse the PostgreSQL connection string")

	cfg.MaxConns = 16

	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err, "open a pgx pool")

	t.Cleanup(pool.Close)

	harness.Reach(t, "pgx", pool.Ping)

	return pool
}

func TestStoreOnStdSQLPostgres(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "postgres", sqlkittest.RunTestPostgres(t))

	executor := stdsqlExecutor(t, db, sqlkit.PostgreSQL)

	notifytest.Run(t, harness.Factory(executor))
	t.Run("email", func(t *testing.T) { notifytest.RunEmail(t, harness.EmailFactory(executor)) })
	t.Run("email-dispatch", func(t *testing.T) { notifytest.RunEmailDispatch(t, harness.EmailFactory(executor)) })
}

func TestStoreOnStdSQLMySQL(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "mysql", sqlkittest.RunTestMySQL(t))

	executor := stdsqlExecutor(t, db, sqlkit.MySQL)

	notifytest.Run(t, harness.Factory(executor))
	t.Run("email", func(t *testing.T) { notifytest.RunEmail(t, harness.EmailFactory(executor)) })
	t.Run("email-dispatch", func(t *testing.T) { notifytest.RunEmailDispatch(t, harness.EmailFactory(executor)) })
}

func TestStoreOnStdSQLSQLite(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "sqlite", sqlkittest.RunTestSQLite(t))

	executor := stdsqlExecutor(t, db, sqlkit.SQLite)

	notifytest.Run(t, harness.Factory(executor))
	t.Run("email", func(t *testing.T) { notifytest.RunEmail(t, harness.EmailFactory(executor)) })
	t.Run("email-dispatch", func(t *testing.T) { notifytest.RunEmailDispatch(t, harness.EmailFactory(executor)) })
}

func TestStoreOnPgxPostgres(t *testing.T) {
	t.Parallel()

	pool := openPgx(t, sqlkittest.RunTestPostgres(t))

	executor, err := pgxexec.New(pool)
	require.NoError(t, err)

	notifytest.Run(t, harness.Factory(executor))
	t.Run("email", func(t *testing.T) { notifytest.RunEmail(t, harness.EmailFactory(executor)) })
	t.Run("email-dispatch", func(t *testing.T) { notifytest.RunEmailDispatch(t, harness.EmailFactory(executor)) })
}
