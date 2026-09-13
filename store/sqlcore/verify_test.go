package sqlcore_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// schemaRow is one row of the introspection a database would return. An index
// row carries the index name in column, and no collation.
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

// fakeQuerier answers schema introspection with canned rows: the index rows for
// the builder's index introspection, and the column rows for anything else.
type fakeQuerier struct {
	rows     []schemaRow
	indexes  []schemaRow
	indexSQL string
	sql      string
}

// newQuerier builds a querier that recognises the builder's index introspection.
func newQuerier(b *sqlcore.Builder, columns, indexes []schemaRow) *fakeQuerier {
	return &fakeQuerier{rows: columns, indexes: indexes, indexSQL: b.IndexQuery().SQL}
}

func (q *fakeQuerier) QueryStatement(_ context.Context, sql string, _ ...any) (sqlcore.Rows, error) {
	if q.indexSQL != "" && sql == q.indexSQL {
		return &fakeRows{rows: q.indexes}, nil
	}

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
			"default_assignment", "updated_at", "metadata",
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

// completeIndexes builds the index introspection a correct schema would report:
// every index the published DDL creates. The names are written out rather than
// read from the builder, so that dropping one from the engine is a visible
// change here too.
func completeIndexes(prefix string) []schemaRow {
	indexesByTable := map[string][]string{
		sqlcore.TasksTable: {
			"tasks_assignee_idx", "tasks_status_idx", "tasks_type_idx", "tasks_correlation_idx",
			"tasks_due_idx", "tasks_priority_idx", "tasks_due_order_idx", "tasks_urgency_idx",
		},
		sqlcore.CandidatesTable: {"task_candidates_lookup_idx"},
		sqlcore.OutboxTable:     {"task_outbox_unpublished_idx", "task_outbox_due_idx"},
	}

	var rows []schemaRow

	for table, indexes := range indexesByTable {
		for _, index := range indexes {
			rows = append(rows, schemaRow{table: prefix + table, column: prefix + index})
		}
	}

	return rows
}

// without drops the rows naming any of the given columns or indexes of a table.
func without(table string, names ...string) func([]schemaRow) []schemaRow {
	return func(rows []schemaRow) []schemaRow {
		kept := make([]schemaRow, 0, len(rows))

		for _, row := range rows {
			if row.table == table && (len(names) == 0 || slices.Contains(names, row.column)) {
				continue
			}

			kept = append(kept, row)
		}

		return kept
	}
}

func TestVerifySchema(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		// columns and indexes change what a correct schema reports. Nil leaves
		// that half of the introspection complete.
		columns func(rows []schemaRow) []schemaRow
		indexes func(rows []schemaRow) []schemaRow
		assert  func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name:   "a correct schema passes",
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name:    "a missing table is named",
			columns: without("task_candidates"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.ErrorIs(t, err, hmntsk.ErrConfiguration,
					"a schema the engine cannot run against is a wiring problem, found at startup")
				assert.Contains(t, err.Error(), "task_candidates: table is missing")
			},
		},
		{
			name:    "a missing column is named",
			columns: without("tasks", "locked_until"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "tasks.locked_until: column is missing")
			},
		},
		{
			name:    "a missing delivery column is named",
			columns: without("task_outbox", "next_attempt_at"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "task_outbox.next_attempt_at: column is missing",
					"a schema without the delivery columns would let the relay start and fail per event")
			},
		},
		{
			name:    "a missing type metadata column is named",
			columns: without("task_types", "metadata"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "task_types.metadata: column is missing",
					"publishing a type with metadata to a schema without the column fails per write")
			},
		},
		{
			name: "a case-insensitive collation on a sink name is named",
			columns: func(rows []schemaRow) []schemaRow {
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
			columns: func(rows []schemaRow) []schemaRow {
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
			columns: func(rows []schemaRow) []schemaRow {
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
			name:    "a missing ordering index is named",
			indexes: without("tasks", "tasks_urgency_idx"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.Contains(t, err.Error(), "tasks: index tasks_urgency_idx is missing",
					"an urgency inbox over a schema without its index reads the whole table per page")
			},
		},
		{
			name:    "every missing index is named, the sweep's included",
			indexes: without("tasks", "tasks_priority_idx", "tasks_due_order_idx", "tasks_due_idx"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "tasks: index tasks_priority_idx is missing")
				assert.Contains(t, err.Error(), "tasks: index tasks_due_order_idx is missing")
				assert.Contains(t, err.Error(), "tasks: index tasks_due_idx is missing",
					"the escalation sweep still relies on the due-and-status index")
			},
		},
		{
			name: "every discrepancy is reported, not only the first",
			columns: func(rows []schemaRow) []schemaRow {
				return without("tasks", "due_at", "locked_by")(without("task_outbox")(rows))
			},
			indexes: without("task_candidates", "task_candidates_lookup_idx"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)

				var schemaErr *sqlcore.SchemaError

				require.ErrorAs(t, err, &schemaErr)
				assert.Len(t, schemaErr.Issues, 4,
					"startup is the one moment the whole schema can be acted on at once")
				assert.Equal(t, "mysql", schemaErr.Dialect)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := sqlcore.New(sqlcore.MySQL)

			columns := completeSchema(b, sqlcore.MySQL, "")
			if tc.columns != nil {
				columns = tc.columns(columns)
			}

			indexes := completeIndexes("")
			if tc.indexes != nil {
				indexes = tc.indexes(indexes)
			}

			tc.assert(t, b.VerifySchema(t.Context(), newQuerier(b, columns, indexes)))
		})
	}
}

func TestVerifySchemaHonoursTheTablePrefix(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.PostgreSQL, sqlcore.WithTablePrefix("hmntsk_"))

	prefixed := newQuerier(b, completeSchema(b, sqlcore.PostgreSQL, "hmntsk_"), completeIndexes("hmntsk_"))
	require.NoError(t, b.VerifySchema(t.Context(), prefixed))

	for _, table := range b.Tables() {
		assert.Contains(t, prefixed.sql, "$", "the introspection binds its table names")
		assert.True(t, strings.HasPrefix(table, "hmntsk_"))
	}

	unprefixed := newQuerier(b, completeSchema(b, sqlcore.PostgreSQL, ""), completeIndexes(""))

	err := b.VerifySchema(t.Context(), unprefixed)
	require.Error(t, err, "a prefixed deployment must not be satisfied by unprefixed tables")
	assert.Contains(t, err.Error(), "hmntsk_tasks: table is missing")

	mixed := newQuerier(b, completeSchema(b, sqlcore.PostgreSQL, "hmntsk_"), completeIndexes(""))

	err = b.VerifySchema(t.Context(), mixed)
	require.Error(t, err, "the prefix reaches every index name, so an unprefixed index is not the engine's")
	assert.Contains(t, err.Error(), "hmntsk_tasks: index hmntsk_tasks_urgency_idx is missing")
}

func TestVerifySchemaOnSQLiteChecksStructureOnly(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.SQLite)

	rows := completeSchema(b, sqlcore.SQLite, "")
	for i := range rows {
		// SQLite exposes no collation through introspection.
		rows[i].collation = ""
	}

	require.NoError(t, b.VerifySchema(t.Context(), newQuerier(b, rows, completeIndexes(""))),
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

func TestIndexQueryPerDialect(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		assert  func(t *testing.T, statement sqlcore.Statement)
	}

	bindsEveryTable := func(t *testing.T, statement sqlcore.Statement) {
		t.Helper()
		assert.Len(t, statement.Args, 5, "one bound name per table the engine owns")
	}

	cases := []testCase{
		{
			name:    "postgres reads pg_indexes",
			dialect: sqlcore.PostgreSQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, "pg_indexes")
				assert.Contains(t, statement.SQL, "current_schema()")
				bindsEveryTable(t, statement)
			},
		},
		{
			name:    "mysql reads the statistics of the current database",
			dialect: sqlcore.MySQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, "information_schema.STATISTICS")
				assert.Contains(t, statement.SQL, "DATABASE()")
				bindsEveryTable(t, statement)
			},
		},
		{
			name:    "sqlite reads the index entries of sqlite_master",
			dialect: sqlcore.SQLite,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, "sqlite_master")
				assert.Contains(t, statement.SQL, "'index'")
				bindsEveryTable(t, statement)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlcore.New(tc.dialect).IndexQuery())
		})
	}
}
