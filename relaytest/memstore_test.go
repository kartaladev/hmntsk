package relaytest_test

import (
	"testing"

	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/relaytest"
)

// TestSuiteAgainstTheInMemoryStore runs the relay suite against the reference
// store.
//
// It is how the suite is kept honest. A case that no store can pass, or one
// that passes for the wrong reason, shows up here — where the store's behaviour
// is entirely visible and the whole run takes milliseconds — long before it
// reaches a driver, where the same failure would be indistinguishable from a
// database quirk.
func TestSuiteAgainstTheInMemoryStore(t *testing.T) {
	t.Parallel()

	relaytest.RunSuite(t, func(*testing.T) relaytest.Harness {
		return relaytest.Harness{Store: memstore.New()}
	})
}
