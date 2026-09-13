package sqlcore_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// schemaDocPath is the schema documentation, relative to this module.
const schemaDocPath = "../../docs/schema.md"

// TestSchemaDocumentationMatchesThePublishedDDL keeps docs/schema.md honest.
//
// Documentation that describes a schema is documentation that will describe the
// old schema, and an adopter who applies what the document says rather than
// what the module emits gets a database the engine refuses to start against.
// Everything the document asserts about the schema is checked here against what
// is actually published.
func TestSchemaDocumentationMatchesThePublishedDDL(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(schemaDocPath)
	require.NoError(t, err, "the schema documentation must exist")

	doc := string(raw)

	t.Run("every documented table is published", func(t *testing.T) {
		t.Parallel()

		builder := sqlcore.New(sqlcore.PostgreSQL)

		statements, err := builder.Migrations()
		require.NoError(t, err)

		ddl := strings.Join(statements, "\n")

		for _, table := range builder.Tables() {
			assert.Containsf(t, doc, "`"+table+"`",
				"table %s is published but the documentation never mentions it", table)
			assert.Containsf(t, ddl, table, "table %s is documented but not published", table)
		}
	})

	t.Run("the documented collations are the ones pinned", func(t *testing.T) {
		t.Parallel()

		for _, dialect := range sqlcore.Dialects() {
			collation := dialect.IdentifierCollation()

			assert.Containsf(t, doc, collation,
				"%s pins the %s collation but the documentation does not say so",
				dialect.Name(), collation)

			statements, err := sqlcore.Migrations(dialect)
			require.NoError(t, err)

			assert.Containsf(t, strings.Join(statements, "\n"), collation,
				"%s documents the %s collation but does not pin it", dialect.Name(), collation)
		}
	})

	t.Run("the documented timestamp types are the ones used", func(t *testing.T) {
		t.Parallel()

		for _, dialect := range sqlcore.Dialects() {
			columnType := dialect.TimestampColumnType()

			assert.Containsf(t, doc, columnType,
				"%s stores timestamps as %s but the documentation does not say so",
				dialect.Name(), columnType)
		}

		assert.Contains(t, doc, sqlcore.TimestampLayout,
			"the one fixed encoding for a dialect with no timestamp type must be documented")
	})

	t.Run("the documented index is the one that serves eligibility", func(t *testing.T) {
		t.Parallel()

		statements, err := sqlcore.Migrations(sqlcore.PostgreSQL)
		require.NoError(t, err)

		const index = "task_candidates_lookup_idx"

		assert.Contains(t, doc, index)
		assert.Contains(t, strings.Join(statements, "\n"), index)
	})

	t.Run("the ordering indexes are documented and published on every dialect", func(t *testing.T) {
		t.Parallel()

		published := make(map[string]string)

		for _, dialect := range sqlcore.Dialects() {
			statements, err := sqlcore.Migrations(dialect)
			require.NoError(t, err)

			published[dialect.Name()] = strings.Join(statements, "\n")
		}

		for _, index := range []string{"tasks_priority_idx", "tasks_due_order_idx", "tasks_urgency_idx"} {
			assert.Containsf(t, doc, "`"+index+"`", "index %s is not documented", index)

			for name, ddl := range published {
				assert.Containsf(t, ddl, index, "%s does not publish index %s", name, index)
			}
		}

		// Every index verification requires is one every dialect's DDL creates,
		// so the three schemas and the verifier cannot drift apart.
		indexes := sqlcore.New(sqlcore.PostgreSQL).Indexes()
		assert.NotEmpty(t, indexes, "verification requires the engine's indexes")

		for _, index := range indexes {
			for name, ddl := range published {
				assert.Containsf(t, ddl, index, "%s does not publish index %s, which verification requires",
					name, index)
			}
		}

		assert.Contains(t, doc, "does not avoid a sort",
			"the limit the no-deadline flag puts on the ordering indexes has to be written down")
	})

	t.Run("the type metadata column is documented and published on every dialect", func(t *testing.T) {
		t.Parallel()

		assert.Contains(t, doc, "`metadata`", "the task_types metadata column is not documented")
		assert.Contains(t, doc, "ADD COLUMN",
			"CREATE TABLE IF NOT EXISTS never adds a column to an existing table, so the upgrade has to be written down")

		for _, dialect := range sqlcore.Dialects() {
			b := sqlcore.New(dialect)

			statements, err := b.Migrations()
			require.NoError(t, err)

			var types string

			for _, statement := range statements {
				if strings.Contains(statement, "CREATE TABLE IF NOT EXISTS "+dialect.Quote(sqlcore.TypesTable)) {
					types = statement
				}
			}

			require.NotEmptyf(t, types, "%s publishes no task_types table", dialect.Name())
			assert.Containsf(t, types, dialect.Quote("metadata"),
				"%s does not publish the task_types metadata column", dialect.Name())
		}
	})

	t.Run("payload columns really are text on every dialect", func(t *testing.T) {
		t.Parallel()

		for _, dialect := range sqlcore.Dialects() {
			assert.NotEqualf(t, "jsonb", dialect.JSONColumnType(),
				"%s must not use a normalising JSON column type", dialect.Name())
			assert.NotEqualf(t, "JSON", dialect.JSONColumnType(),
				"%s must not use a normalising JSON column type", dialect.Name())

			statements, err := sqlcore.Migrations(dialect)
			require.NoError(t, err)

			assert.NotContainsf(t, strings.Join(statements, "\n"), " jsonb",
				"%s schema still declares a jsonb column", dialect.Name())
		}

		assert.Contains(t, doc, "as text, not as `jsonb`",
			"the reason payloads are text has to be written down, or it will be undone")
	})

	t.Run("the documented accessor names exist", func(t *testing.T) {
		t.Parallel()

		// Every API the document tells an adopter to call.
		for _, snippet := range []string{
			"sqlcore.Migrations(sqlcore.PostgreSQL)",
			"MigrationsSource()",
			"WithTablePrefix",
			"VerifySchema(ctx)",
			"store.Migrate(ctx)",
		} {
			assert.Containsf(t, doc, snippet, "the documentation should show %s", snippet)
		}

		// And they resolve.
		builder := sqlcore.New(sqlcore.PostgreSQL, sqlcore.WithTablePrefix("hmntsk_"))

		source, err := builder.MigrationsSource()
		require.NoError(t, err)
		assert.Contains(t, source, "hmntsk_tasks")

		statements, err := sqlcore.Migrations(sqlcore.PostgreSQL)
		require.NoError(t, err)
		assert.NotEmpty(t, statements)
	})
}
