package sqlstore_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/kartaladev/hmntsk"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
	"github.com/kartaladev/hmntsk/store/sqlcore"
	"github.com/kartaladev/hmntsk/storetest"
)

// open returns a pool for a DSN, closed when the test that opened it ends.
func open(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open(driver, dsn)
	require.NoErrorf(t, err, "open a %s pool", driver)

	t.Cleanup(func() { _ = db.Close() })

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(time.Hour)

	// A container that has announced itself can still refuse the first
	// connection for a moment, so the first contact is retried rather than
	// treated as a failure.
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	var pingErr error

	for range 60 {
		if pingErr = db.PingContext(ctx); pingErr == nil {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("reach the %s database: %s", driver, ctx.Err())
		case <-time.After(time.Second):
		}
	}

	require.NoErrorf(t, pingErr, "reach the %s database", driver)

	return db
}

// factory builds a harness per case, each with its own prefixed schema in the
// one database.
//
// A prefix per case is what lets a whole suite share one container, and it
// means every run against a real engine is also a run of the table-prefix
// option — the thing a host configures once on day one and can never change
// afterwards.
func factory(db *sql.DB, dialect sqlcore.Dialect) storetest.Factory {
	return func(t *testing.T) storetest.Harness {
		t.Helper()

		prefix := storetest.NextTablePrefix()
		store := sqlstore.New(db, dialect, sqlstore.WithTablePrefix(prefix))

		require.NoError(t, store.Migrate(t.Context()), "apply the published schema")

		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := store.Builder().Drop(ctx, store); err != nil {
				t.Errorf("drop the test schema: %s", err)
			}
		})

		require.NoError(t, store.VerifySchema(t.Context()),
			"the schema the engine publishes must satisfy the verification the engine runs")

		return storetest.Harness{
			Store: store,
			HostTx: func(ctx context.Context) (context.Context, func(bool) error) {
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)

				return sqlstore.ContextWithTx(ctx, tx), func(commit bool) error {
					if commit {
						return tx.Commit()
					}

					return tx.Rollback()
				}
			},
			Events: func(ctx context.Context) ([]hmntsk.Event, error) {
				return store.Events(ctx, 0)
			},
			SupportsConcurrentTransactions: true,
		}
	}
}

func TestStoreOnSQLite(t *testing.T) {
	t.Parallel()

	db := open(t, "sqlite", storetest.RunTestSQLite(t))

	storetest.RunSuite(t, factory(db, sqlcore.SQLite))
}

func TestStoreOnPostgres(t *testing.T) {
	t.Parallel()

	db := open(t, "postgres", storetest.RunTestPostgres(t))

	storetest.RunSuite(t, factory(db, sqlcore.PostgreSQL))
}

func TestStoreOnMySQL(t *testing.T) {
	t.Parallel()

	db := open(t, "mysql", storetest.RunTestMySQL(t))

	storetest.RunSuite(t, factory(db, sqlcore.MySQL))
}
