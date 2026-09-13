package sqlstore_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/notify/sqlstore/internal/harness"
	"github.com/kartaladev/hmntsk/sqlkit"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
)

// The indexes VerifySchema requires, before the table prefix.
var requiredIndexes = []string{
	"notify_notifications_source_key",
	"notify_notifications_recipient_idx",
	"notify_notifications_state_idx",
	"notify_notifications_subject_idx",
	"notify_notifications_inactive_idx",
	"notify_watermarks_updated_idx",
}

// prefixOf recovers a store's table prefix from its prefixed tables.
func prefixOf(store *sqlstore.Store) string {
	return strings.TrimSuffix(store.Tables()[0], sqlstore.NotificationsTable)
}

// execerOf returns an executor's schema-statement side, failing the test when it
// has none.
func execerOf(t *testing.T, executor sqlkit.Executor) sqlkit.Execer {
	t.Helper()

	execer, ok := executor.(sqlkit.Execer)
	require.True(t, ok, "every sqlkit executor runs schema statements")

	return execer
}

// exec runs a schema-breaking statement and fails the test on an error.
func exec(t *testing.T, executor sqlkit.Executor, statement string) {
	t.Helper()

	_, err := executor.Exec(t.Context(), sqlkit.Statement{SQL: statement})
	require.NoErrorf(t, err, "run %q", statement)
}

// dropIndex drops a prefixed index in the executor's dialect.
func dropIndex(t *testing.T, executor sqlkit.Executor, store *sqlstore.Store, index string) {
	t.Helper()

	dialect := executor.Dialect()
	name := dialect.Quote(prefixOf(store) + index)

	if dialect.Name() == sqlkit.MySQL.Name() {
		table := store.Tables()[0]
		if strings.HasPrefix(index, "notify_watermarks") {
			table = store.Tables()[1]
		}

		exec(t, executor, "DROP INDEX "+name+" ON "+dialect.Quote(table))

		return
	}

	exec(t, executor, "DROP INDEX "+name)
}

// issues lists a schema error's issues as text.
func issues(t *testing.T, err error) string {
	t.Helper()

	require.ErrorIs(t, err, sqlkit.ErrSchemaMismatch)

	var schema *sqlkit.SchemaError
	require.ErrorAs(t, err, &schema)

	return schema.IssueList()
}

// runVerifySchema checks VerifySchema over one executor, breaking a freshly
// migrated schema one object at a time.
func runVerifySchema(t *testing.T, executor sqlkit.Executor) {
	t.Helper()

	type testCase struct {
		name   string
		breaks func(t *testing.T, store *sqlstore.Store)
		assert func(t *testing.T, store *sqlstore.Store, err error)
	}

	cases := []testCase{
		{
			name: "a freshly migrated schema, migrated twice, verifies",
			breaks: func(t *testing.T, store *sqlstore.Store) {
				require.NoError(t, store.Migrate(t.Context()), "migrating an existing schema changes nothing")
			},
			assert: func(t *testing.T, _ *sqlstore.Store, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name: "a missing notifications table is reported",
			breaks: func(t *testing.T, store *sqlstore.Store) {
				require.NoError(t, sqlkit.DropTables(t.Context(), execerOf(t, executor), executor.Dialect(), store.Tables()[:1]))
			},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				assert.Contains(t, issues(t, err), store.Tables()[0]+": table is missing")
			},
		},
		{
			name: "a missing watermarks table is reported",
			breaks: func(t *testing.T, store *sqlstore.Store) {
				require.NoError(t, sqlkit.DropTables(t.Context(), execerOf(t, executor), executor.Dialect(), store.Tables()[1:]))
			},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				assert.Contains(t, issues(t, err), store.Tables()[1]+": table is missing")
			},
		},
		{
			name: "a missing column is reported",
			breaks: func(t *testing.T, store *sqlstore.Store) {
				dialect := executor.Dialect()
				exec(t, executor, "ALTER TABLE "+dialect.Quote(store.Tables()[0])+" DROP COLUMN "+dialect.Quote("title"))
			},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				assert.Contains(t, issues(t, err), store.Tables()[0]+".title: column is missing")
			},
		},
		{
			name: "every problem is reported at once",
			breaks: func(t *testing.T, store *sqlstore.Store) {
				dialect := executor.Dialect()
				exec(t, executor, "ALTER TABLE "+dialect.Quote(store.Tables()[0])+" DROP COLUMN "+dialect.Quote("title"))
				require.NoError(t, sqlkit.DropTables(t.Context(), execerOf(t, executor), executor.Dialect(), store.Tables()[1:]))
			},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				listed := issues(t, err)
				assert.Contains(t, listed, store.Tables()[0]+".title: column is missing")
				assert.Contains(t, listed, store.Tables()[1]+": table is missing")
			},
		},
	}

	for _, index := range requiredIndexes {
		cases = append(cases, testCase{
			name: "a missing " + index + " index is reported",
			breaks: func(t *testing.T, store *sqlstore.Store) {
				dropIndex(t, executor, store, index)
			},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				assert.Contains(t, issues(t, err), "index "+prefixOf(store)+index+" is missing")
			},
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := harness.NewStore(t, executor)
			tc.breaks(t, store)

			tc.assert(t, store, store.VerifySchema(t.Context()))
		})
	}
}

func TestVerifySchemaOnPostgres(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "postgres", sqlkittest.RunTestPostgres(t))

	runVerifySchema(t, stdsqlExecutor(t, db, sqlkit.PostgreSQL))
}

func TestVerifySchemaOnMySQL(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "mysql", sqlkittest.RunTestMySQL(t))

	runVerifySchema(t, stdsqlExecutor(t, db, sqlkit.MySQL))
}

func TestVerifySchemaOnSQLite(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "sqlite", sqlkittest.RunTestSQLite(t))

	runVerifySchema(t, stdsqlExecutor(t, db, sqlkit.SQLite))
}
