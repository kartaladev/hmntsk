package pgxstore_test

import (
	"testing"

	"github.com/kartaladev/hmntsk/relaytest"
	"github.com/kartaladev/hmntsk/storetest"
)

// TestRelayOnPostgres runs the relay conformance suite against the pgx adapter.
//
// The relay suite needs the store and nothing else: it reaches it through the
// engine's own relay port rather than through a host-led transaction, so the
// harness carries no transaction or event hooks. The store itself is built by
// the same helper the storage suite uses, so both suites run against the same
// schema under the same prefix and cleanup.
func TestRelayOnPostgres(t *testing.T) {
	t.Parallel()

	pool := openPool(t, storetest.RunTestPostgres(t))

	relaytest.RunSuite(t, func(t *testing.T) relaytest.Harness {
		t.Helper()

		return relaytest.Harness{Store: newStore(t, pool)}
	})
}
