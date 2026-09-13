package stdsqlexec_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/kartaladev/hmntsk/sqlkit"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
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

// factory builds a harness over one pool.
func factory(db *sql.DB, dialect sqlkit.Dialect) sqlkittest.Factory {
	return func(t *testing.T) sqlkittest.Harness {
		t.Helper()

		executor, err := stdsqlexec.New(db, dialect)
		require.NoError(t, err)

		return sqlkittest.Harness{
			Executor: executor,
			CallerTx: func(ctx context.Context) (context.Context, func(bool) error) {
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)

				return stdsqlexec.ContextWithTx(ctx, tx), func(commit bool) error {
					if commit {
						return tx.Commit()
					}

					return tx.Rollback()
				}
			},
		}
	}
}

func TestExecutorOnSQLite(t *testing.T) {
	t.Parallel()

	db := open(t, "sqlite", sqlkittest.RunTestSQLite(t))

	sqlkittest.RunExecutorSuite(t, factory(db, sqlkit.SQLite))
}

func TestExecutorOnPostgres(t *testing.T) {
	t.Parallel()

	db := open(t, "postgres", sqlkittest.RunTestPostgres(t))

	sqlkittest.RunExecutorSuite(t, factory(db, sqlkit.PostgreSQL))
}

func TestExecutorOnMySQL(t *testing.T) {
	t.Parallel()

	db := open(t, "mysql", sqlkittest.RunTestMySQL(t))

	sqlkittest.RunExecutorSuite(t, factory(db, sqlkit.MySQL))
}

func TestNew(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", "file::memory:")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	type testCase struct {
		name    string
		db      *sql.DB
		dialect sqlkit.Dialect
		assert  func(t *testing.T, executor *stdsqlexec.Executor, err error)
	}

	cases := []testCase{
		{
			name: "a handle and a dialect make an executor", db: db, dialect: sqlkit.SQLite,
			assert: func(t *testing.T, executor *stdsqlexec.Executor, err error) {
				require.NoError(t, err)
				assert.Equal(t, sqlkit.SQLite, executor.Dialect())
				assert.Same(t, db, executor.DB())
			},
		},
		{
			name: "a nil handle is a configuration error", db: nil, dialect: sqlkit.SQLite,
			assert: func(t *testing.T, executor *stdsqlexec.Executor, err error) {
				require.ErrorIs(t, err, sqlkit.ErrConfiguration)
				assert.Nil(t, executor)
			},
		},
		{
			name: "a nil dialect is a configuration error", db: db, dialect: nil,
			assert: func(t *testing.T, executor *stdsqlexec.Executor, err error) {
				require.ErrorIs(t, err, sqlkit.ErrConfiguration)
				assert.Nil(t, executor)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor, err := stdsqlexec.New(tc.db, tc.dialect)
			tc.assert(t, executor, err)
		})
	}
}
