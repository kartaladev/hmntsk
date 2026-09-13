package pgxexec

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kartaladev/hmntsk/sqlkit"
)

// Executor runs statements through a pgx pool, or through the pgx.Tx a context
// carries.
type Executor struct {
	pool *pgxpool.Pool
}

// Compile-time proof that the executor satisfies every contract sqlkit drives.
var (
	_ sqlkit.Executor = (*Executor)(nil)
	_ sqlkit.Execer   = (*Executor)(nil)
	_ sqlkit.Querier  = (*Executor)(nil)
)

// New returns an executor over a pgx pool. A nil pool is a configuration error.
// It does not connect: the pool dials when a statement first needs it.
func New(pool *pgxpool.Pool) (*Executor, error) {
	if pool == nil {
		return nil, &sqlkit.ConfigurationError{Detail: "pgxexec: a *pgxpool.Pool is required"}
	}

	return &Executor{pool: pool}, nil
}

// Pool returns the underlying pool.
func (e *Executor) Pool() *pgxpool.Pool { return e.pool }

// Dialect implements [sqlkit.Executor]. It is always [sqlkit.PostgreSQL].
func (e *Executor) Dialect() sqlkit.Dialect { return sqlkit.PostgreSQL }

// contextKey is the private type a transaction travels under.
type contextKey struct{}

// ContextWithTx returns a context carrying an open pgx transaction, so that a
// caller that began one itself can have the executor join it. The executor
// uses this transaction and neither commits nor rolls it back.
//
// The key is this package's own, so a transaction a task store carries is not
// visible here. A host that wants one transaction across both reads it from the
// store's context and passes it here.
func ContextWithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, contextKey{}, tx)
}

// TxFromContext returns the transaction active on a context, if any.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(contextKey{}).(pgx.Tx)

	return tx, ok && tx != nil
}

// InTransaction implements [sqlkit.Executor].
func (e *Executor) InTransaction(ctx context.Context) bool {
	_, ok := TxFromContext(ctx)

	return ok
}

// Do implements [sqlkit.Executor].
//
// A nested call joins and flattens rather than opening the savepoint that
// pgx.Tx.Begin would give.
func (e *Executor) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, joined := TxFromContext(ctx); joined {
		return fn(ctx)
	}

	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sqlkit: begin transaction: %w", err)
	}

	committed := false

	defer func() {
		if committed {
			return
		}

		// Rollback needs a live context: the one the caller gave may be exactly
		// the thing that went wrong.
		rollbackCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()

		_ = tx.Rollback(rollbackCtx)
	}()

	if err := fn(ContextWithTx(ctx, tx)); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("sqlkit: commit transaction: %w", err)
	}

	committed = true

	return nil
}

// handle is the little of a connection the executor needs, satisfied by both
// *pgxpool.Pool and pgx.Tx.
type handle interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// handle returns the transaction active on the context, or the pool.
func (e *Executor) handle(ctx context.Context) handle {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}

	return e.pool
}

// Exec implements [sqlkit.Executor].
func (e *Executor) Exec(ctx context.Context, statement sqlkit.Statement) (int64, error) {
	if statement.IsZero() {
		return 0, nil
	}

	tag, err := e.handle(ctx).Exec(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return 0, statementError(err, statement.SQL)
	}

	return tag.RowsAffected(), nil
}

// Query implements [sqlkit.Executor].
//
// pgx reports most server errors on the rows rather than from Query itself, so
// the rows' error is checked after they are closed, whether or not scan read
// them.
func (e *Executor) Query(ctx context.Context, statement sqlkit.Statement, scan func(rows sqlkit.Rows) error) error {
	if statement.IsZero() {
		return nil
	}

	rows, err := e.handle(ctx).Query(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return statementError(err, statement.SQL)
	}

	defer rows.Close()

	if err := scan(rows); err != nil {
		return err
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return statementError(err, statement.SQL)
	}

	return nil
}

// ExecStatement implements [sqlkit.Execer].
func (e *Executor) ExecStatement(ctx context.Context, sql string, args ...any) error {
	if _, err := e.handle(ctx).Exec(ctx, sql, args...); err != nil {
		return statementError(err, sql)
	}

	return nil
}

// QueryStatement implements [sqlkit.Querier].
func (e *Executor) QueryStatement(ctx context.Context, sql string, args ...any) (sqlkit.Rows, error) {
	rows, err := e.handle(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, statementError(err, sql)
	}

	return rows, nil
}

// statementError names the statement a driver error came from.
func statementError(err error, sql string) error {
	return fmt.Errorf("sqlkit: %w\nstatement: %s", err, sql)
}
