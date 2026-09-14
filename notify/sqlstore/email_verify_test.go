package sqlstore_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/notify/sqlstore/internal/harness"
	"github.com/kartaladev/hmntsk/sqlkit"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
)

// The indexes VerifyEmailSchema requires, before the table prefix.
var requiredEmailIndexes = []string{
	"notify_email_deliveries_lease_idx",
	"notify_email_deliveries_retry_idx",
}

// dropEmailIndex drops a prefixed email index in the executor's dialect.
func dropEmailIndex(t *testing.T, executor sqlkit.Executor, store *sqlstore.Store, index string) {
	t.Helper()

	dialect := executor.Dialect()
	name := dialect.Quote(prefixOf(store) + index)

	if dialect.Name() == sqlkit.MySQL.Name() {
		exec(t, executor, "DROP INDEX "+name+" ON "+dialect.Quote(store.EmailTables()[0]))

		return
	}

	exec(t, executor, "DROP INDEX "+name)
}

// runVerifyEmailSchema checks VerifyEmailSchema over one executor, breaking a
// freshly migrated email schema one object at a time.
func runVerifyEmailSchema(t *testing.T, executor sqlkit.Executor) {
	t.Helper()

	type testCase struct {
		name   string
		store  func(t *testing.T) *sqlstore.Store
		breaks func(t *testing.T, store *sqlstore.Store)
		verify func(t *testing.T, store *sqlstore.Store) error
		assert func(t *testing.T, store *sqlstore.Store, err error)
	}

	withEmail := func(t *testing.T) *sqlstore.Store { return harness.NewEmailStore(t, executor) }
	verifyEmail := func(t *testing.T, store *sqlstore.Store) error { return store.VerifyEmailSchema(t.Context()) }
	unbroken := func(*testing.T, *sqlstore.Store) {}

	cases := []testCase{
		{
			name:  "a freshly migrated email schema, migrated twice, verifies",
			store: withEmail,
			breaks: func(t *testing.T, store *sqlstore.Store) {
				require.NoError(t, store.MigrateEmail(t.Context()), "migrating an existing schema changes nothing")
			},
			verify: verifyEmail,
			assert: func(t *testing.T, _ *sqlstore.Store, err error) { assert.NoError(t, err) },
		},
		{
			name:   "the notification schema verifies without the email table",
			store:  func(t *testing.T) *sqlstore.Store { return harness.NewStore(t, executor) },
			breaks: unbroken,
			verify: func(t *testing.T, store *sqlstore.Store) error { return store.VerifySchema(t.Context()) },
			assert: func(t *testing.T, _ *sqlstore.Store, err error) { assert.NoError(t, err) },
		},
		{
			name:   "a missing email table is reported",
			store:  func(t *testing.T) *sqlstore.Store { return harness.NewStore(t, executor) },
			breaks: unbroken,
			verify: verifyEmail,
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				assert.Contains(t, issues(t, err), store.EmailTables()[0]+": table is missing")
			},
		},
		{
			name:  "a missing column is reported, alongside a missing index",
			store: withEmail,
			breaks: func(t *testing.T, store *sqlstore.Store) {
				dialect := executor.Dialect()
				exec(t, executor, "ALTER TABLE "+dialect.Quote(store.EmailTables()[0])+" DROP COLUMN "+dialect.Quote("reason"))
				dropEmailIndex(t, executor, store, requiredEmailIndexes[0])
			},
			verify: verifyEmail,
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				listed := issues(t, err)
				assert.Contains(t, listed, store.EmailTables()[0]+".reason: column is missing")
				assert.Contains(t, listed, "index "+prefixOf(store)+requiredEmailIndexes[0]+" is missing")
			},
		},
	}

	for _, index := range requiredEmailIndexes {
		cases = append(cases, testCase{
			name:  "a missing " + index + " index is reported",
			store: withEmail,
			breaks: func(t *testing.T, store *sqlstore.Store) {
				dropEmailIndex(t, executor, store, index)
			},
			verify: verifyEmail,
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				assert.Contains(t, issues(t, err), "index "+prefixOf(store)+index+" is missing")
			},
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := tc.store(t)
			tc.breaks(t, store)

			tc.assert(t, store, tc.verify(t, store))
		})
	}
}

func TestVerifyEmailSchemaOnPostgres(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "postgres", sqlkittest.RunTestPostgres(t))

	runVerifyEmailSchema(t, stdsqlExecutor(t, db, sqlkit.PostgreSQL))
}

func TestVerifyEmailSchemaOnMySQL(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "mysql", sqlkittest.RunTestMySQL(t))

	runVerifyEmailSchema(t, stdsqlExecutor(t, db, sqlkit.MySQL))
}

func TestVerifyEmailSchemaOnSQLite(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "sqlite", sqlkittest.RunTestSQLite(t))

	runVerifyEmailSchema(t, stdsqlExecutor(t, db, sqlkit.SQLite))
}
