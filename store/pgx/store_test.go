package pgxstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	pgxstore "github.com/kartaladev/hmntsk/store/pgx"
	"github.com/kartaladev/hmntsk/storetest"
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

// newStore builds a store over its own prefixed schema, migrated, verified and
// dropped when the case ends.
//
// It is separate from the harness so that the relay suite can build a store the
// same way — change the prefix scheme, the drop timeout or the verification and
// both suites move together, rather than one of them silently running against a
// setup the other stopped using.
//
// A prefix per case is what lets a whole suite share one container, and it
// means every run against a real engine is also a run of the table-prefix
// option — the thing a host configures once on day one and can never change
// afterwards.
func newStore(t *testing.T, pool *pgxpool.Pool) *pgxstore.Store {
	t.Helper()

	store := pgxstore.New(pool, pgxstore.WithTablePrefix(storetest.NextTablePrefix()))

	require.NoError(t, store.Migrate(t.Context()), "apply the published schema")

	t.Cleanup(func() {
		// Not t.Context(): it is already cancelled by the time cleanup runs.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := store.Builder().Drop(ctx, store); err != nil {
			t.Errorf("drop the test schema: %s", err)
		}
	})

	require.NoError(t, store.VerifySchema(t.Context()),
		"the schema the engine publishes must satisfy the verification the engine runs")

	return store
}

func TestStoreOnPostgres(t *testing.T) {
	t.Parallel()

	pool := openPool(t, storetest.RunTestPostgres(t))

	storetest.RunSuite(t, func(t *testing.T) storetest.Harness {
		t.Helper()

		store := newStore(t, pool)

		return storetest.Harness{
			Store: store,
			HostTx: func(ctx context.Context) (context.Context, func(bool) error) {
				tx, err := pool.Begin(ctx)
				require.NoError(t, err)

				return pgxstore.ContextWithTx(ctx, tx), func(commit bool) error {
					done, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()

					if commit {
						return tx.Commit(done)
					}

					return tx.Rollback(done)
				}
			},
			Events: func(ctx context.Context) ([]hmntsk.Event, error) {
				return store.Events(ctx, 0)
			},
			SupportsConcurrentTransactions: true,
		}
	})
}

// TestTxFromContext pins the host-led seam both ways: a transaction the host
// opened is visible to the engine, and one it never opened is not.
func TestTxFromContext(t *testing.T) {
	t.Parallel()

	pool := openPool(t, storetest.RunTestPostgres(t))

	_, found := pgxstore.TxFromContext(t.Context())
	require.False(t, found, "a bare context carries no transaction")

	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)

	defer func() { _ = tx.Rollback(context.Background()) }()

	scoped := pgxstore.ContextWithTx(t.Context(), tx)

	joined, found := pgxstore.TxFromContext(scoped)
	require.True(t, found)
	require.NotNil(t, joined)

	store := pgxstore.New(pool)
	require.True(t, store.InTransaction(scoped))
	require.False(t, store.InTransaction(t.Context()))

	// The context carries a pgx.Tx, not a driver-agnostic handle: the engine
	// joins the host's own transaction, it does not wrap it.
	require.Implements(t, (*pgx.Tx)(nil), joined)
}
