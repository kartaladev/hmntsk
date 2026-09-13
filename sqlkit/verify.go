package sqlkit

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// TableExpectation is what a store's statements require of one table.
type TableExpectation struct {
	// Columns is every column the statements read or write, in the order
	// issues about them are reported.
	Columns []string
	// IdentifierColumns are the columns whose comparison must be
	// case-sensitive and whose ordering must be byte-wise, and which therefore
	// must carry the dialect's [Dialect.IdentifierCollation].
	IdentifierColumns []string
	// Indexes are the secondary indexes the statements rely on, by unprefixed
	// name. Primary keys are not listed: every dialect names them differently.
	Indexes []string
}

// SchemaExpectation is what a store's statements require of the live schema,
// by unprefixed table name.
type SchemaExpectation map[string]TableExpectation

// SchemaIssue is one discrepancy between the live schema and an expectation.
type SchemaIssue struct {
	// Table is the table the issue concerns, prefix applied.
	Table string
	// Column is the column, empty when the table or one of its indexes is the
	// problem.
	Column string
	// Detail says what is wrong.
	Detail string
}

// String implements [fmt.Stringer].
func (i SchemaIssue) String() string {
	if i.Column == "" {
		return i.Table + ": " + i.Detail
	}

	return i.Table + "." + i.Column + ": " + i.Detail
}

// SchemaError reports every discrepancy found, not only the first.
//
// Startup is the one moment when the whole schema can be inspected at once and
// the whole list acted on; discovering the same problems one at a time, as
// traffic happens to reach each statement, is strictly worse.
type SchemaError struct {
	// Dialect is the dialect verified against.
	Dialect string
	// Issues is every discrepancy found: missing tables and columns and wrong
	// collations first, in table order, then missing indexes.
	Issues []SchemaIssue
}

// Error implements the error interface.
func (e *SchemaError) Error() string {
	return fmt.Sprintf("sqlkit: the %s schema does not match what is expected: %s", e.Dialect, e.IssueList())
}

// IssueList renders every issue, semicolon separated, for an error message that
// wraps this one in its own words.
func (e *SchemaError) IssueList() string {
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.String())
	}

	return strings.Join(parts, "; ")
}

// Unwrap makes the error match [ErrSchemaMismatch], and through it
// [ErrConfiguration].
func (e *SchemaError) Unwrap() error { return ErrSchemaMismatch }

// SchemaQuery renders the column introspection a dialect offers for the given
// tables, prefixed names as stored: one row per column, as (table, column,
// collation).
func SchemaQuery(dialect Dialect, tables []string) Statement {
	w := NewWriter(dialect)
	names := bindable(tables)

	switch dialect.Name() {
	case PostgreSQL.Name():
		w.Write("SELECT table_name, column_name, COALESCE(collation_name, '') ")
		w.Write("FROM information_schema.columns ")
		w.Write("WHERE table_schema = current_schema() AND table_name IN (", w.BindAll(names...), ")")
	case MySQL.Name():
		w.Write("SELECT TABLE_NAME, COLUMN_NAME, COALESCE(COLLATION_NAME, '') ")
		w.Write("FROM information_schema.COLUMNS ")
		w.Write("WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (", w.BindAll(names...), ")")
	default:
		// SQLite exposes no collation through introspection. It does not need
		// to: BINARY is the default and the only way to lose it is to override
		// it per column. Verification therefore checks that the tables and
		// columns are there, and treats an unreported collation as the default.
		w.Write("SELECT m.name, p.name, '' ")
		w.Write("FROM sqlite_master m JOIN pragma_table_info(m.name) p ")
		w.Write("WHERE m.type = 'table' AND m.name IN (", w.BindAll(names...), ")")
	}

	return w.Done()
}

// IndexQuery renders the index introspection a dialect offers for the given
// tables, prefixed names as stored: one row per index, as (table, index).
func IndexQuery(dialect Dialect, tables []string) Statement {
	w := NewWriter(dialect)
	names := bindable(tables)

	switch dialect.Name() {
	case PostgreSQL.Name():
		w.Write("SELECT tablename, indexname FROM pg_indexes ")
		w.Write("WHERE schemaname = current_schema() AND tablename IN (", w.BindAll(names...), ")")
	case MySQL.Name():
		// STATISTICS has one row per indexed column, hence DISTINCT.
		w.Write("SELECT DISTINCT TABLE_NAME, INDEX_NAME FROM information_schema.STATISTICS ")
		w.Write("WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (", w.BindAll(names...), ")")
	default:
		w.Write("SELECT tbl_name, name FROM sqlite_master ")
		w.Write("WHERE type = 'index' AND tbl_name IN (", w.BindAll(names...), ")")
	}

	return w.Done()
}

// VerifySchema compares the live schema against an expectation, with a table
// prefix applied to every table and index name, and reports every discrepancy
// in one [*SchemaError] rather than failing on first use.
//
// It checks that every table and column exists, that identifier columns carry
// the collation that makes comparison case-sensitive, and that every expected
// index exists, by name. A missing table is reported once, without its columns
// or indexes. It does not check column types: a dialect has several spellings
// for the same storage, and a type mismatch that matters shows up as a failing
// statement immediately, while a wrong collation shows up months later as the
// wrong match.
//
// A failure to read the schema at all is returned as itself, not as issues.
func VerifySchema(
	ctx context.Context, querier Querier, dialect Dialect, prefix string, expectation SchemaExpectation,
) error {
	names := slices.Sorted(maps.Keys(expectation))

	prefixed := make([]string, 0, len(names))
	for _, name := range names {
		prefixed = append(prefixed, prefix+name)
	}

	columns, err := readColumns(ctx, querier, SchemaQuery(dialect, prefixed))
	if err != nil {
		return err
	}

	indexes, err := readIndexes(ctx, querier, IndexQuery(dialect, prefixed))
	if err != nil {
		return err
	}

	var issues []SchemaIssue

	for _, name := range names {
		issues = append(issues, columnIssues(dialect, prefix+name, expectation[name], columns)...)
	}

	for _, name := range names {
		issues = append(issues, indexIssues(prefix, prefix+name, expectation[name], columns, indexes)...)
	}

	if len(issues) == 0 {
		return nil
	}

	return &SchemaError{Dialect: dialect.Name(), Issues: issues}
}

// columnIssues reports a missing table, its missing columns and its identifier
// columns with the wrong collation.
func columnIssues(
	dialect Dialect, table string, expected TableExpectation, observed map[string]map[string]string,
) []SchemaIssue {
	found, present := observed[table]
	if !present {
		return []SchemaIssue{{Table: table, Detail: "table is missing"}}
	}

	want := dialect.IdentifierCollation()

	var issues []SchemaIssue

	for _, column := range expected.Columns {
		collation, ok := found[column]
		if !ok {
			issues = append(issues, SchemaIssue{Table: table, Column: column, Detail: "column is missing"})

			continue
		}

		// An unreported collation is the dialect's default, which is correct
		// everywhere verification can ask.
		if !slices.Contains(expected.IdentifierColumns, column) || collation == "" || collation == want {
			continue
		}

		issues = append(issues, SchemaIssue{
			Table: table, Column: column,
			Detail: fmt.Sprintf(
				"collation is %q but must be %q, or identifiers will compare case-insensitively",
				collation, want,
			),
		})
	}

	return issues
}

// indexIssues reports the expected indexes a present table lacks.
func indexIssues(
	prefix, table string, expected TableExpectation,
	columns map[string]map[string]string, indexes map[string]map[string]bool,
) []SchemaIssue {
	if _, present := columns[table]; !present {
		return nil
	}

	var issues []SchemaIssue

	for _, index := range expected.Indexes {
		if name := prefix + index; !indexes[table][name] {
			issues = append(issues, SchemaIssue{Table: table, Detail: "index " + name + " is missing"})
		}
	}

	return issues
}

// readColumns runs the column introspection into a table to column to
// collation map.
func readColumns(ctx context.Context, querier Querier, statement Statement) (map[string]map[string]string, error) {
	observed := make(map[string]map[string]string)

	err := readText(ctx, querier, statement, "schema", 3, func(values []string) {
		table, column, collation := values[0], values[1], values[2]

		if observed[table] == nil {
			observed[table] = make(map[string]string)
		}

		observed[table][column] = collation
	})

	return observed, err
}

// readIndexes runs the index introspection into a table to index set.
func readIndexes(ctx context.Context, querier Querier, statement Statement) (map[string]map[string]bool, error) {
	observed := make(map[string]map[string]bool)

	err := readText(ctx, querier, statement, "indexes", 2, func(values []string) {
		table, index := values[0], values[1]

		if observed[table] == nil {
			observed[table] = make(map[string]bool)
		}

		observed[table][index] = true
	})

	return observed, err
}

// readText runs an introspection statement whose rows have width text columns,
// handing each row's values to visit.
func readText(
	ctx context.Context, querier Querier, statement Statement, what string, width int, visit func(values []string),
) error {
	rows, err := querier.QueryStatement(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return fmt.Errorf("sqlkit: read the live %s: %w", what, err)
	}

	raw := make([]any, width)
	dest := make([]any, width)
	values := make([]string, width)

	for i := range raw {
		dest[i] = &raw[i]
	}

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("sqlkit: scan %s row: %w", what, err)
		}

		for i, value := range raw {
			text, err := DecodeString(value)
			if err != nil {
				return err
			}

			values[i] = text
		}

		visit(values)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlkit: read %s rows: %w", what, err)
	}

	return nil
}

// bindable turns table names into bind values.
func bindable(tables []string) []any {
	out := make([]any, 0, len(tables))
	for _, table := range tables {
		out = append(out, table)
	}

	return out
}
