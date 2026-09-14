// Package harness is the glue sqlstore's conformance tests share: reaching a
// freshly started database, and building a store on tables of its own for every
// conformance case.
//
// It imports no database driver. Two SQLite drivers register under the same
// name, so the database/sql entry points and the GORM entry points run as two
// test binaries, and this package is what both of them build on. The containers
// themselves come from sqlkittest, reused rather than rewritten.
package harness

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/notifytest"
	"github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// ReachTimeout bounds how long a test waits for a container that has announced
// itself to accept its first connection.
const ReachTimeout = 60 * time.Second

// Reach retries a first contact until it succeeds or ReachTimeout passes,
// because a container that has announced itself can still refuse its first
// connection for a moment.
func Reach(t *testing.T, what string, ping func(ctx context.Context) error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), ReachTimeout)
	defer cancel()

	var err error

	for range int(ReachTimeout / time.Second) {
		if err = ping(ctx); err == nil {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("reach the %s database: %s", what, ctx.Err())
		case <-time.After(time.Second):
		}
	}

	require.NoErrorf(t, err, "reach the %s database", what)
}

// prefixes numbers the tables each store migrates, so that cases sharing one
// database never see each other's rows.
var prefixes atomic.Int64

// NewStore builds a store over an executor on freshly migrated tables with a
// prefix of their own, dropped when the test ends.
func NewStore(t *testing.T, executor sqlkit.Executor) *sqlstore.Store {
	t.Helper()

	prefix := fmt.Sprintf("t%d_", prefixes.Add(1))

	store, err := sqlstore.New(executor, sqlstore.WithTablePrefix(prefix))
	require.NoError(t, err)

	require.NoError(t, store.Migrate(t.Context()), "migrate the %s schema", executor.Dialect().Name())

	t.Cleanup(func() {
		// t.Context is already cancelled when cleanup runs.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		execer, ok := executor.(sqlkit.Execer)
		if !ok {
			return
		}

		if err := sqlkit.DropTables(ctx, execer, executor.Dialect(), store.Tables()); err != nil {
			t.Errorf("drop the %s tables: %s", prefix, err)
		}
	})

	return store
}

// Factory returns a conformance factory building a store over an executor, on
// freshly migrated tables for every case.
func Factory(executor sqlkit.Executor) notifytest.Factory {
	return func(t *testing.T) notify.Store {
		t.Helper()

		return NewStore(t, executor)
	}
}

// NewEmailStore builds a store like [NewStore] whose email delivery table is
// migrated too, and dropped when the test ends.
func NewEmailStore(t *testing.T, executor sqlkit.Executor) *sqlstore.Store {
	t.Helper()

	store := NewStore(t, executor)
	require.NoError(t, store.MigrateEmail(t.Context()), "migrate the %s email schema", executor.Dialect().Name())

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		execer, ok := executor.(sqlkit.Execer)
		if !ok {
			return
		}

		if err := sqlkit.DropTables(ctx, execer, executor.Dialect(), store.EmailTables()); err != nil {
			t.Errorf("drop the email tables: %s", err)
		}
	})

	return store
}

// EmailFactory returns an email conformance factory building a store over an
// executor, on freshly migrated tables for every case.
func EmailFactory(executor sqlkit.Executor) notifytest.EmailFactory {
	return func(t *testing.T) notifytest.EmailStore {
		t.Helper()

		return NewEmailStore(t, executor)
	}
}
