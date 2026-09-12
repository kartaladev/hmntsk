// Package pgxstore adapts the hmntsk engine to jackc/pgx.
//
// It executes the statements store/sqlcore builds against a pgxpool.Pool and
// joins the host's pgx.Tx. Like every adapter in this repository it contains no
// SQL and makes no decisions; the storetest conformance suite pins that it
// behaves identically to the others.
//
// pgx is PostgreSQL-only, which is why the driver-by-dialect matrix is sparse:
// seven combinations, not nine.
package pgxstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// Store is a repository, a transactor and an event sink over a pgx pool.
type Store struct {
	pool    *pgxpool.Pool
	builder *sqlcore.Builder
}

// Compile-time proof that the adapter satisfies the whole contract.
var _ hmntsk.Store = (*Store)(nil)

// Option configures a [Store].
type Option func(*config)

// config collects the options before the store is built.
type config struct {
	prefix string
}

// WithTablePrefix prefixes every table the engine owns. It must match the
// prefix the schema was created with.
func WithTablePrefix(prefix string) Option {
	return func(c *config) { c.prefix = prefix }
}

// New returns a store over a pgx pool.
func New(pool *pgxpool.Pool, opts ...Option) *Store {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Store{
		pool:    pool,
		builder: sqlcore.New(sqlcore.PostgreSQL, sqlcore.WithTablePrefix(cfg.prefix)),
	}
}

// Builder returns the statement builder this store executes.
func (s *Store) Builder() *sqlcore.Builder { return s.builder }

// Pool returns the underlying pool.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// contextKey is the private type a transaction travels under.
type contextKey struct{}

// ContextWithTx returns a context carrying an open pgx transaction, so that a
// host that began one itself can have the engine join it.
func ContextWithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, contextKey{}, tx)
}

// TxFromContext returns the transaction active on a context, if any.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(contextKey{}).(pgx.Tx)

	return tx, ok
}

// InTransaction implements [hmntsk.Transactor].
func (s *Store) InTransaction(ctx context.Context) bool {
	_, ok := TxFromContext(ctx)

	return ok
}

// Do implements [hmntsk.Transactor].
//
// A nested call joins and flattens rather than opening a savepoint, which is
// what pgx.Tx.Begin would give. An inner failure therefore aborts the whole
// scope, a panic rolls back and keeps unwinding, and a cancelled context rolls
// back rather than committing work whose caller has gone.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, joined := TxFromContext(ctx); joined {
		return fn(ctx)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("hmntsk: begin transaction: %w", err)
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
		return fmt.Errorf("hmntsk: commit transaction: %w", err)
	}

	committed = true

	return nil
}

// querier is the little of a handle the adapter needs, satisfied by both
// *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// handle returns the transaction active on the context, or the pool.
func (s *Store) handle(ctx context.Context) querier {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}

	return s.pool
}

// exec runs a statement, skipping one the builder declined to produce.
func (s *Store) exec(ctx context.Context, statement sqlcore.Statement) (int64, error) {
	if statement.IsZero() {
		return 0, nil
	}

	tag, err := s.handle(ctx).Exec(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return 0, fmt.Errorf("hmntsk: %w\nstatement: %s", err, statement.SQL)
	}

	return tag.RowsAffected(), nil
}

// query runs a statement that returns rows.
func (s *Store) query(ctx context.Context, statement sqlcore.Statement) (pgx.Rows, error) {
	rows, err := s.handle(ctx).Query(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return nil, fmt.Errorf("hmntsk: %w\nstatement: %s", err, statement.SQL)
	}

	return rows, nil
}

// ExecStatement implements [sqlcore.Execer].
func (s *Store) ExecStatement(ctx context.Context, sql string, args ...any) error {
	if _, err := s.handle(ctx).Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("hmntsk: %w\nstatement: %s", err, sql)
	}

	return nil
}

// QueryStatement implements [sqlcore.Querier].
func (s *Store) QueryStatement(ctx context.Context, sql string, args ...any) (sqlcore.Rows, error) {
	rows, err := s.handle(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("hmntsk: %w\nstatement: %s", err, sql)
	}

	return rows, nil
}

// Migrate applies the engine's schema. It is for tests and development.
func (s *Store) Migrate(ctx context.Context) error { return s.builder.Migrate(ctx, s) }

// VerifySchema compares the live schema against what the engine requires.
func (s *Store) VerifySchema(ctx context.Context) error { return s.builder.VerifySchema(ctx, s) }

// Transactional implements [hmntsk.EventSink].
func (s *Store) Transactional() bool { return true }

// Append implements [hmntsk.EventSink].
func (s *Store) Append(ctx context.Context, events []hmntsk.Event) error {
	if len(events) == 0 {
		return nil
	}

	rows, err := sqlcore.EventRows(events)
	if err != nil {
		return err
	}

	_, err = s.exec(ctx, s.builder.InsertOutbox(rows))

	return err
}

// Events reads the durable event record, oldest first.
func (s *Store) Events(ctx context.Context, limit int) ([]hmntsk.Event, error) {
	rows, err := s.query(ctx, s.builder.SelectOutbox(limit))
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	return sqlcore.ScanOutbox(rows)
}

// Create implements [hmntsk.Repository]. The duplicate check runs before the
// insert, because a unique violation aborts the transaction and the read that
// would explain it could not then run.
func (s *Store) Create(ctx context.Context, task hmntsk.Task) error {
	current, err := s.version(ctx, task.ID)

	switch {
	case err == nil:
		return &hmntsk.ConflictError{TaskID: task.ID, Current: current}
	case !errors.Is(err, hmntsk.ErrNotFound):
		return err
	}

	if _, err := s.exec(ctx, s.builder.InsertTask(task)); err != nil {
		return err
	}

	_, err = s.exec(ctx, s.builder.InsertCandidates(task.ID, task.Candidates))

	return err
}

// version reads a task's current version.
func (s *Store) version(ctx context.Context, id hmntsk.TaskID) (int64, error) {
	statement := s.builder.SelectTaskVersion(id)

	var current any

	err := s.handle(ctx).QueryRow(ctx, statement.SQL, statement.Args...).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, &hmntsk.NotFoundError{TaskID: id}
		}

		return 0, fmt.Errorf("hmntsk: read the current version of task %s: %w", id, err)
	}

	return sqlcore.DecodeInt(current)
}

// Get implements [hmntsk.Repository].
func (s *Store) Get(ctx context.Context, id hmntsk.TaskID) (hmntsk.Task, error) {
	rows, err := s.query(ctx, s.builder.SelectTask(id))
	if err != nil {
		return hmntsk.Task{}, err
	}

	tasks, err := sqlcore.ScanTasks(rows)

	rows.Close()

	switch {
	case err != nil:
		return hmntsk.Task{}, err
	case len(tasks) == 0:
		return hmntsk.Task{}, &hmntsk.NotFoundError{TaskID: id}
	}

	if err := s.attachCandidates(ctx, tasks); err != nil {
		return hmntsk.Task{}, err
	}

	return tasks[0], nil
}

// attachCandidates fills in the candidate pools of tasks read from the tasks
// table, which does not carry them.
func (s *Store) attachCandidates(ctx context.Context, tasks []hmntsk.Task) error {
	if len(tasks) == 0 {
		return nil
	}

	ids := make([]hmntsk.TaskID, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}

	rows, err := s.query(ctx, s.builder.SelectCandidates(ids...))
	if err != nil {
		return err
	}

	pools, err := sqlcore.ScanCandidates(rows)

	rows.Close()

	if err != nil {
		return err
	}

	for i := range tasks {
		tasks[i].Candidates = pools[tasks[i].ID]
	}

	return nil
}

// Update implements [hmntsk.Repository].
func (s *Store) Update(ctx context.Context, task hmntsk.Task, expectedVersion int64) error {
	affected, err := s.exec(ctx, s.builder.UpdateTask(task, expectedVersion))
	if err != nil {
		return err
	}

	if affected == 0 {
		current, versionErr := s.version(ctx, task.ID)
		if versionErr != nil && !errors.Is(versionErr, hmntsk.ErrNotFound) {
			return versionErr
		}

		return sqlcore.CheckAffected(0, task.ID, expectedVersion, current)
	}

	if _, err := s.exec(ctx, s.builder.DeleteCandidates(task.ID)); err != nil {
		return err
	}

	_, err = s.exec(ctx, s.builder.InsertCandidates(task.ID, task.Candidates))

	return err
}

// AppendHistory implements [hmntsk.Repository].
func (s *Store) AppendHistory(ctx context.Context, records ...hmntsk.TransitionRecord) error {
	_, err := s.exec(ctx, s.builder.InsertHistory(records...))

	return err
}

// History implements [hmntsk.Repository].
func (s *Store) History(ctx context.Context, id hmntsk.TaskID) ([]hmntsk.TransitionRecord, error) {
	rows, err := s.query(ctx, s.builder.SelectHistory(id))
	if err != nil {
		return nil, err
	}

	records, err := sqlcore.ScanHistory(rows)

	rows.Close()

	return records, err
}

// Query implements [hmntsk.Repository].
func (s *Store) Query(ctx context.Context, query hmntsk.ResolvedQuery) (hmntsk.Page, error) {
	rows, err := s.query(ctx, s.builder.QueryTasks(query))
	if err != nil {
		return hmntsk.Page{}, err
	}

	tasks, err := sqlcore.ScanTasks(rows)

	rows.Close()

	if err != nil {
		return hmntsk.Page{}, err
	}

	page := hmntsk.Page{}

	if limit := query.EffectiveLimit(); len(tasks) > limit {
		tasks = tasks[:limit]
		page.NextCursor = tasks[len(tasks)-1].ID.String()
	}

	if err := s.attachCandidates(ctx, tasks); err != nil {
		return hmntsk.Page{}, err
	}

	page.Tasks = tasks

	return page, nil
}

// ClaimOverdue implements [hmntsk.Repository].
func (s *Store) ClaimOverdue(ctx context.Context, lease hmntsk.LeaseRequest) ([]hmntsk.Task, error) {
	ids, err := s.overdueIDs(ctx, lease)
	if err != nil {
		return nil, err
	}

	claimed := make([]hmntsk.Task, 0, len(ids))

	for _, id := range ids {
		affected, err := s.exec(ctx, s.builder.ClaimLease(id, lease))
		if err != nil {
			return nil, err
		}

		if affected == 0 {
			continue
		}

		task, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}

		claimed = append(claimed, task)
	}

	return claimed, nil
}

// overdueIDs selects the tasks a sweep may try to claim.
func (s *Store) overdueIDs(ctx context.Context, lease hmntsk.LeaseRequest) ([]hmntsk.TaskID, error) {
	rows, err := s.query(ctx, s.builder.SelectOverdue(lease))
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var ids []hmntsk.TaskID

	for rows.Next() {
		var value any

		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("hmntsk: scan an overdue task identifier: %w", err)
		}

		id, err := sqlcore.DecodeString(value)
		if err != nil {
			return nil, err
		}

		ids = append(ids, hmntsk.TaskID(id))
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hmntsk: read overdue tasks: %w", err)
	}

	return ids, nil
}
