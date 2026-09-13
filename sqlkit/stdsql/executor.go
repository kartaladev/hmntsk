package stdsqlexec

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kartaladev/hmntsk/sqlkit"
)

// Executor runs statements through a *sql.DB, or through the *sql.Tx a context
// carries.
type Executor struct {
	db      *sql.DB
	dialect sqlkit.Dialect
}

// Compile-time proof that the executor satisfies every contract sqlkit drives.
var (
	_ sqlkit.Executor = (*Executor)(nil)
	_ sqlkit.Execer   = (*Executor)(nil)
	_ sqlkit.Querier  = (*Executor)(nil)
)

// New returns an executor over a database handle and the dialect of the
// database behind it. A nil handle or dialect is a configuration error.
func New(db *sql.DB, dialect sqlkit.Dialect) (*Executor, error) {
	switch {
	case db == nil:
		return nil, &sqlkit.ConfigurationError{Detail: "stdsqlexec: a *sql.DB is required"}
	case dialect == nil:
		return nil, &sqlkit.ConfigurationError{Detail: "stdsqlexec: a dialect is required"}
	default:
		return &Executor{db: db, dialect: dialect}, nil
	}
}

// DB returns the underlying handle.
func (e *Executor) DB() *sql.DB { return e.db }

// Dialect implements [sqlkit.Executor].
func (e *Executor) Dialect() sqlkit.Dialect { return e.dialect }

// contextKey is the private type a transaction travels under.
type contextKey struct{}

// ContextWithTx returns a context carrying an open transaction, so that a
// caller that began one itself can have the executor join it. The executor
// uses this transaction and neither commits nor rolls it back.
//
// The key is this package's own, so a transaction a task store carries is not
// visible here. A host that wants one transaction across both reads it from the
// store's context and passes it here.
func ContextWithTx(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, contextKey{}, tx)
}

// TxFromContext returns the transaction active on a context, if any.
func TxFromContext(ctx context.Context) (*sql.Tx, bool) {
	tx, ok := ctx.Value(contextKey{}).(*sql.Tx)

	return tx, ok && tx != nil
}

// InTransaction implements [sqlkit.Executor].
func (e *Executor) InTransaction(ctx context.Context) bool {
	_, ok := TxFromContext(ctx)

	return ok
}

// Do implements [sqlkit.Executor].
func (e *Executor) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, joined := TxFromContext(ctx); joined {
		return fn(ctx)
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlkit: begin transaction: %w", err)
	}

	committed := false

	defer func() {
		if committed {
			return
		}

		// Rollback on the way out of an error or a panic alike. The panic keeps
		// unwinding afterwards: converting it to an error would hide a bug the
		// caller has to see.
		_ = tx.Rollback()
	}()

	if err := fn(ContextWithTx(ctx, tx)); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlkit: commit transaction: %w", err)
	}

	committed = true

	return nil
}

// handle is the little of a connection the executor needs, satisfied by both
// *sql.DB and *sql.Tx.
type handle interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// handle returns the transaction active on the context, or the pool.
func (e *Executor) handle(ctx context.Context) handle {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}

	return e.db
}

// Exec implements [sqlkit.Executor].
func (e *Executor) Exec(ctx context.Context, statement sqlkit.Statement) (int64, error) {
	if statement.IsZero() {
		return 0, nil
	}

	result, err := e.handle(ctx).ExecContext(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return 0, statementError(err, statement.SQL)
	}

	matched, err := result.RowsAffected()
	if err != nil {
		return 0, statementError(err, statement.SQL)
	}

	return matched, nil
}

// Query implements [sqlkit.Executor].
func (e *Executor) Query(ctx context.Context, statement sqlkit.Statement, scan func(rows sqlkit.Rows) error) error {
	if statement.IsZero() {
		return nil
	}

	rows, err := e.handle(ctx).QueryContext(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return statementError(err, statement.SQL)
	}

	defer func() { _ = rows.Close() }()

	if err := scan(rows); err != nil {
		return err
	}

	if err := rows.Err(); err != nil {
		return statementError(err, statement.SQL)
	}

	return nil
}

// ExecStatement implements [sqlkit.Execer], so that the schema runner can
// drive this executor.
func (e *Executor) ExecStatement(ctx context.Context, query string, args ...any) error {
	if _, err := e.handle(ctx).ExecContext(ctx, query, args...); err != nil {
		return statementError(err, query)
	}

	return nil
}

// QueryStatement implements [sqlkit.Querier], so that schema verification can
// drive this executor.
func (e *Executor) QueryStatement(ctx context.Context, query string, args ...any) (sqlkit.Rows, error) {
	rows, err := e.handle(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, statementError(err, query)
	}

	return rows, nil
}

// statementError names the statement a driver error came from.
func statementError(err error, query string) error {
	return fmt.Errorf("sqlkit: %w\nstatement: %s", err, query)
}
