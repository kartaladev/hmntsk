package storetest_test

import (
	"context"
	"testing"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/storetest"
)

// TestSuiteAgainstTheInMemoryStore runs the conformance suite against the
// reference adapter.
//
// It is how the suite is kept honest. A case that no adapter can pass, or one
// that passes for the wrong reason, shows up here — in a store whose behaviour
// is entirely visible — long before it reaches a driver where the failure would
// be indistinguishable from a database quirk.
func TestSuiteAgainstTheInMemoryStore(t *testing.T) {
	t.Parallel()

	storetest.RunSuite(t, func(_ *testing.T) storetest.Harness {
		store := memstore.New()

		return storetest.Harness{
			Store: store,
			HostTx: func(ctx context.Context) (context.Context, func(bool) error) {
				scoped, done := store.ContextWithTx(ctx)

				return scoped, func(commit bool) error {
					done(commit)

					return nil
				}
			},
			Events: func(context.Context) ([]hmntsk.Event, error) {
				return store.Events(), nil
			},
			// Transactions are serialised by a mutex, so two are never open at
			// once. The conflict semantics are still exercised, by the
			// sequential stale-version cases.
			SupportsConcurrentTransactions: false,
		}
	})
}
