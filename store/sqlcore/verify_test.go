package sqlcore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// schemaRow is one row of the introspection a database would return.
type schemaRow struct {
	table     string
	column    string
	collation string
}

// fakeRows replays canned introspection rows.
type fakeRows struct {
	rows  []schemaRow
	index int
}

func (r *fakeRows) Next() bool {
	r.index++

	return r.index <= len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.index-1]

	values := []string{row.table, row.column, row.collation}
	for i, target := range dest {
		*target.(*any) = values[i] //nolint:errcheck,forcetypeassert // the scanner always passes *any
	}

	return nil
}

func (r *fakeRows) Err() error { return nil }

// fakeQuerier answers schema introspection with canned rows.
type fakeQuerier struct {
	rows []schemaRow
	sql  string
}

func (q *fakeQuerier) QueryStatement(_ context.Context, sql string, _ ...any) (sqlcore.Rows, error) {
	q.sql = sql

	return &fakeRows{rows: q.rows}, nil
}

// completeSchema builds the introspection a correct schema would report.
func completeSchema(b *sqlcore.Builder, dialect sqlcore.Dialect, prefix string) []schemaRow {
	collation := dialect.IdentifierCollation()

	columnsByTable := map[string][]string{
		sqlcore.TasksTable:      b.TaskColumns(),
		sqlcore.CandidatesTable: {"task_id", "kind", "value", "ordinal"},
		sqlcore.HistoryTable:    b.HistoryColumns(),
		sqlcore.OutboxTable:     b.OutboxColumns(),
		sqlcore.TypesTable: {
			"name", "title", "description", "input_schema", "output_schema",
			"default_priority", "default_deadline_ms", "default_escalation",
			"default_assignment", "updated_at",
		},
	}

	var rows []schemaRow

	for table, columns := range columnsByTable {
		for _, column := range columns {
			rows = append(rows, schemaRow{table: prefix + table, column: column, collation: collation})
		}
	}

	return rows
}

func TestVerifySchema(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		mutate func(rows []schemaRow) []schemaRow
		assert func(t *testing.T, err error)
	}

	withoutTable := func(table string) func([]schemaRow) []schemaRow {
		return func(rows []schemaRow) []schemaRow {
			kept := make([]schemaRow, 0, len(rows))

			for _, row := range rows {
				if row.table != table {
					kept = append(kept, row)
				}
			}

			return kept
		}
	}

	cases := []testCase{
		{
			name:   "a correct schema passes",
			mutate: func(rows []schemaRow) []schemaRow { return rows },
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name:   "a missing table is named",
			mutate: withoutTable("task_candidates"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.ErrorIs(t, err, hmntsk.ErrConfiguration,
					"a schema the engine cannot run against is a wiring problem, found at startup")
				assert.Contains(t, err.Error(), "task_candidates: table is missing")
			},
		},
		{
			name: "a missing column is named",
			mutate: func(rows []schemaRow) []schemaRow {
				kept := make([]schemaRow, 0, len(rows))

				for _, row := range rows {
					if row.table != "tasks" || row.column != "locked_until" {
						kept = append(kept, row)
					}
				}

				return kept
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "tasks.locked_until: column is missing")
			},
		},
		{
			name: "a missing delivery column is named",
			mutate: func(rows []schemaRow) []schemaRow {
				kept := make([]schemaRow, 0, len(rows))

				for _, row := range rows {
					if row.table != "task_outbox" || row.column != "next_attempt_at" {
						kept = append(kept, row)
					}
				}

				return kept
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "task_outbox.next_attempt_at: column is missing",
					"a schema without the delivery columns would let the relay start and fail per event")
			},
		},
		{
			name: "a case-insensitive collation on a sink name is named",
			mutate: func(rows []schemaRow) []schemaRow {
				for i := range rows {
					if rows[i].table == "task_outbox" && rows[i].column == "accepted_sinks" {
						rows[i].collation = "utf8mb4_0900_ai_ci"
					}
				}

				return rows
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "task_outbox.accepted_sinks")
				assert.Contains(t, err.Error(), "case-insensitively",
					"a sink whose name matched another's in a different case would be told "+
						"an event it never took had already been delivered")
			},
		},
		{
			name: "a case-insensitive collation is named",
			mutate: func(rows []schemaRow) []schemaRow {
				for i := range rows {
					if rows[i].table == "task_candidates" && rows[i].column == "value" {
						rows[i].collation = "utf8mb4_0900_ai_ci"
					}
				}

				return rows
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "task_candidates.value")
				assert.Contains(t, err.Error(), "utf8mb4_0900_ai_ci")
				assert.Contains(t, err.Error(), "case-insensitively",
					"the message has to say why this matters, not only that it differs")
			},
		},
		{
			name: "a wrong collation on a payload column is not an issue",
			mutate: func(rows []schemaRow) []schemaRow {
				for i := range rows {
					if rows[i].table == "tasks" && rows[i].column == "reason" {
						rows[i].collation = "utf8mb4_0900_ai_ci"
					}
				}

				return rows
			},
			assert: func(t *testing.T, err error) {
				require.NoError(t, err, "free text is never compared for eligibility")
			},
		},
		{
			name: "every discrepancy is reported, not only the first",
			mutate: func(rows []schemaRow) []schemaRow {
				rows = withoutTable("task_outbox")(rows)

				kept := make([]schemaRow, 0, len(rows))

				for _, row := range rows {
					if row.table == "tasks" && (row.column == "due_at" || row.column == "locked_by") {
						continue
					}

					kept = append(kept, row)
				}

				return kept
			},
			assert: func(t *testing.T, err error) {
				require.Error(t, err)

				var schemaErr *sqlcore.SchemaError

				require.ErrorAs(t, err, &schemaErr)
				assert.Len(t, schemaErr.Issues, 3,
					"startup is the one moment the whole schema can be acted on at once")
				assert.Equal(t, "mysql", schemaErr.Dialect)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := sqlcore.New(sqlcore.MySQL)

			querier := &fakeQuerier{rows: tc.mutate(completeSchema(b, sqlcore.MySQL, ""))}
			tc.assert(t, b.VerifySchema(t.Context(), querier))
		})
	}
}

func TestVerifySchemaHonoursTheTablePrefix(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.PostgreSQL, sqlcore.WithTablePrefix("hmntsk_"))

	prefixed := &fakeQuerier{rows: completeSchema(b, sqlcore.PostgreSQL, "hmntsk_")}
	require.NoError(t, b.VerifySchema(t.Context(), prefixed))

	for _, table := range b.Tables() {
		assert.Contains(t, prefixed.sql, "$", "the introspection binds its table names")
		assert.True(t, strings.HasPrefix(table, "hmntsk_"))
	}

	unprefixed := &fakeQuerier{rows: completeSchema(b, sqlcore.PostgreSQL, "")}

	err := b.VerifySchema(t.Context(), unprefixed)
	require.Error(t, err, "a prefixed deployment must not be satisfied by unprefixed tables")
	assert.Contains(t, err.Error(), "hmntsk_tasks: table is missing")
}

func TestVerifySchemaOnSQLiteChecksStructureOnly(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.SQLite)

	rows := completeSchema(b, sqlcore.SQLite, "")
	for i := range rows {
		// SQLite exposes no collation through introspection.
		rows[i].collation = ""
	}

	require.NoError(t, b.VerifySchema(t.Context(), &fakeQuerier{rows: rows}),
		"BINARY is SQLite's default and the published schema never overrides it")

	statement := b.SchemaQuery()
	assert.Contains(t, statement.SQL, "sqlite_master")
	assert.Contains(t, statement.SQL, "pragma_table_info")
}

func TestSchemaQueryPerDialect(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		expect  string
	}

	cases := []testCase{
		{name: "postgres", dialect: sqlcore.PostgreSQL, expect: "information_schema.columns"},
		{name: "mysql", dialect: sqlcore.MySQL, expect: "information_schema.COLUMNS"},
		{name: "sqlite", dialect: sqlcore.SQLite, expect: "sqlite_master"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement := sqlcore.New(tc.dialect).SchemaQuery()

			assert.Contains(t, statement.SQL, tc.expect)
			assert.Len(t, statement.Args, 5, "one bound name per table the engine owns")
		})
	}
}
