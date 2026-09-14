// Package gormtest runs sqlstore's conformance suite on GORM.
//
// It is a separate test binary from the database/sql and pgx entry points on
// purpose: GORM's SQLite driver and the modernc driver database/sql uses both
// register a driver named "sqlite", and one binary cannot hold both.
package gormtest_test

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/kartaladev/hmntsk/notify/notifytest"
	"github.com/kartaladev/hmntsk/notify/sqlstore/internal/harness"
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

	deadline := time.Now().Add(harness.ReachTimeout)

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

	harness.Reach(t, "gorm", pool.PingContext)

	return db
}

// run executes the conformance suite over a GORM handle.
func run(t *testing.T, db *gorm.DB, dialect sqlkit.Dialect) {
	t.Helper()

	executor, err := gormexec.New(db, dialect)
	require.NoError(t, err)

	notifytest.Run(t, harness.Factory(executor))
	t.Run("email", func(t *testing.T) { notifytest.RunEmail(t, harness.EmailFactory(executor)) })
	t.Run("email-dispatch", func(t *testing.T) { notifytest.RunEmailDispatch(t, harness.EmailFactory(executor)) })
}

func TestStoreOnGormPostgres(t *testing.T) {
	t.Parallel()

	run(t, openGORM(t, postgres.Open(sqlkittest.RunTestPostgres(t))), sqlkit.PostgreSQL)
}

func TestStoreOnGormMySQL(t *testing.T) {
	t.Parallel()

	run(t, openGORM(t, mysql.Open(sqlkittest.RunTestMySQL(t))), sqlkit.MySQL)
}

func TestStoreOnGormSQLite(t *testing.T) {
	t.Parallel()

	run(t, openGORM(t, sqlite.Open(sqlkittest.RunTestSQLite(t))), sqlkit.SQLite)
}
