package tasknotify_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	nsql "github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/sqlkit"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
	hsql "github.com/kartaladev/hmntsk/store/sql"
	"github.com/kartaladev/hmntsk/store/sqlcore"
	"github.com/kartaladev/hmntsk/storetest"
)

// These helpers live in a _test.go file on purpose. As a production file they
// would put three database drivers, testcontainers and store/sql into the build
// of every consumer of tasknotify, and only this package's tests need them. The
// containers themselves come from sqlkittest, which owns them.

// backend is one database the end-to-end tests run on.
type backend struct {
	name    string
	driver  string
	dsn     func(t *testing.T, opts ...sqlkittest.TestOption) string
	dialect sqlkit.Dialect
	engine  sqlcore.Dialect
}

// backends are the three dialects, on the database/sql driver each store's own
// tests use.
func backends() []backend {
	return []backend{
		{name: "sqlite", driver: "sqlite", dsn: sqlkittest.RunTestSQLite, dialect: sqlkit.SQLite, engine: sqlcore.SQLite},
		{name: "postgres", driver: "postgres", dsn: sqlkittest.RunTestPostgres, dialect: sqlkit.PostgreSQL, engine: sqlcore.PostgreSQL},
		{name: "mysql", driver: "mysql", dsn: sqlkittest.RunTestMySQL, dialect: sqlkit.MySQL, engine: sqlcore.MySQL},
	}
}

// reachTimeout bounds how long a test waits for a started database to accept
// its first connection.
const reachTimeout = 60 * time.Second

// openDatabase starts a backend's database and opens a pool on it, retrying the
// first contact because a container that has announced itself can still refuse
// a connection for a moment.
func openDatabase(t *testing.T, b backend) *sql.DB {
	t.Helper()

	db, err := sql.Open(b.driver, b.dsn(t))
	require.NoErrorf(t, err, "open a %s pool", b.name)

	t.Cleanup(func() { _ = db.Close() })

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)

	ctx, cancel := context.WithTimeout(t.Context(), reachTimeout)
	defer cancel()

	var pingErr error

	for range int(reachTimeout / time.Second) {
		if pingErr = db.PingContext(ctx); pingErr == nil {
			return db
		}

		select {
		case <-ctx.Done():
			t.Fatalf("reach the %s database: %s", b.name, ctx.Err())
		case <-time.After(time.Second):
		}
	}

	require.NoErrorf(t, pingErr, "reach the %s database", b.name)

	return db
}

// stores is a task store and a notification store sharing one database, each on
// tables of its own.
type stores struct {
	engine *hsql.Store
	notify *nsql.Store
}

// notifyPrefixes numbers the notification tables each case migrates.
var notifyPrefixes atomic.Int64

// newStores migrates a task store and a notification store on db, with prefixes
// of their own, and drops both when the test ends. A whole table test shares one
// database this way without any case seeing another's rows.
func newStores(t *testing.T, b backend, db *sql.DB) stores {
	t.Helper()

	engineStore := hsql.New(db, b.engine, hsql.WithTablePrefix(storetest.NextTablePrefix()))
	require.NoError(t, engineStore.Migrate(t.Context()), "migrate the task schema on %s", b.name)

	executor, err := stdsqlexec.New(db, b.dialect)
	require.NoError(t, err)

	notifyStore, err := nsql.New(executor, nsql.WithTablePrefix(fmt.Sprintf("n%d_", notifyPrefixes.Add(1))))
	require.NoError(t, err)
	require.NoError(t, notifyStore.Migrate(t.Context()), "migrate the notification schema on %s", b.name)

	t.Cleanup(func() {
		// t.Context is already cancelled when cleanup runs.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := engineStore.Builder().Drop(ctx, engineStore); err != nil {
			t.Errorf("drop the task tables on %s: %s", b.name, err)
		}

		if err := sqlkit.DropTables(ctx, executor, b.dialect, notifyStore.Tables()); err != nil {
			t.Errorf("drop the notification tables on %s: %s", b.name, err)
		}
	})

	return stores{engine: engineStore, notify: notifyStore}
}

// TestDatabasesStart is the smoke test for the helpers: every backend starts,
// and both schemas migrate and verify side by side in one database.
func TestDatabasesStart(t *testing.T) {
	t.Parallel()

	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			s := newStores(t, b, openDatabase(t, b))

			require.NoError(t, s.engine.VerifySchema(t.Context()))
			require.NoError(t, s.notify.VerifySchema(t.Context()))
		})
	}
}
