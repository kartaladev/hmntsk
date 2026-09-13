package sqlkit

import "strings"

// Statement is one SQL statement and its bind values, ready for an [Executor].
type Statement struct {
	// SQL is the statement text, with the dialect's bind markers already
	// applied.
	SQL string
	// Args are the bind values, in bind-marker order.
	Args []any
}

// IsZero reports whether the statement has no text. A builder returns the zero
// statement when there is nothing to do, and [Executor.Exec] treats it as a
// no-op.
func (s Statement) IsZero() bool { return s.SQL == "" }

// Writer accumulates SQL and its arguments, handing out bind markers in the
// dialect's style as it goes.
//
// Markers are numbered across every call on one writer, so a statement written
// in several steps binds $1, $2, $3 on PostgreSQL in the order the values were
// bound. A Writer is not safe for concurrent use; build one per statement.
type Writer struct {
	dialect Dialect
	sql     strings.Builder
	args    []any
}

// NewWriter returns a writer producing a statement for a dialect.
func NewWriter(dialect Dialect) *Writer { return &Writer{dialect: dialect} }

// Write appends literal SQL. Identifiers are quoted by the caller, with the
// dialect's Quote, before they are written.
func (w *Writer) Write(parts ...string) {
	for _, part := range parts {
		w.sql.WriteString(part)
	}
}

// Bind records a value and returns the bind marker that stands for it.
func (w *Writer) Bind(value any) string {
	w.args = append(w.args, value)

	return w.dialect.Placeholder(len(w.args))
}

// BindAll records several values and returns their bind markers, comma
// separated, for an IN list or a VALUES row. No values render nothing.
func (w *Writer) BindAll(values ...any) string {
	markers := make([]string, 0, len(values))
	for _, value := range values {
		markers = append(markers, w.Bind(value))
	}

	return strings.Join(markers, ", ")
}

// Done returns the accumulated statement.
func (w *Writer) Done() Statement {
	return Statement{SQL: w.sql.String(), Args: w.args}
}

// TrimSQL collapses a statement's whitespace, for readable test failures and
// log lines.
func TrimSQL(sql string) string { return strings.Join(strings.Fields(sql), " ") }
