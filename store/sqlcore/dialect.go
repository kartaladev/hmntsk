// Package sqlcore builds the SQL the hmntsk engine needs and executes none of
// it.
//
// Driver and dialect are orthogonal. database/sql, pgx and GORM are drivers;
// PostgreSQL, MySQL and SQLite are dialects. Everything that varies by dialect
// but not by domain — placeholder style, quoting, column types, what the
// database can and cannot do, the codecs, schema rendering and verification —
// lives in sqlkit, and this package re-exports the parts the driver modules
// already use. What remains here is the engine's own: its tables, its
// statements with their arguments, and its scanners. The driver modules own
// execution, scanning and transaction participation, and contain no decisions
// at all.
//
// Putting the SQL in the driver modules instead would mean writing PostgreSQL
// three times over while still not supporting MySQL.
package sqlcore

import "github.com/kartaladev/hmntsk/sqlkit"

// Dialect is everything about a database that changes the SQL. It is
// [sqlkit.Dialect], which owns it; the alias keeps the driver modules compiling
// unchanged.
type Dialect = sqlkit.Dialect

// The three supported dialects, owned by sqlkit.
var (
	// PostgreSQL is [sqlkit.PostgreSQL].
	PostgreSQL = sqlkit.PostgreSQL
	// MySQL is [sqlkit.MySQL].
	MySQL = sqlkit.MySQL
	// SQLite is [sqlkit.SQLite].
	SQLite = sqlkit.SQLite
)

// Dialects returns the supported dialects, in a stable order. It is
// [sqlkit.Dialects].
func Dialects() []Dialect { return sqlkit.Dialects() }

// DialectByName returns the dialect registered under name, and false when there
// is none. It is [sqlkit.DialectByName].
func DialectByName(name string) (Dialect, bool) { return sqlkit.DialectByName(name) }
