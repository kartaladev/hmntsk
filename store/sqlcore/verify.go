package sqlcore

import (
	"context"
	"errors"
	"fmt"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/sqlkit"
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

// schemaExpectation is what the engine's statements require of the live
// schema, in the shape sqlkit verifies against.
var schemaExpectation = func() sqlkit.SchemaExpectation {
	expectation := make(sqlkit.SchemaExpectation, len(tableOrder))

	for _, table := range tableOrder {
		expectation[table] = sqlkit.TableExpectation{
			Columns:           expectedColumns[table],
			IdentifierColumns: identifierColumns[table],
			Indexes:           expectedIndexes[table],
		}
	}

	return expectation
}()

// SchemaIssue is one discrepancy between the live schema and what the engine's
// statements require. It is [sqlkit.SchemaIssue].
type SchemaIssue = sqlkit.SchemaIssue

// SchemaError reports every discrepancy found, not only the first, in the
// engine's words.
//
// It carries sqlkit's error, whose Dialect and Issues it exposes, and matches
// both [hmntsk.ErrConfiguration] and [sqlkit.ErrConfiguration]: a schema the
// engine cannot run against is a wiring problem, discovered at startup.
type SchemaError struct {
	*sqlkit.SchemaError
}

// Error implements the error interface.
func (e *SchemaError) Error() string {
	return fmt.Sprintf("hmntsk: the %s schema does not match what the engine requires: %s",
		e.Dialect, e.IssueList())
}

// Unwrap makes the error match sqlkit's error and [hmntsk.ErrConfiguration].
func (e *SchemaError) Unwrap() []error {
	return []error{e.SchemaError, hmntsk.ErrConfiguration}
}

// SchemaQuery renders the introspection the dialect offers: one row per column
// of the engine's tables, as (table, column, collation).
func (b *Builder) SchemaQuery() Statement { return sqlkit.SchemaQuery(b.dialect, b.Tables()) }

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
	err := sqlkit.VerifySchema(ctx, querier, b.dialect, b.prefix, schemaExpectation)

	var mismatch *sqlkit.SchemaError

	switch {
	case err == nil:
		return nil
	case errors.As(err, &mismatch):
		return &SchemaError{SchemaError: mismatch}
	default:
		return fmt.Errorf("hmntsk: verify the schema: %w", err)
	}
}

// IndexQuery renders the introspection of the indexes on the engine's tables:
// one row per index, as (table, index).
func (b *Builder) IndexQuery() Statement { return sqlkit.IndexQuery(b.dialect, b.Tables()) }

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
