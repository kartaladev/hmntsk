package sqlstore_test

import (
	"testing"

	"github.com/kartaladev/hmntsk/relaytest"
	"github.com/kartaladev/hmntsk/store/sqlcore"
	"github.com/kartaladev/hmntsk/storetest"
)

// relayFactory adapts a store harness to the relay suite.
//
// The relay suite needs the store and nothing else: it reaches it through the
// engine's own relay port rather than through a host-led transaction, so the
// host-transaction and event-reading hooks the storage suite needs have no
// counterpart here. Reusing the factory keeps the schema, the table prefix and
// the cleanup identical between the two suites, which is the point — the relay
// cases are asserting this adapter's claiming, not a second one written for
// them.
func relayFactory(inner storetest.Factory) relaytest.Factory {
	return func(t *testing.T) relaytest.Harness {
		t.Helper()

		return relaytest.Harness{Store: inner(t).Store}
	}
}

func TestRelayOnSQLite(t *testing.T) {
	t.Parallel()

	db := open(t, "sqlite", storetest.RunTestSQLite(t))

	relaytest.RunSuite(t, relayFactory(factory(db, sqlcore.SQLite)))
}

func TestRelayOnPostgres(t *testing.T) {
	t.Parallel()

	db := open(t, "postgres", storetest.RunTestPostgres(t))

	relaytest.RunSuite(t, relayFactory(factory(db, sqlcore.PostgreSQL)))
}

func TestRelayOnMySQL(t *testing.T) {
	t.Parallel()

	db := open(t, "mysql", storetest.RunTestMySQL(t))

	relaytest.RunSuite(t, relayFactory(factory(db, sqlcore.MySQL)))
}
