// Package storetest is the behavioural suite every hmntsk store adapter must
// pass.
//
// Ten adapter-and-dialect combinations exist. Writing their tests
// independently would produce ten copies of the same cases, ten places for a
// divergence to hide, and no way to tell whether a passing adapter passes the
// same bar as the others. One exported suite makes the count affordable: each
// adapter's test file starts its dependency and calls [RunSuite].
//
// The cases here are chosen for where adapters actually diverge, not for
// coverage. Nested scopes joining rather than nesting, because GORM's default
// is the other way. Rollback on a panic with the panic re-raised, because it is
// easy to swallow. Identifier comparison staying case-sensitive, because
// MySQL's default collation is not. Exclusive claiming without a row lock,
// because SQLite has none. An adapter that passes these behaves like the
// others where it matters.
package storetest

import (
	"context"
	"testing"

	"github.com/kartaladev/hmntsk"
)

// Harness is one adapter, ready to exercise. A factory builds a fresh one per
// case, with an empty schema.
type Harness struct {
	// Store is the adapter under test: repository, transactor and event sink
	// as one value.
	Store hmntsk.Store

	// HostTx opens a transaction the way the host's own code would, and returns
	// the scoped context plus a function that commits or rolls it back. It is
	// how the suite exercises the host-led half of the transaction contract:
	// the engine must join this transaction and must neither commit nor roll it
	// back itself.
	HostTx func(ctx context.Context) (scoped context.Context, done func(commit bool) error)

	// Events returns every event durably recorded so far, so that the suite can
	// assert that a rollback left none behind. Ordering is not significant.
	Events func(ctx context.Context) ([]hmntsk.Event, error)

	// SupportsConcurrentTransactions reports whether two transactions may be
	// open at once. An adapter that serialises them — the in-memory store does
	// — still has its conflict semantics checked, by the sequential
	// stale-version cases rather than the racing ones.
	SupportsConcurrentTransactions bool
}

// Factory builds a fresh harness over an empty schema. It is called once per
// case, and may register cleanup with t.
type Factory func(t *testing.T) Harness

// RunSuite runs every case against the adapter the factory builds.
//
// It is the executable form of the task-persistence capability: an adapter that
// passes it behaves identically to every other adapter in every way the engine
// depends on.
func RunSuite(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("Repository", func(t *testing.T) { runRepositoryCases(t, factory) })
	t.Run("Inbox", func(t *testing.T) { runInboxCases(t, factory) })
	t.Run("Outbox", func(t *testing.T) { runOutboxCases(t, factory) })
	t.Run("Transactions", func(t *testing.T) { runTransactionCases(t, factory) })
	t.Run("Rollback", func(t *testing.T) { runRollbackCases(t, factory) })
	t.Run("Concurrency", func(t *testing.T) { runConcurrencyCases(t, factory) })
	t.Run("Portability", func(t *testing.T) { runPortabilityCases(t, factory) })
	t.Run("Cancellation", func(t *testing.T) { runCancellationCases(t, factory) })
}
