package gormstore_test

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"

	"github.com/kartaladev/hmntsk/relaytest"
	"github.com/kartaladev/hmntsk/store/sqlcore"
	"github.com/kartaladev/hmntsk/storetest"
)

// relayFactory adapts a store harness to the relay suite, which needs the store
// and nothing else. See the same helper in store/sql for why it reuses the
// storage suite's factory rather than building a store of its own.
func relayFactory(inner storetest.Factory) relaytest.Factory {
	return func(t *testing.T) relaytest.Harness {
		t.Helper()

		return relaytest.Harness{Store: inner(t).Store}
	}
}

func TestRelayOnSQLite(t *testing.T) {
	t.Parallel()

	db := openGORM(t, sqlite.Open(storetest.RunTestSQLite(t)))

	relaytest.RunSuite(t, relayFactory(factory(db, sqlcore.SQLite)))
}

func TestRelayOnPostgres(t *testing.T) {
	t.Parallel()

	db := openGORM(t, postgres.Open(storetest.RunTestPostgres(t)))

	relaytest.RunSuite(t, relayFactory(factory(db, sqlcore.PostgreSQL)))
}

func TestRelayOnMySQL(t *testing.T) {
	t.Parallel()

	db := openGORM(t, mysql.Open(storetest.RunTestMySQL(t)))

	relaytest.RunSuite(t, relayFactory(factory(db, sqlcore.MySQL)))
}
