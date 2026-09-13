// Package sqlstore adapts the hmntsk engine to database/sql.
//
// It executes the statements store/sqlcore builds and participates in the
// host's transaction. It contains no SQL and makes no decisions: every choice
// that varies by dialect was made in sqlcore, and every choice that varies by
// behaviour is pinned by the storetest conformance suite.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// Store is a repository, a transactor and an event sink over a *sql.DB.
//
// They are one value on purpose. Three ports that must share a connection, wired
// independently, is a mistake that compiles cleanly and shows up in production
// as a transaction that silently split in two.
type Store struct {
	db      *sql.DB
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

// New returns a store over a database handle and a dialect.
func New(db *sql.DB, dialect sqlcore.Dialect, opts ...Option) *Store {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Store{
		db:      db,
		builder: sqlcore.New(dialect, sqlcore.WithTablePrefix(cfg.prefix)),
	}
}

// Builder returns the statement builder this store executes, so that a host can
// obtain the schema or run verification against the same prefix and dialect.
func (s *Store) Builder() *sqlcore.Builder { return s.builder }

// DB returns the underlying handle.
func (s *Store) DB() *sql.DB { return s.db }

// contextKey is the private type a transaction travels under.
type contextKey struct{}

// ContextWithTx returns a context carrying an open transaction, so that a host
// that began one itself can have the engine join it.
//
// This is the host-led direction of the transaction contract. The engine will
// use this transaction and will neither commit nor roll it back.
func ContextWithTx(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, contextKey{}, tx)
}

// TxFromContext returns the transaction active on a context, if any.
func TxFromContext(ctx context.Context) (*sql.Tx, bool) {
	tx, ok := ctx.Value(contextKey{}).(*sql.Tx)

	return tx, ok
}

// InTransaction implements [hmntsk.Transactor].
func (s *Store) InTransaction(ctx context.Context) bool {
	_, ok := TxFromContext(ctx)

	return ok
}

// Do implements [hmntsk.Transactor].
//
// A call made while a transaction is already active joins it and flattens into
// it: no second transaction and no savepoint, so an inner failure aborts the
// whole scope rather than being quietly contained. A panic rolls back and is
// re-raised unchanged, and a context cancelled while fn ran rolls back rather
// than committing work whose caller has gone.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, joined := TxFromContext(ctx); joined {
		return fn(ctx)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hmntsk: begin transaction: %w", err)
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
		return fmt.Errorf("hmntsk: commit transaction: %w", err)
	}

	committed = true

	return nil
}

// execer is the little of a handle the adapter needs, satisfied by both
// *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// handle returns the transaction active on the context, or the pool.
func (s *Store) handle(ctx context.Context) execer {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}

	return s.db
}

// exec runs a statement, skipping one that the builder declined to produce.
func (s *Store) exec(ctx context.Context, statement sqlcore.Statement) (sql.Result, error) {
	if statement.IsZero() {
		return nil, nil
	}

	result, err := s.handle(ctx).ExecContext(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return nil, fmt.Errorf("hmntsk: %w\nstatement: %s", err, statement.SQL)
	}

	return result, nil
}

// query runs a statement that returns rows.
func (s *Store) query(ctx context.Context, statement sqlcore.Statement) (*sql.Rows, error) {
	rows, err := s.handle(ctx).QueryContext(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return nil, fmt.Errorf("hmntsk: %w\nstatement: %s", err, statement.SQL)
	}

	return rows, nil
}

// ExecStatement implements [sqlcore.Execer], so that the development migration
// runner can drive this store.
func (s *Store) ExecStatement(ctx context.Context, query string, args ...any) error {
	_, err := s.handle(ctx).ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("hmntsk: %w\nstatement: %s", err, query)
	}

	return nil
}

// QueryStatement implements [sqlcore.Querier], so that schema verification can
// drive this store.
func (s *Store) QueryStatement(ctx context.Context, query string, args ...any) (sqlcore.Rows, error) {
	rows, err := s.handle(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("hmntsk: %w\nstatement: %s", err, query)
	}

	return rows, nil
}

// Migrate applies the engine's schema. It is for tests and development; a
// production deployment applies the published DDL through its own pipeline.
func (s *Store) Migrate(ctx context.Context) error { return s.builder.Migrate(ctx, s) }

// VerifySchema compares the live schema against what the engine requires and
// reports every discrepancy.
func (s *Store) VerifySchema(ctx context.Context) error { return s.builder.VerifySchema(ctx, s) }

// Transactional implements [hmntsk.EventSink]: the outbox is a table in the
// same database, written by the same transaction.
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

// Events reads the durable event record, oldest first. It is what a relay polls
// and what the conformance suite reads to prove a rollback left nothing behind.
func (s *Store) Events(ctx context.Context, limit int) ([]hmntsk.Event, error) {
	rows, err := s.query(ctx, s.builder.SelectOutbox(limit))
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	return sqlcore.ScanOutbox(rows)
}

// Create implements [hmntsk.Repository].
//
// The duplicate check runs before the insert rather than after it. A unique
// violation is reported differently by every driver, and on at least one
// dialect it also aborts the transaction, so the failed insert cannot be
// followed by the read that would explain it. Looking first costs one index
// probe and gives the same answer everywhere.
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

	if err := s.handle(ctx).QueryRowContext(ctx, statement.SQL, statement.Args...).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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

	closeErr := rows.Close()

	switch {
	case err != nil:
		return hmntsk.Task{}, err
	case closeErr != nil:
		return hmntsk.Task{}, fmt.Errorf("hmntsk: read task %s: %w", id, closeErr)
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

	closeErr := rows.Close()

	switch {
	case err != nil:
		return err
	case closeErr != nil:
		return fmt.Errorf("hmntsk: read candidate rows: %w", closeErr)
	}

	for i := range tasks {
		tasks[i].Candidates = pools[tasks[i].ID]
	}

	return nil
}

// Update implements [hmntsk.Repository]. The write is conditional on the
// version the caller read; nothing here ever falls back to an unconditional
// one.
func (s *Store) Update(ctx context.Context, task hmntsk.Task, expectedVersion int64) error {
	result, err := s.exec(ctx, s.builder.UpdateTask(task, expectedVersion))
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("hmntsk: read the rows affected by updating task %s: %w", task.ID, err)
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

	closeErr := rows.Close()

	switch {
	case err != nil:
		return nil, err
	case closeErr != nil:
		return nil, fmt.Errorf("hmntsk: read the history of task %s: %w", id, closeErr)
	}

	return records, nil
}

// Count implements [hmntsk.Repository].
func (s *Store) Count(ctx context.Context, query hmntsk.ResolvedQuery) (int64, error) {
	rows, err := s.query(ctx, s.builder.CountTasks(query))
	if err != nil {
		return 0, err
	}

	count, err := sqlcore.ScanCount(rows)

	closeErr := rows.Close()

	switch {
	case err != nil:
		return 0, err
	case closeErr != nil:
		return 0, fmt.Errorf("hmntsk: read the task count: %w", closeErr)
	}

	return count, nil
}

// Query implements [hmntsk.Repository].
func (s *Store) Query(ctx context.Context, query hmntsk.ResolvedQuery) (hmntsk.Page, error) {
	statement, err := s.builder.QueryTasks(query)
	if err != nil {
		return hmntsk.Page{}, err
	}

	rows, err := s.query(ctx, statement)
	if err != nil {
		return hmntsk.Page{}, err
	}

	tasks, err := sqlcore.ScanTasks(rows)

	closeErr := rows.Close()

	switch {
	case err != nil:
		return hmntsk.Page{}, err
	case closeErr != nil:
		return hmntsk.Page{}, fmt.Errorf("hmntsk: read the task page: %w", closeErr)
	}

	page := hmntsk.Page{}

	// The statement asks for one row beyond the page, which is how the caller
	// learns whether another page exists without a second round trip.
	if limit := query.EffectiveLimit(); len(tasks) > limit {
		tasks = tasks[:limit]
		page.NextCursor = s.builder.NextCursor(query.Query, tasks[len(tasks)-1])
	}

	if err := s.attachCandidates(ctx, tasks); err != nil {
		return hmntsk.Page{}, err
	}

	page.Tasks = tasks

	return page, nil
}

// ClaimOverdue implements [hmntsk.Repository].
//
// Each candidate is claimed by a conditional update that repeats the whole
// overdue predicate, so two sweepers racing on one row produce exactly one
// winner without either taking a lock — which is the only way this can work on
// a dialect that has no row-level locking.
func (s *Store) ClaimOverdue(ctx context.Context, lease hmntsk.LeaseRequest) ([]hmntsk.Task, error) {
	ids, err := s.overdueIDs(ctx, lease)
	if err != nil {
		return nil, err
	}

	claimed := make([]hmntsk.Task, 0, len(ids))

	for _, id := range ids {
		result, err := s.exec(ctx, s.builder.ClaimLease(id, lease))
		if err != nil {
			return nil, err
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("hmntsk: read the rows affected by leasing task %s: %w", id, err)
		}

		if affected == 0 {
			// Another sweeper got there first. That is the mechanism working,
			// not a failure.
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

	var ids []hmntsk.TaskID

	for rows.Next() {
		var value any

		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()

			return nil, fmt.Errorf("hmntsk: scan an overdue task identifier: %w", err)
		}

		id, err := sqlcore.DecodeString(value)
		if err != nil {
			_ = rows.Close()

			return nil, err
		}

		ids = append(ids, hmntsk.TaskID(id))
	}

	iterErr := rows.Err()
	closeErr := rows.Close()

	switch {
	case iterErr != nil:
		return nil, fmt.Errorf("hmntsk: read overdue tasks: %w", iterErr)
	case closeErr != nil:
		return nil, fmt.Errorf("hmntsk: read overdue tasks: %w", closeErr)
	}

	return ids, nil
}

// ClaimDueEvents implements [hmntsk.OutboxStore].
//
// Each candidate is claimed by a conditional update that repeats the whole due
// predicate, so two relays racing on one event produce exactly one winner
// without either taking a lock — which is the only way this can work on a
// dialect that has no row-level locking.
func (s *Store) ClaimDueEvents(ctx context.Context, claim hmntsk.OutboxClaim) ([]hmntsk.OutboxEntry, error) {
	ids, err := s.dueEventIDs(ctx, claim)
	if err != nil {
		return nil, err
	}

	claimed := make([]hmntsk.OutboxEntry, 0, len(ids))

	for _, id := range ids {
		result, err := s.exec(ctx, s.builder.ClaimEvent(id, claim))
		if err != nil {
			return nil, err
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("hmntsk: read the rows affected by claiming event %s: %w", id, err)
		}

		if affected == 0 {
			// Another relay got there first. That is the mechanism working,
			// not a failure.
			continue
		}

		entry, err := s.OutboxEntry(ctx, id)
		if err != nil {
			return nil, err
		}

		claimed = append(claimed, entry)
	}

	return claimed, nil
}

// dueEventIDs selects the events a relay pass may try to claim.
func (s *Store) dueEventIDs(ctx context.Context, claim hmntsk.OutboxClaim) ([]string, error) {
	rows, err := s.query(ctx, s.builder.SelectDueEvents(claim))
	if err != nil {
		return nil, err
	}

	var ids []string

	for rows.Next() {
		var value any

		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()

			return nil, fmt.Errorf("hmntsk: scan a due event identifier: %w", err)
		}

		id, err := sqlcore.DecodeString(value)
		if err != nil {
			_ = rows.Close()

			return nil, err
		}

		ids = append(ids, id)
	}

	iterErr := rows.Err()
	closeErr := rows.Close()

	switch {
	case iterErr != nil:
		return nil, fmt.Errorf("hmntsk: read due events: %w", iterErr)
	case closeErr != nil:
		return nil, fmt.Errorf("hmntsk: read due events: %w", closeErr)
	}

	return ids, nil
}

// OutboxEntry implements [hmntsk.OutboxStore].
func (s *Store) OutboxEntry(ctx context.Context, eventID string) (hmntsk.OutboxEntry, error) {
	rows, err := s.query(ctx, s.builder.SelectOutboxEntry(eventID))
	if err != nil {
		return hmntsk.OutboxEntry{}, err
	}

	entries, err := sqlcore.ScanOutboxEntries(rows)

	closeErr := rows.Close()

	switch {
	case err != nil:
		return hmntsk.OutboxEntry{}, err
	case closeErr != nil:
		return hmntsk.OutboxEntry{}, fmt.Errorf("hmntsk: read outbox entry %s: %w", eventID, closeErr)
	case len(entries) == 0:
		return hmntsk.OutboxEntry{}, &hmntsk.OutboxNotFoundError{EventID: eventID}
	}

	return entries[0], nil
}

// RecordAttempt implements [hmntsk.OutboxStore].
func (s *Store) RecordAttempt(ctx context.Context, record hmntsk.AttemptRecord) error {
	return s.settleOutbox(ctx, record.EventID, s.builder.RecordAttempt(record))
}

// MarkAccepted implements [hmntsk.OutboxStore].
func (s *Store) MarkAccepted(ctx context.Context, acceptance hmntsk.Acceptance) error {
	return s.settleOutbox(ctx, acceptance.EventID, s.builder.MarkAccepted(acceptance))
}

// MarkDeadLettered implements [hmntsk.OutboxStore].
func (s *Store) MarkDeadLettered(ctx context.Context, letter hmntsk.DeadLetter) error {
	return s.settleOutbox(ctx, letter.EventID, s.builder.MarkDeadLettered(letter))
}

// settleOutbox runs one settlement write and reports an unknown event.
//
// A rows-affected count of zero is not proof the event is gone: MySQL reports a
// write that changed nothing the same way, and a relay recording the same
// outcome twice after a crash writes exactly that. The row is therefore looked
// up before an absence is reported.
func (s *Store) settleOutbox(ctx context.Context, eventID string, statement sqlcore.Statement) error {
	result, err := s.exec(ctx, statement)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("hmntsk: read the rows affected by settling event %s: %w", eventID, err)
	}

	if affected > 0 {
		return nil
	}

	_, err = s.OutboxEntry(ctx, eventID)

	return err
}
