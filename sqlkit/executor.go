package sqlkit

import "context"

// Rows is the little a scanner needs of a driver's result set. database/sql,
// pgx and GORM all satisfy it.
type Rows interface {
	// Next advances to the next row.
	Next() bool
	// Scan reads the current row into dest.
	Scan(dest ...any) error
	// Err reports any error that ended the iteration.
	Err() error
}

// Execer is the little of a connection the development schema runner needs.
type Execer interface {
	// ExecStatement runs a statement that returns no rows.
	ExecStatement(ctx context.Context, sql string, args ...any) error
}

// Querier is the little of a connection schema verification needs.
type Querier interface {
	// QueryStatement runs a statement and returns its rows. The caller reads
	// them to the end, which releases them on every supported driver.
	QueryStatement(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Executor runs statements for a store written once against sqlkit, on
// whichever driver the host uses.
//
// The stdsqlexec, pgxexec and gormexec modules implement it, and one
// conformance suite in sqlkittest proves they behave identically. Any other
// type satisfying it works with every sqlkit-based store.
//
// Every executor also implements [Execer] and [Querier], so the schema runner
// and verification can drive it.
type Executor interface {
	// Dialect is the dialect statements for this executor must be built with,
	// bind marker included: the GORM executor reports "?" on every database,
	// because GORM rewrites it for the dialector itself.
	Dialect() Dialect
	// Exec runs a write and returns the number of rows it matched. The zero
	// Statement is a no-op returning 0.
	Exec(ctx context.Context, statement Statement) (int64, error)
	// Query runs a read and hands its rows to scan. The executor releases the
	// rows and checks their error after scan returns, whether scan succeeded or
	// not, so a store can never leak a connection by forgetting to. The zero
	// Statement is a no-op that never calls scan.
	Query(ctx context.Context, statement Statement, scan func(rows Rows) error) error
	// Do runs fn in a transaction.
	//
	// When ctx already carries a transaction for this executor, fn joins it
	// and Do neither commits nor rolls back: whoever begins, commits. Nested
	// scopes flatten into that one transaction, never a savepoint, so a failure
	// anywhere aborts the whole scope once the error reaches the outermost Do.
	// Otherwise Do begins a transaction, commits it when fn returns nil and the
	// context is still live, and rolls it back on an error, a panic (re-raised
	// unchanged) or a cancelled context.
	Do(ctx context.Context, fn func(ctx context.Context) error) error
	// InTransaction reports whether ctx carries a transaction for this
	// executor.
	InTransaction(ctx context.Context) bool
}
