package gormstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/kartaladev/hmntsk"
	gormstore "github.com/kartaladev/hmntsk/store/gorm"
	"github.com/kartaladev/hmntsk/store/sqlcore"
	"github.com/kartaladev/hmntsk/storetest"
)

var errCaseFailure = errCase{}

// errCase is the failure the nesting test injects.
type errCase struct{}

func (errCase) Error() string { return "gormstore: injected failure" }

// openGORM returns a GORM handle for a DSN, closed when the test ends.
func openGORM(t *testing.T, dialector gorm.Dialector) *gorm.DB {
	t.Helper()

	var (
		db  *gorm.DB
		err error
	)

	deadline := time.Now().Add(60 * time.Second)

	for {
		db, err = gorm.Open(dialector, &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
			// The engine owns its own transactions; GORM must not wrap each
			// statement in one of its own.
			SkipDefaultTransaction: true,
		})
		if err == nil || time.Now().After(deadline) {
			break
		}

		time.Sleep(time.Second)
	}

	require.NoError(t, err, "open a GORM handle")

	pool, err := db.DB()
	require.NoError(t, err)

	pool.SetMaxOpenConns(16)
	pool.SetMaxIdleConns(16)

	t.Cleanup(func() { _ = pool.Close() })

	require.NoError(t, pool.PingContext(t.Context()))

	return db
}

// factory builds a harness per case, each with its own prefixed schema.
func factory(db *gorm.DB, dialect sqlcore.Dialect) storetest.Factory {
	return func(t *testing.T) storetest.Harness {
		t.Helper()

		store := gormstore.New(db, dialect,
			gormstore.WithTablePrefix(storetest.NextTablePrefix()))

		require.NoError(t, store.Migrate(t.Context()), "apply the published schema")

		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := store.Builder().Drop(ctx, store); err != nil {
				t.Errorf("drop the test schema: %s", err)
			}
		})

		require.NoError(t, store.VerifySchema(t.Context()))

		return storetest.Harness{
			Store: store,
			HostTx: func(ctx context.Context) (context.Context, func(bool) error) {
				tx := db.WithContext(ctx).Begin()
				require.NoError(t, tx.Error)

				return gormstore.ContextWithTx(ctx, tx), func(commit bool) error {
					if commit {
						return tx.Commit().Error
					}

					return tx.Rollback().Error
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

	db := openGORM(t, sqlite.Open(storetest.RunTestSQLite(t)))

	storetest.RunSuite(t, factory(db, sqlcore.SQLite))
}

func TestStoreOnPostgres(t *testing.T) {
	t.Parallel()

	db := openGORM(t, postgres.Open(storetest.RunTestPostgres(t)))

	storetest.RunSuite(t, factory(db, sqlcore.PostgreSQL))
}

func TestStoreOnMySQL(t *testing.T) {
	t.Parallel()

	db := openGORM(t, mysql.Open(storetest.RunTestMySQL(t)))

	storetest.RunSuite(t, factory(db, sqlcore.MySQL))
}

// TestNestedScopesFlattenRatherThanNest is the case GORM's default would fail.
//
// db.Transaction nests with a SAVEPOINT, so an inner scope that fails rolls
// back only to the savepoint and leaves the outer transaction alive and
// committable. The engine's contract is the opposite, and every other driver
// behaves the opposite way, so this asserts that the adapter's Do flattens: an
// inner failure must take the outer write with it, even when the outer scope
// catches the error and returns cleanly afterwards.
func TestNestedScopesFlattenRatherThanNest(t *testing.T) {
	t.Parallel()

	db := openGORM(t, sqlite.Open(storetest.RunTestSQLite(t)))

	store := gormstore.New(db, sqlcore.SQLite,
		gormstore.WithTablePrefix(storetest.NextTablePrefix()))
	require.NoError(t, store.Migrate(t.Context()))

	outer := hmntsk.TaskID("019243af-9f1c-7000-8000-000000000001")
	inner := hmntsk.TaskID("019243af-9f1c-7000-8000-000000000002")

	err := store.Do(t.Context(), func(ctx context.Context) error {
		if err := store.Create(ctx, storetest.NewTask(outer)); err != nil {
			return err
		}

		innerErr := store.Do(ctx, func(ctx context.Context) error {
			if err := store.Create(ctx, storetest.NewTask(inner)); err != nil {
				return err
			}

			return errCaseFailure
		})
		require.ErrorIs(t, innerErr, errCaseFailure)

		// Under GORM's default the inner scope would have released a savepoint
		// on its way out, discarding its own write and leaving this scope alive
		// and committable. Joining leaves both writes in one undecided
		// transaction.
		_, getErr := store.Get(ctx, inner)
		assert.NoError(t, getErr, "db.Transaction's savepoint would have discarded this write")

		return innerErr
	})
	require.ErrorIs(t, err, errCaseFailure)

	for _, id := range []hmntsk.TaskID{outer, inner} {
		_, getErr := store.Get(t.Context(), id)
		assert.ErrorIsf(t, getErr, hmntsk.ErrNotFound,
			"a savepoint would have let %s survive its sibling's failure", id)
	}
}

// TestAdapterDeclaresNoModelsAndRunsNoAutoMigrate pins the other half of the
// GORM decision: the schema comes from the published DDL, not from GORM.
func TestAdapterDeclaresNoModelsAndRunsNoAutoMigrate(t *testing.T) {
	t.Parallel()

	db := openGORM(t, sqlite.Open(storetest.RunTestSQLite(t)))

	prefix := storetest.NextTablePrefix()
	store := gormstore.New(db, sqlcore.SQLite, gormstore.WithTablePrefix(prefix))

	require.NoError(t, store.Migrate(t.Context()))

	// GORM's own migrator, asked about the engine's tables, must find exactly
	// what the published DDL created — which is only possible because nothing
	// here ever handed GORM a model to infer a schema from.
	for _, table := range store.Builder().Tables() {
		assert.Truef(t, db.Migrator().HasTable(table), "%s must exist", table)
	}

	assert.NoError(t, store.VerifySchema(t.Context()))
}
