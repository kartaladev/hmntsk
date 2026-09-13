package sqlkit_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/sqlkit"
)

// introspectionRow is one row a database would return: (table, column,
// collation) for the column introspection, (table, index) for the index one.
type introspectionRow struct {
	table     string
	column    string
	collation string
}

// scriptedRows replays canned introspection rows.
type scriptedRows struct {
	rows  []introspectionRow
	index int
}

func (r *scriptedRows) Next() bool {
	r.index++

	return r.index <= len(r.rows)
}

func (r *scriptedRows) Scan(dest ...any) error {
	row := r.rows[r.index-1]

	values := []string{row.table, row.column, row.collation}
	for i, target := range dest {
		*target.(*any) = values[i] //nolint:errcheck,forcetypeassert // verification always passes *any
	}

	return nil
}

func (r *scriptedRows) Err() error { return nil }

// errIntrospection is the failure a scripted querier can inject.
var errIntrospection = errors.New("sqlkit_test: injected introspection failure")

// scriptedQuerier answers the index introspection with index rows and anything
// else with column rows.
type scriptedQuerier struct {
	indexSQL string
	columns  []introspectionRow
	indexes  []introspectionRow
	fail     bool
	args     []any
}

func (q *scriptedQuerier) QueryStatement(_ context.Context, sql string, args ...any) (sqlkit.Rows, error) {
	if q.fail {
		return nil, errIntrospection
	}

	if sql == q.indexSQL {
		return &scriptedRows{rows: q.indexes}, nil
	}

	q.args = args

	return &scriptedRows{rows: q.columns}, nil
}

// widgetExpectation is a small two-table expectation with identifier columns and
// one secondary index.
var widgetExpectation = sqlkit.SchemaExpectation{
	"widgets": {
		Columns:           []string{"id", "owner", "payload"},
		IdentifierColumns: []string{"id", "owner"},
		Indexes:           []string{"widgets_owner_idx"},
	},
	"parts": {
		Columns:           []string{"id", "widget_id"},
		IdentifierColumns: []string{"id"},
	},
}

// completeColumns is the column introspection of a schema that meets the
// expectation, with the given collation on every column.
func completeColumns(prefix, collation string) []introspectionRow {
	var rows []introspectionRow

	for table, expected := range widgetExpectation {
		for _, column := range expected.Columns {
			rows = append(rows, introspectionRow{table: prefix + table, column: column, collation: collation})
		}
	}

	return rows
}

// completeIndexes is the index introspection of a schema that meets the
// expectation.
func completeIndexes(prefix string) []introspectionRow {
	return []introspectionRow{{table: prefix + "widgets", column: prefix + "widgets_owner_idx"}}
}

// dropRows removes rows of a table, or only those naming the given columns or
// indexes.
func dropRows(table string, names ...string) func([]introspectionRow) []introspectionRow {
	return func(rows []introspectionRow) []introspectionRow {
		return slices.DeleteFunc(slices.Clone(rows), func(row introspectionRow) bool {
			return row.table == table && (len(names) == 0 || slices.Contains(names, row.column))
		})
	}
}

// setCollation changes one column's reported collation.
func setCollation(table, column, collation string) func([]introspectionRow) []introspectionRow {
	return func(rows []introspectionRow) []introspectionRow {
		rows = slices.Clone(rows)

		for i := range rows {
			if rows[i].table == table && rows[i].column == column {
				rows[i].collation = collation
			}
		}

		return rows
	}
}

func TestVerifySchema(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		prefix  string
		// columns and indexes change what a correct schema reports; nil leaves
		// that half complete. collation is what every column reports.
		columns   func([]introspectionRow) []introspectionRow
		indexes   func([]introspectionRow) []introspectionRow
		collation string
		fail      bool
		assert    func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "a schema that meets the expectation passes", dialect: sqlkit.MySQL, collation: "utf8mb4_0900_as_cs",
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name: "a missing table is reported once, without its columns or indexes", dialect: sqlkit.MySQL,
			collation: "utf8mb4_0900_as_cs", columns: dropRows("widgets"),
			assert: func(t *testing.T, err error) {
				var schemaErr *sqlkit.SchemaError

				require.ErrorAs(t, err, &schemaErr)
				require.Len(t, schemaErr.Issues, 1)
				assert.Equal(t, "widgets: table is missing", schemaErr.Issues[0].String())
			},
		},
		{
			name: "a missing column is named", dialect: sqlkit.PostgreSQL, collation: "C",
			columns: dropRows("parts", "widget_id"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "parts.widget_id: column is missing")
			},
		},
		{
			name: "a case-insensitive collation on an identifier column is named", dialect: sqlkit.MySQL,
			collation: "utf8mb4_0900_as_cs", columns: setCollation("widgets", "owner", "utf8mb4_0900_ai_ci"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "widgets.owner")
				assert.Contains(t, err.Error(), `collation is "utf8mb4_0900_ai_ci" but must be "utf8mb4_0900_as_cs"`)
				assert.Contains(t, err.Error(), "case-insensitively", "the message has to say why it matters")
			},
		},
		{
			name: "a collation on a column that is not an identifier is not checked", dialect: sqlkit.MySQL,
			collation: "utf8mb4_0900_as_cs", columns: setCollation("widgets", "payload", "utf8mb4_0900_ai_ci"),
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name: "a missing index is named", dialect: sqlkit.PostgreSQL, collation: "C",
			indexes: dropRows("widgets"),
			assert: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "widgets: index widgets_owner_idx is missing")
			},
		},
		{
			name: "sqlite reports no collation, which is its default", dialect: sqlkit.SQLite, collation: "",
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name: "every discrepancy is reported in one error", dialect: sqlkit.MySQL, collation: "utf8mb4_0900_as_cs",
			columns: func(rows []introspectionRow) []introspectionRow {
				return setCollation("parts", "id", "latin1_swedish_ci")(dropRows("widgets", "payload")(rows))
			},
			indexes: dropRows("widgets"),
			assert: func(t *testing.T, err error) {
				var schemaErr *sqlkit.SchemaError

				require.ErrorAs(t, err, &schemaErr)
				assert.Len(t, schemaErr.Issues, 3, "startup is when the whole schema can be acted on at once")
				assert.Equal(t, "mysql", schemaErr.Dialect)
				assert.ErrorIs(t, err, sqlkit.ErrSchemaMismatch)
				assert.ErrorIs(t, err, sqlkit.ErrConfiguration, "a schema that cannot be run against is a wiring problem")
			},
		},
		{
			name: "the prefix reaches tables and indexes", dialect: sqlkit.PostgreSQL, prefix: "app_", collation: "C",
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name: "an introspection failure is returned, not reported as issues", dialect: sqlkit.SQLite, fail: true,
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, errIntrospection)

				var schemaErr *sqlkit.SchemaError

				assert.NotErrorAs(t, err, &schemaErr)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			columns := completeColumns(tc.prefix, tc.collation)
			if tc.columns != nil {
				columns = tc.columns(columns)
			}

			indexes := completeIndexes(tc.prefix)
			if tc.indexes != nil {
				indexes = tc.indexes(indexes)
			}

			querier := &scriptedQuerier{
				indexSQL: sqlkit.IndexQuery(tc.dialect, []string{tc.prefix + "parts", tc.prefix + "widgets"}).SQL,
				columns:  columns,
				indexes:  indexes,
				fail:     tc.fail,
			}

			tc.assert(t, sqlkit.VerifySchema(t.Context(), querier, tc.dialect, tc.prefix, widgetExpectation))
		})
	}
}

func TestVerifySchemaRefusesAnUnprefixedDeployment(t *testing.T) {
	t.Parallel()

	querier := &scriptedQuerier{
		indexSQL: sqlkit.IndexQuery(sqlkit.PostgreSQL, []string{"app_parts", "app_widgets"}).SQL,
		columns:  completeColumns("", "C"),
		indexes:  completeIndexes(""),
	}

	err := sqlkit.VerifySchema(t.Context(), querier, sqlkit.PostgreSQL, "app_", widgetExpectation)
	require.Error(t, err, "a prefixed deployment must not be satisfied by unprefixed tables")
	assert.Contains(t, err.Error(), "app_widgets: table is missing")
	assert.Equal(t, []any{"app_parts", "app_widgets"}, querier.args, "the introspection binds the prefixed names")
}

func TestIntrospectionQueriesPerDialect(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		assert  func(t *testing.T, columns, indexes sqlkit.Statement)
	}

	tables := []string{"parts", "widgets"}

	cases := []testCase{
		{
			name: "postgres", dialect: sqlkit.PostgreSQL,
			assert: func(t *testing.T, columns, indexes sqlkit.Statement) {
				assert.Contains(t, columns.SQL, "information_schema.columns")
				assert.Contains(t, columns.SQL, "current_schema()")
				assert.Contains(t, indexes.SQL, "pg_indexes")
			},
		},
		{
			name: "mysql", dialect: sqlkit.MySQL,
			assert: func(t *testing.T, columns, indexes sqlkit.Statement) {
				assert.Contains(t, columns.SQL, "information_schema.COLUMNS")
				assert.Contains(t, indexes.SQL, "information_schema.STATISTICS")
				assert.Contains(t, indexes.SQL, "DATABASE()")
			},
		},
		{
			name: "sqlite", dialect: sqlkit.SQLite,
			assert: func(t *testing.T, columns, indexes sqlkit.Statement) {
				assert.Contains(t, columns.SQL, "pragma_table_info")
				assert.Contains(t, indexes.SQL, "'index'")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			columns := sqlkit.SchemaQuery(tc.dialect, tables)
			indexes := sqlkit.IndexQuery(tc.dialect, tables)

			assert.Equal(t, []any{"parts", "widgets"}, columns.Args, "one bound name per table")
			assert.Equal(t, []any{"parts", "widgets"}, indexes.Args, "one bound name per table")
			tc.assert(t, columns, indexes)
		})
	}
}
