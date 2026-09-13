package gormexec_test

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

	"github.com/kartaladev/hmntsk/sqlkit"
	gormexec "github.com/kartaladev/hmntsk/sqlkit/gorm"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
)

// openGORM returns a GORM handle for a dialector, closed when the test ends.
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
			// The executor owns its transactions; GORM must not wrap each
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

// factory builds a harness over one GORM handle.
func factory(db *gorm.DB, dialect sqlkit.Dialect) sqlkittest.Factory {
	return func(t *testing.T) sqlkittest.Harness {
		t.Helper()

		executor, err := gormexec.New(db, dialect)
		require.NoError(t, err)

		return sqlkittest.Harness{
			Executor: executor,
			CallerTx: func(ctx context.Context) (context.Context, func(bool) error) {
				tx := db.WithContext(ctx).Begin()
				require.NoError(t, tx.Error)

				return gormexec.ContextWithTx(ctx, tx), func(commit bool) error {
					if commit {
						return tx.Commit().Error
					}

					return tx.Rollback().Error
				}
			},
		}
	}
}

func TestExecutorOnSQLite(t *testing.T) {
	t.Parallel()

	db := openGORM(t, sqlite.Open(sqlkittest.RunTestSQLite(t)))

	sqlkittest.RunExecutorSuite(t, factory(db, sqlkit.SQLite))
}

func TestExecutorOnPostgres(t *testing.T) {
	t.Parallel()

	db := openGORM(t, postgres.Open(sqlkittest.RunTestPostgres(t)))

	sqlkittest.RunExecutorSuite(t, factory(db, sqlkit.PostgreSQL))
}

func TestExecutorOnMySQL(t *testing.T) {
	t.Parallel()

	db := openGORM(t, mysql.Open(sqlkittest.RunTestMySQL(t)))

	sqlkittest.RunExecutorSuite(t, factory(db, sqlkit.MySQL))
}

func TestNew(t *testing.T) {
	t.Parallel()

	db := openGORM(t, sqlite.Open(sqlkittest.RunTestSQLite(t)))

	type testCase struct {
		name    string
		db      *gorm.DB
		dialect sqlkit.Dialect
		assert  func(t *testing.T, executor *gormexec.Executor, err error)
	}

	cases := []testCase{
		{
			name: "the dialect reports GORM's own bind marker on every database", db: db, dialect: sqlkit.PostgreSQL,
			assert: func(t *testing.T, executor *gormexec.Executor, err error) {
				require.NoError(t, err)
				assert.Equal(t, "postgres", executor.Dialect().Name())
				assert.Equal(t, "?", executor.Dialect().Placeholder(2),
					"GORM rewrites ? for the dialector; a statement carrying $2 would reach the server unbound")
				assert.Equal(t, sqlkit.PostgreSQL.Quote("type"), executor.Dialect().Quote("type"),
					"only the bind marker differs")
				assert.Same(t, db, executor.DB())
			},
		},
		{
			name: "a nil handle is a configuration error", db: nil, dialect: sqlkit.SQLite,
			assert: func(t *testing.T, executor *gormexec.Executor, err error) {
				require.ErrorIs(t, err, sqlkit.ErrConfiguration)
				assert.Nil(t, executor)
			},
		},
		{
			name: "a nil dialect is a configuration error", db: db, dialect: nil,
			assert: func(t *testing.T, executor *gormexec.Executor, err error) {
				require.ErrorIs(t, err, sqlkit.ErrConfiguration)
				assert.Nil(t, executor)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor, err := gormexec.New(tc.db, tc.dialect)
			tc.assert(t, executor, err)
		})
	}
}
