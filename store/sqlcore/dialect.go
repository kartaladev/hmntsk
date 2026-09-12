// Package sqlcore builds the SQL the hmntsk engine needs and executes none of
// it.
//
// Driver and dialect are orthogonal. database/sql, pgx and GORM are drivers;
// PostgreSQL, MySQL and SQLite are dialects. This package owns everything that
// varies by dialect — placeholder style, quoting, column types, what the
// database can and cannot do — and produces statements with their arguments.
// The driver modules own execution, scanning and transaction participation, and
// contain no decisions at all.
//
// Putting the SQL in the driver modules instead would mean writing PostgreSQL
// three times over while still not supporting MySQL.
package sqlcore

import (
	"strconv"
	"strings"
)

// Dialect is everything about a database that changes the SQL.
//
// It deliberately exposes capabilities as questions rather than letting
// statement builders test the dialect's name: a builder that asks
// SupportsSkipLocked works for a dialect added later, and one that asks whether
// it is talking to PostgreSQL does not.
type Dialect interface {
	// Name identifies the dialect in errors and in migration file names.
	Name() string
	// Placeholder renders the bind marker for the one-based position of an
	// argument: $1 on PostgreSQL, ? on the others.
	Placeholder(position int) string
	// Quote renders an identifier so that a column named "type" or "from" is
	// not a syntax error.
	Quote(identifier string) string
	// SupportsReturning reports whether the dialect can return rows from a
	// write. Nothing in this package relies on it — MySQL cannot — but
	// VerifySchema and diagnostics report it.
	SupportsReturning() bool
	// SupportsSkipLocked reports whether SELECT ... FOR UPDATE SKIP LOCKED is
	// available. It never changes what the engine does, only whether an
	// optimisation is offered: exclusive claiming is done by a conditional
	// update on the lease columns, because SQLite has no row-level locking at
	// all.
	SupportsSkipLocked() bool
	// IdentifierCollation is the collation that actor, group and task-type
	// columns must carry for comparison to be case-sensitive. It is empty when
	// the dialect is case-sensitive by default and needs nothing pinned.
	IdentifierCollation() string
	// TimestampColumnType is the column type for a UTC instant stored at
	// microsecond precision.
	TimestampColumnType() string
	// JSONColumnType is the column type for an opaque JSON payload.
	//
	// It is a text type on every dialect, never a native JSON type. PostgreSQL
	// jsonb and MySQL JSON both normalise what they are given — they reorder
	// object keys, drop insignificant whitespace and rewrite number literals —
	// and the engine promises to give a payload back exactly as it was handed
	// over, fields the schema does not describe included. Nothing queries
	// inside a payload, so the native types buy nothing to set against that.
	JSONColumnType() string
	// BooleanTrue renders the literal the dialect stores for true, used only
	// where a boolean must appear inline in generated DDL.
	BooleanTrue() string
	// UpsertSuffix renders the clause that turns an INSERT into an insert-or-
	// update on conflict with keyColumns, refreshing updateColumns. The three
	// dialects spell this three different ways and one of them does not name
	// the key at all.
	UpsertSuffix(keyColumns, updateColumns []string) string
}

// The three supported dialects. They are values, not constructors, because a
// dialect holds no state.
var (
	// PostgreSQL is the PostgreSQL dialect, 9.5 or later.
	PostgreSQL Dialect = postgres{}
	// MySQL is the MySQL dialect, 8.0 or later.
	MySQL Dialect = mysql{}
	// SQLite is the SQLite dialect, 3.35 or later.
	SQLite Dialect = sqlite{}
)

// Dialects returns the supported dialects, in a stable order.
func Dialects() []Dialect { return []Dialect{PostgreSQL, MySQL, SQLite} }

// DialectByName returns the dialect registered under name, and false when there
// is none. Names are lower-case: "postgres", "mysql", "sqlite".
func DialectByName(name string) (Dialect, bool) {
	for _, dialect := range Dialects() {
		if dialect.Name() == name {
			return dialect, true
		}
	}

	return nil, false
}

// postgres is the PostgreSQL dialect.
type postgres struct{}

func (postgres) Name() string { return "postgres" }

func (postgres) Placeholder(position int) string { return "$" + strconv.Itoa(position) }

func (postgres) Quote(identifier string) string { return quoteWith(identifier, '"') }

func (postgres) SupportsReturning() bool { return true }

func (postgres) SupportsSkipLocked() bool { return true }

// IdentifierCollation pins the C collation. PostgreSQL already compares
// case-sensitively, so this is not about equality: it is about ordering. A
// locale-aware collation can sort "a-b" before "ab", and task identifiers
// contain hyphens, which would make keyset pagination skip or repeat rows.
func (postgres) IdentifierCollation() string { return "C" }

func (postgres) TimestampColumnType() string { return "timestamptz(6)" }

func (postgres) JSONColumnType() string { return "text" }

func (postgres) BooleanTrue() string { return "TRUE" }

func (d postgres) UpsertSuffix(keyColumns, updateColumns []string) string {
	return onConflictSuffix(d, keyColumns, updateColumns)
}

// mysql is the MySQL dialect.
type mysql struct{}

func (mysql) Name() string { return "mysql" }

func (mysql) Placeholder(int) string { return "?" }

func (mysql) Quote(identifier string) string { return quoteWith(identifier, '`') }

// SupportsReturning is false: MySQL has no RETURNING clause, which is why every
// write in this package is a conditional update followed by a rows-affected
// check rather than a write that reports what it did.
func (mysql) SupportsReturning() bool { return false }

func (mysql) SupportsSkipLocked() bool { return true }

// IdentifierCollation pins case sensitivity. MySQL's default,
// utf8mb4_0900_ai_ci, is case-insensitive, so without this an actor called
// "alice" would match a candidate called "Alice" and the set of people who may
// claim a task would quietly differ on one dialect out of three.
func (mysql) IdentifierCollation() string { return "utf8mb4_0900_as_cs" }

// TimestampColumnType is DATETIME rather than TIMESTAMP: TIMESTAMP converts
// through the session time zone and runs out of range in 2038.
func (mysql) TimestampColumnType() string { return "DATETIME(6)" }

func (mysql) JSONColumnType() string { return "LONGTEXT" }

func (mysql) BooleanTrue() string { return "1" }

// UpsertSuffix uses ON DUPLICATE KEY UPDATE, which names no key columns: MySQL
// applies it to whichever unique index the insert collided with.
func (d mysql) UpsertSuffix(_, updateColumns []string) string {
	assignments := make([]string, 0, len(updateColumns))
	for _, column := range updateColumns {
		quoted := d.Quote(column)
		assignments = append(assignments, quoted+" = VALUES("+quoted+")")
	}

	return " ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", ")
}

// sqlite is the SQLite dialect.
type sqlite struct{}

func (sqlite) Name() string { return "sqlite" }

func (sqlite) Placeholder(int) string { return "?" }

func (sqlite) Quote(identifier string) string { return quoteWith(identifier, '"') }

func (sqlite) SupportsReturning() bool { return true }

// SupportsSkipLocked is false, and that is the reason the whole engine leans on
// optimistic concurrency: SQLite has no row-level locking to skip.
func (sqlite) SupportsSkipLocked() bool { return false }

func (sqlite) IdentifierCollation() string { return "BINARY" }

// TimestampColumnType is TEXT because SQLite has no native date type. The
// engine writes one fixed encoding into it, so ordering and comparison work
// lexically.
func (sqlite) TimestampColumnType() string { return "TEXT" }

func (sqlite) JSONColumnType() string { return "TEXT" }

func (sqlite) BooleanTrue() string { return "1" }

func (d sqlite) UpsertSuffix(keyColumns, updateColumns []string) string {
	return onConflictSuffix(d, keyColumns, updateColumns)
}

// onConflictSuffix renders the SQL-standard ON CONFLICT clause that PostgreSQL
// and SQLite share.
func onConflictSuffix(d Dialect, keyColumns, updateColumns []string) string {
	keys := make([]string, 0, len(keyColumns))
	for _, column := range keyColumns {
		keys = append(keys, d.Quote(column))
	}

	assignments := make([]string, 0, len(updateColumns))
	for _, column := range updateColumns {
		quoted := d.Quote(column)
		assignments = append(assignments, quoted+" = EXCLUDED."+quoted)
	}

	return " ON CONFLICT (" + strings.Join(keys, ", ") + ") DO UPDATE SET " +
		strings.Join(assignments, ", ")
}

// quoteWith wraps an identifier in the given quote character, doubling any
// occurrence of it inside.
func quoteWith(identifier string, quote byte) string {
	out := make([]byte, 0, len(identifier)+2)
	out = append(out, quote)

	for i := range len(identifier) {
		if identifier[i] == quote {
			out = append(out, quote)
		}

		out = append(out, identifier[i])
	}

	return string(append(out, quote))
}
