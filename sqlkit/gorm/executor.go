package gormexec

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/kartaladev/hmntsk/sqlkit"
)

// Executor runs raw statements through a *gorm.DB, or through the GORM
// transaction a context carries.
type Executor struct {
	db      *gorm.DB
	dialect sqlkit.Dialect
}

// Compile-time proof that the executor satisfies every contract sqlkit drives.
var (
	_ sqlkit.Executor = (*Executor)(nil)
	_ sqlkit.Execer   = (*Executor)(nil)
	_ sqlkit.Querier  = (*Executor)(nil)
)

// questionMarks wraps a dialect so that statements come out with GORM's own
// bind marker.
//
// GORM rewrites ? into whatever the dialector needs — $1 on PostgreSQL — while
// building a statement, so a statement that already carried $1 would reach the
// server with nothing bound to it. This changes the marker and nothing else.
type questionMarks struct{ sqlkit.Dialect }

// Placeholder implements [sqlkit.Dialect].
func (questionMarks) Placeholder(int) string { return "?" }

// New returns an executor over a GORM handle and the dialect of the database
// behind it. A nil handle or dialect is a configuration error.
func New(db *gorm.DB, dialect sqlkit.Dialect) (*Executor, error) {
	switch {
	case db == nil:
		return nil, &sqlkit.ConfigurationError{Detail: "gormexec: a *gorm.DB is required"}
	case dialect == nil:
		return nil, &sqlkit.ConfigurationError{Detail: "gormexec: a dialect is required"}
	default:
		return &Executor{db: db, dialect: questionMarks{Dialect: dialect}}, nil
	}
}

// DB returns the underlying handle.
func (e *Executor) DB() *gorm.DB { return e.db }

// Dialect implements [sqlkit.Executor]. It is the dialect given to [New] with
// GORM's "?" bind marker, so a statement built with it binds correctly on
// every database.
func (e *Executor) Dialect() sqlkit.Dialect { return e.dialect }

// contextKey is the private type a transaction travels under.
type contextKey struct{}

// ContextWithTx returns a context carrying an open GORM transaction, so that a
// caller that began one itself can have the executor join it. The executor
// uses this transaction and neither commits nor rolls it back.
//
// The key is this package's own, so a transaction a task store carries is not
// visible here. A host that wants one transaction across both reads it from the
// store's context and passes it here.
func ContextWithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, contextKey{}, tx)
}

// TxFromContext returns the transaction active on a context, if any.
func TxFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(contextKey{}).(*gorm.DB)

	return tx, ok && tx != nil
}

// InTransaction implements [sqlkit.Executor].
func (e *Executor) InTransaction(ctx context.Context) bool {
	_, ok := TxFromContext(ctx)

	return ok
}

// Do implements [sqlkit.Executor].
//
// This is the executor's one real decision, and it is a decision to refuse
// GORM's. db.Transaction nests with a SAVEPOINT, so an inner scope that fails
// rolls back to the savepoint and leaves the outer transaction alive — a caller
// that catches the inner error and carries on then commits half a change. Every
// other executor flattens, and so does this one: it begins a transaction itself
// and joins every nested call into it. db.Transaction is never called.
func (e *Executor) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, joined := TxFromContext(ctx); joined {
		return fn(ctx)
	}

	tx := e.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("sqlkit: begin transaction: %w", tx.Error)
	}

	committed := false

	defer func() {
		if committed {
			return
		}

		// Rollback on an error or a panic alike. A panic keeps unwinding
		// afterwards: turning it into an error would hide a bug the caller has
		// to see.
		tx.Rollback()
	}()

	if err := fn(ContextWithTx(ctx, tx)); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("sqlkit: commit transaction: %w", err)
	}

	committed = true

	return nil
}

// handle returns the transaction active on the context, or the handle bound to
// the context.
func (e *Executor) handle(ctx context.Context) *gorm.DB {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}

	return e.db.WithContext(ctx)
}

// Exec implements [sqlkit.Executor].
func (e *Executor) Exec(ctx context.Context, statement sqlkit.Statement) (int64, error) {
	if statement.IsZero() {
		return 0, nil
	}

	result := e.handle(ctx).Exec(statement.SQL, statement.Args...)
	if result.Error != nil {
		return 0, statementError(result.Error, statement.SQL)
	}

	return result.RowsAffected, nil
}

// Query implements [sqlkit.Executor].
func (e *Executor) Query(ctx context.Context, statement sqlkit.Statement, scan func(rows sqlkit.Rows) error) error {
	if statement.IsZero() {
		return nil
	}

	rows, err := e.handle(ctx).Raw(statement.SQL, statement.Args...).Rows()
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

// ExecStatement implements [sqlkit.Execer].
func (e *Executor) ExecStatement(ctx context.Context, sql string, args ...any) error {
	if err := e.handle(ctx).Exec(sql, args...).Error; err != nil {
		return statementError(err, sql)
	}

	return nil
}

// QueryStatement implements [sqlkit.Querier].
func (e *Executor) QueryStatement(ctx context.Context, sql string, args ...any) (sqlkit.Rows, error) {
	rows, err := e.handle(ctx).Raw(sql, args...).Rows()
	if err != nil {
		return nil, statementError(err, sql)
	}

	return rows, nil
}

// statementError names the statement a driver error came from.
func statementError(err error, sql string) error {
	return fmt.Errorf("sqlkit: %w\nstatement: %s", err, sql)
}
