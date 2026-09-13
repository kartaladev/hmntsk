package sqlstore_test

import (
	"database/sql"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/sqlkit"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
)

var update = flag.Bool("update", false, "rewrite the golden schema files")

// unpublishedDialect is a dialect the store has no DDL document for.
type unpublishedDialect struct{ sqlkit.Dialect }

// Name implements sqlkit.Dialect.
func (unpublishedDialect) Name() string { return "oracle" }

// newExecutor returns an executor in a dialect over an in-memory SQLite handle
// that is never used to reach a database.
func newExecutor(t *testing.T, dialect sqlkit.Dialect) sqlkit.Executor {
	t.Helper()

	db, err := sql.Open("sqlite", "file::memory:")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	executor, err := stdsqlexec.New(db, dialect)
	require.NoError(t, err)

	return executor
}

func TestSchema(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		prefix  string
		assert  func(t *testing.T, store *sqlstore.Store, err error)
	}

	golden := func(file, prefix string) func(t *testing.T, store *sqlstore.Store, err error) {
		return func(t *testing.T, store *sqlstore.Store, err error) {
			require.NoError(t, err)

			schema := store.Schema()
			assert.NotContains(t, schema, sqlkit.PrefixToken, "the prefix is applied")

			for _, table := range store.Tables() {
				assert.Contains(t, schema, table)
			}

			if prefix != "" {
				assert.NotContains(t, schema, `"notify_`, "no unprefixed table or index")
				assert.NotContains(t, schema, "`notify_", "no unprefixed table or index")
			}

			path := filepath.Join("testdata", "schema", file)

			if *update {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(schema), 0o600))
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "run go test -update to write the golden file")
			assert.Equal(t, string(want), schema)
		}
	}

	cases := []testCase{
		{name: "PostgreSQL", dialect: sqlkit.PostgreSQL, assert: golden("postgres.sql", "")},
		{name: "PostgreSQL with a prefix", dialect: sqlkit.PostgreSQL, prefix: "app_", assert: golden("postgres_app_.sql", "app_")},
		{name: "MySQL", dialect: sqlkit.MySQL, assert: golden("mysql.sql", "")},
		{name: "MySQL with a prefix", dialect: sqlkit.MySQL, prefix: "app_", assert: golden("mysql_app_.sql", "app_")},
		{name: "SQLite", dialect: sqlkit.SQLite, assert: golden("sqlite.sql", "")},
		{name: "SQLite with a prefix", dialect: sqlkit.SQLite, prefix: "app_", assert: golden("sqlite_app_.sql", "app_")},
		{
			name:    "a dialect with no published schema is a configuration error",
			dialect: unpublishedDialect{sqlkit.SQLite},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				require.ErrorIs(t, err, notify.ErrConfiguration)
				assert.Nil(t, store)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store, err := sqlstore.New(newExecutor(t, tc.dialect), sqlstore.WithTablePrefix(tc.prefix))
			tc.assert(t, store, err)
		})
	}
}

func TestSchemaStatementsAreComplete(t *testing.T) {
	t.Parallel()

	for _, dialect := range sqlkit.Dialects() {
		store, err := sqlstore.New(newExecutor(t, dialect))
		require.NoError(t, err)

		statements := sqlkit.SplitStatements(store.Schema())
		assert.NotEmptyf(t, statements, "%s has statements", dialect.Name())

		for _, statement := range statements {
			assert.Truef(t, strings.HasPrefix(statement, "CREATE "), "%s: %q", dialect.Name(), statement)
		}
	}
}
