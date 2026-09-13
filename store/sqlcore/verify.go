package sqlcore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/kartaladev/hmntsk"
)

// identifierColumns are the columns whose comparison must be case-sensitive,
// per table. An actor identifier that matches a candidate differing only in
// case would change who may claim a task, and MySQL's default collation does
// exactly that, so these carry a pinned collation in every published schema.
var identifierColumns = map[string][]string{
	TasksTable: {
		"id", "task_type", "status", "suspended_from", "assignee",
		"owner_type", "owner_ref", "activity_key", "created_by", "locked_by",
	},
	CandidatesTable: {"task_id", "kind", "value"},
	HistoryTable:    {"task_id", "operation", "from_status", "to_status", "actor"},
	// locked_by and accepted_sinks are identifiers for the same reason the
	// tasks table's are: a relay whose owner string matched another's in a
	// different case would take over its lease, and a sink whose name matched
	// another's would be told an event it never took had already been
	// delivered.
	OutboxTable: {"id", "task_id", "task_type", "event_type", "locked_by", "accepted_sinks"},
	TypesTable:  {"name"},
}

// expectedColumns lists every column the engine's statements read or write, per
// table.
var expectedColumns = map[string][]string{
	TasksTable:      taskColumns,
	CandidatesTable: {"task_id", "kind", "value", "ordinal"},
	HistoryTable:    historyColumns,
	OutboxTable:     outboxColumns,
	TypesTable:      typeColumns,
}

// SchemaIssue is one discrepancy between the live schema and what the engine's
// statements require.
type SchemaIssue struct {
	// Table is the table the issue concerns, prefix applied.
	Table string
	// Column is the column, empty when the whole table is the problem.
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
	// Issues is every discrepancy found, in table and column order.
	Issues []SchemaIssue
}

// Error implements the error interface.
func (e *SchemaError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.String())
	}

	return fmt.Sprintf("hmntsk: the %s schema does not match what the engine requires: %s",
		e.Dialect, strings.Join(parts, "; "))
}

// Unwrap makes the error match [hmntsk.ErrConfiguration], because a schema the
// engine cannot run against is a wiring problem, discovered at startup.
func (e *SchemaError) Unwrap() error { return hmntsk.ErrConfiguration }

// SchemaQuery renders the introspection the dialect offers: one row per column
// of the engine's tables, as (table, column, collation).
func (b *Builder) SchemaQuery() Statement {
	s := b.begin()

	tables := b.tableArgs()

	switch b.dialect.Name() {
	case "postgres":
		s.write("SELECT table_name, column_name, COALESCE(collation_name, '') ")
		s.write("FROM information_schema.columns ")
		s.write("WHERE table_schema = current_schema() AND table_name IN (", s.bindAll(tables...), ")")
	case "mysql":
		s.write("SELECT TABLE_NAME, COLUMN_NAME, COALESCE(COLLATION_NAME, '') ")
		s.write("FROM information_schema.COLUMNS ")
		s.write("WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (", s.bindAll(tables...), ")")
	default:
		// SQLite exposes no collation through introspection. It does not need
		// to: BINARY is the default and the only way to lose it is to override
		// it per column, which the published schema never does. Verification
		// therefore checks that the tables and columns are there, and treats an
		// unreported collation as the default.
		s.write("SELECT m.name, p.name, '' ")
		s.write("FROM sqlite_master m JOIN pragma_table_info(m.name) p ")
		s.write("WHERE m.type = 'table' AND m.name IN (", s.bindAll(tables...), ")")
	}

	return s.done()
}

// VerifySchema compares the live schema against what the engine's statements
// require, and reports every discrepancy rather than failing on first use.
//
// It checks that every table and column exists, that the identifier columns
// carry the collation that makes comparison case-sensitive, and that every
// index the engine's statements rely on exists, by name. It does not check
// column types: a dialect has several spellings for the same storage, and a
// type mismatch that matters shows up as a failing statement immediately, while
// a wrong collation shows up months later as the wrong person claiming a task.
func (b *Builder) VerifySchema(ctx context.Context, querier Querier) error {
	statement := b.SchemaQuery()

	rows, err := querier.QueryStatement(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return fmt.Errorf("hmntsk: read the live schema: %w", err)
	}

	observed, err := scanSchema(rows)
	if err != nil {
		return err
	}

	indexStatement := b.IndexQuery()

	indexRows, err := querier.QueryStatement(ctx, indexStatement.SQL, indexStatement.Args...)
	if err != nil {
		return fmt.Errorf("hmntsk: read the live indexes: %w", err)
	}

	indexes, err := scanIndexes(indexRows)
	if err != nil {
		return err
	}

	issues := append(b.compareSchema(observed), b.compareIndexes(observed, indexes)...)
	if len(issues) == 0 {
		return nil
	}

	return &SchemaError{Dialect: b.dialect.Name(), Issues: issues}
}

// observedColumn is one column as the database reports it.
type observedColumn struct {
	collation string
}

// scanSchema reads the introspection rows into a table-and-column map.
func scanSchema(rows Rows) (map[string]map[string]observedColumn, error) {
	observed := make(map[string]map[string]observedColumn)

	var table, column, collation any

	for rows.Next() {
		if err := rows.Scan(&table, &column, &collation); err != nil {
			return nil, fmt.Errorf("hmntsk: scan schema row: %w", err)
		}

		tableName, err := DecodeString(table)
		if err != nil {
			return nil, err
		}

		columnName, err := DecodeString(column)
		if err != nil {
			return nil, err
		}

		collationName, err := DecodeString(collation)
		if err != nil {
			return nil, err
		}

		if observed[tableName] == nil {
			observed[tableName] = make(map[string]observedColumn)
		}

		observed[tableName][columnName] = observedColumn{collation: collationName}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hmntsk: read schema rows: %w", err)
	}

	return observed, nil
}

// compareSchema produces one issue per discrepancy, in a stable order.
func (b *Builder) compareSchema(observed map[string]map[string]observedColumn) []SchemaIssue {
	var issues []SchemaIssue

	wantCollation := b.dialect.IdentifierCollation()

	tables := make([]string, 0, len(expectedColumns))
	for table := range expectedColumns {
		tables = append(tables, table)
	}

	sort.Strings(tables)

	for _, table := range tables {
		prefixed := b.TableName(table)

		columns, present := observed[prefixed]
		if !present {
			issues = append(issues, SchemaIssue{Table: prefixed, Detail: "table is missing"})

			continue
		}

		for _, column := range expectedColumns[table] {
			found, ok := columns[column]
			if !ok {
				issues = append(issues, SchemaIssue{
					Table: prefixed, Column: column, Detail: "column is missing",
				})

				continue
			}

			if !isIdentifierColumn(table, column) || wantCollation == "" {
				continue
			}

			// An unreported collation is the dialect's default, which is
			// correct everywhere the engine can ask.
			if found.collation == "" || found.collation == wantCollation {
				continue
			}

			issues = append(issues, SchemaIssue{
				Table: prefixed, Column: column,
				Detail: fmt.Sprintf(
					"collation is %q but must be %q, or identifiers will compare case-insensitively",
					found.collation, wantCollation,
				),
			})
		}
	}

	return issues
}

// IndexQuery renders the introspection of the indexes on the engine's tables:
// one row per index, as (table, index).
func (b *Builder) IndexQuery() Statement {
	s := b.begin()

	tables := b.tableArgs()

	switch b.dialect.Name() {
	case "postgres":
		s.write("SELECT tablename, indexname FROM pg_indexes ")
		s.write("WHERE schemaname = current_schema() AND tablename IN (", s.bindAll(tables...), ")")
	case "mysql":
		// STATISTICS has one row per indexed column, hence DISTINCT.
		s.write("SELECT DISTINCT TABLE_NAME, INDEX_NAME FROM information_schema.STATISTICS ")
		s.write("WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (", s.bindAll(tables...), ")")
	default:
		s.write("SELECT tbl_name, name FROM sqlite_master ")
		s.write("WHERE type = 'index' AND tbl_name IN (", s.bindAll(tables...), ")")
	}

	return s.done()
}

// Indexes returns the indexes [Builder.VerifySchema] requires, prefixed, in
// table creation order. Every one of them is created by the published DDL of
// every dialect.
func (b *Builder) Indexes() []string {
	var indexes []string

	for _, table := range tableOrder {
		for _, index := range expectedIndexes[table] {
			indexes = append(indexes, b.TableName(index))
		}
	}

	return indexes
}

// expectedIndexes lists, per table, the secondary indexes the engine's
// statements rely on. Primary keys are not listed: every dialect names them
// differently, and a table without one is not a table the published DDL made.
//
// An index is required by name, not by its columns. A missing index does not
// make a statement fail; it makes the statement read the whole table, which is
// found in production rather than at startup unless something looks for it.
var expectedIndexes = map[string][]string{
	TasksTable: {
		"tasks_assignee_idx", "tasks_status_idx", "tasks_type_idx", "tasks_correlation_idx",
		// The escalation sweep's: it filters overdue tasks by status.
		"tasks_due_idx",
		// The inbox orderings'.
		"tasks_priority_idx", "tasks_due_order_idx", "tasks_urgency_idx",
	},
	CandidatesTable: {"task_candidates_lookup_idx"},
	OutboxTable:     {"task_outbox_unpublished_idx", "task_outbox_due_idx"},
}

// scanIndexes reads the index introspection rows into a table-and-index set.
func scanIndexes(rows Rows) (map[string]map[string]bool, error) {
	observed := make(map[string]map[string]bool)

	var table, index any

	for rows.Next() {
		if err := rows.Scan(&table, &index); err != nil {
			return nil, fmt.Errorf("hmntsk: scan index row: %w", err)
		}

		tableName, err := DecodeString(table)
		if err != nil {
			return nil, err
		}

		indexName, err := DecodeString(index)
		if err != nil {
			return nil, err
		}

		if observed[tableName] == nil {
			observed[tableName] = make(map[string]bool)
		}

		observed[tableName][indexName] = true
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hmntsk: read index rows: %w", err)
	}

	return observed, nil
}

// compareIndexes produces one issue per missing index, in a stable order. A
// table that is missing altogether is already reported, and its indexes are not
// reported again.
func (b *Builder) compareIndexes(
	columns map[string]map[string]observedColumn, indexes map[string]map[string]bool,
) []SchemaIssue {
	var issues []SchemaIssue

	for _, table := range slices.Sorted(maps.Keys(expectedIndexes)) {
		prefixed := b.TableName(table)

		if _, present := columns[prefixed]; !present {
			continue
		}

		for _, index := range expectedIndexes[table] {
			name := b.TableName(index)

			if !indexes[prefixed][name] {
				issues = append(issues, SchemaIssue{Table: prefixed, Detail: "index " + name + " is missing"})
			}
		}
	}

	return issues
}

// isIdentifierColumn reports whether a column must compare case-sensitively.
func isIdentifierColumn(table, column string) bool {
	for _, candidate := range identifierColumns[table] {
		if candidate == column {
			return true
		}
	}

	return false
}
