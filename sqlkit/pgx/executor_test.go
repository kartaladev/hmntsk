package pgxexec_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/sqlkit"
	pgxexec "github.com/kartaladev/hmntsk/sqlkit/pgx"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
)

// openPool returns a pgx pool for a DSN, closed when the test that opened it
// ends.
func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse the PostgreSQL connection string")

	cfg.MaxConns = 16

	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err, "open a pgx pool")

	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	var pingErr error

	for range 60 {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("reach the PostgreSQL database: %s", ctx.Err())
		case <-time.After(time.Second):
		}
	}

	require.NoError(t, pingErr, "reach the PostgreSQL database")

	return pool
}

func TestExecutorOnPostgres(t *testing.T) {
	t.Parallel()

	pool := openPool(t, sqlkittest.RunTestPostgres(t))

	sqlkittest.RunExecutorSuite(t, func(t *testing.T) sqlkittest.Harness {
		t.Helper()

		executor, err := pgxexec.New(pool)
		require.NoError(t, err)

		return sqlkittest.Harness{
			Executor: executor,
			CallerTx: func(ctx context.Context) (context.Context, func(bool) error) {
				tx, err := pool.Begin(ctx)
				require.NoError(t, err)

				return pgxexec.ContextWithTx(ctx, tx), func(commit bool) error {
					done := context.WithoutCancel(ctx)
					if commit {
						return tx.Commit(done)
					}

					return tx.Rollback(done)
				}
			},
		}
	})
}

func TestNew(t *testing.T) {
	t.Parallel()

	// Nothing connects: pgxpool dials lazily, and the constructor must not.
	pool, err := pgxpool.New(t.Context(), "postgres://sqlkit:sqlkit@127.0.0.1:1/sqlkit?connect_timeout=1")
	require.NoError(t, err)

	t.Cleanup(pool.Close)

	type testCase struct {
		name   string
		pool   *pgxpool.Pool
		assert func(t *testing.T, executor *pgxexec.Executor, err error)
	}

	cases := []testCase{
		{
			name: "a pool makes a PostgreSQL executor", pool: pool,
			assert: func(t *testing.T, executor *pgxexec.Executor, err error) {
				require.NoError(t, err)
				assert.Equal(t, sqlkit.PostgreSQL, executor.Dialect(), "pgx speaks only PostgreSQL")
				assert.Same(t, pool, executor.Pool())
			},
		},
		{
			name: "a nil pool is a configuration error", pool: nil,
			assert: func(t *testing.T, executor *pgxexec.Executor, err error) {
				require.ErrorIs(t, err, sqlkit.ErrConfiguration)
				assert.Nil(t, executor)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor, err := pgxexec.New(tc.pool)
			tc.assert(t, executor, err)
		})
	}
}
