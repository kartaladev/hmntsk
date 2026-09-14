package sqlstore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/sqlkit"
)

func TestEmailSchema(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		prefix  string
		file    string
		assert  func(t *testing.T, store *sqlstore.Store, file string)
	}

	golden := func(t *testing.T, store *sqlstore.Store, file string) {
		t.Helper()

		schema := store.EmailSchema()
		assert.NotContains(t, schema, sqlkit.PrefixToken, "the prefix is applied")
		assert.Equal(t, []string{strings.TrimSuffix(store.Tables()[0], sqlstore.NotificationsTable) + sqlstore.EmailDeliveriesTable},
			store.EmailTables())

		for _, table := range store.EmailTables() {
			assert.Contains(t, schema, table)
		}

		for _, table := range store.Tables() {
			assert.NotContains(t, schema, `"`+table+`"`, "the email schema creates no notification table")
			assert.NotContains(t, schema, "`"+table+"`", "the email schema creates no notification table")
		}

		for _, statement := range sqlkit.SplitStatements(schema) {
			assert.Truef(t, strings.HasPrefix(statement, "CREATE "), "%q", statement)
		}

		path := filepath.Join("testdata", "schema", file)

		if *update {
			require.NoError(t, os.WriteFile(path, []byte(schema), 0o600))
		}

		want, err := os.ReadFile(path)
		require.NoError(t, err, "run go test -update to write the golden file")
		assert.Equal(t, string(want), schema)
	}

	cases := []testCase{
		{name: "PostgreSQL", dialect: sqlkit.PostgreSQL, file: "email_postgres.sql", assert: golden},
		{name: "PostgreSQL with a prefix", dialect: sqlkit.PostgreSQL, prefix: "app_", file: "email_postgres_app_.sql", assert: golden},
		{name: "MySQL", dialect: sqlkit.MySQL, file: "email_mysql.sql", assert: golden},
		{name: "MySQL with a prefix", dialect: sqlkit.MySQL, prefix: "app_", file: "email_mysql_app_.sql", assert: golden},
		{name: "SQLite", dialect: sqlkit.SQLite, file: "email_sqlite.sql", assert: golden},
		{name: "SQLite with a prefix", dialect: sqlkit.SQLite, prefix: "app_", file: "email_sqlite_app_.sql", assert: golden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store, err := sqlstore.New(newExecutor(t, tc.dialect), sqlstore.WithTablePrefix(tc.prefix))
			require.NoError(t, err)

			tc.assert(t, store, tc.file)
		})
	}
}
