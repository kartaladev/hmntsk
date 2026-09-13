// Package gormstore adapts the hmntsk engine to GORM.
//
// Its job is transaction participation, not object mapping. It declares no GORM
// models, calls no AutoMigrate and owns no schema: it runs the statements
// store/sqlcore builds through the host's *gorm.DB so that a task change and a
// business change written through GORM commit together.
//
// Keeping GORM at arm's length is deliberate. Models would leak gorm struct
// tags into the engine's own types, and AutoMigrate would hand GORM the schema
// the host is supposed to own through its own migration pipeline.
package gormstore

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// Store is a repository, a transactor and an event sink over a *gorm.DB.
type Store struct {
	db      *gorm.DB
	builder *sqlcore.Builder
}

// Compile-time proof that the adapter satisfies the whole contract.
var _ hmntsk.Store = (*Store)(nil)

// questionMarks wraps a dialect so that statements come out with GORM's own
// bind marker.
//
// GORM rewrites ? into whatever the dialector needs — $1 on PostgreSQL — while
// building a statement, so a statement that already carries $1 would reach the
// server with nothing bound to it. This is the one place the adapter departs
// from the dialect sqlcore would use on its own, and it changes the marker and
// nothing else.
type questionMarks struct{ sqlcore.Dialect }

// Placeholder implements [sqlcore.Dialect].
func (questionMarks) Placeholder(int) string { return "?" }

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

// New returns a store over a GORM handle and a dialect.
func New(db *gorm.DB, dialect sqlcore.Dialect, opts ...Option) *Store {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Store{
		db: db,
		builder: sqlcore.New(
			questionMarks{Dialect: dialect},
			sqlcore.WithTablePrefix(cfg.prefix),
		),
	}
}

// Builder returns the statement builder this store executes.
func (s *Store) Builder() *sqlcore.Builder { return s.builder }

// DB returns the underlying handle.
func (s *Store) DB() *gorm.DB { return s.db }

// contextKey is the private type a transaction travels under.
type contextKey struct{}

// ContextWithTx returns a context carrying an open GORM transaction, so that a
// host that began one itself can have the engine join it.
func ContextWithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, contextKey{}, tx)
}

// TxFromContext returns the transaction active on a context, if any.
func TxFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(contextKey{}).(*gorm.DB)

	return tx, ok
}

// InTransaction implements [hmntsk.Transactor].
func (s *Store) InTransaction(ctx context.Context) bool {
	_, ok := TxFromContext(ctx)

	return ok
}

// Do implements [hmntsk.Transactor].
//
// This is the adapter's one real decision, and it is a decision to refuse
// GORM's. db.Transaction nests with a SAVEPOINT by default, so an inner scope
// that fails rolls back to the savepoint and leaves the outer transaction alive
// — a host that catches the inner error and carries on then commits half a
// change. Every other driver aborts the whole scope, and the engine's contract
// says the whole scope aborts, so this begins a transaction itself and flattens
// every nested call into it. db.Transaction is never called.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, joined := TxFromContext(ctx); joined {
		return fn(ctx)
	}

	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("hmntsk: begin transaction: %w", tx.Error)
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
		return fmt.Errorf("hmntsk: commit transaction: %w", err)
	}

	committed = true

	return nil
}

// handle returns the transaction active on the context, or the pool.
func (s *Store) handle(ctx context.Context) *gorm.DB {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}

	return s.db.WithContext(ctx)
}

// exec runs a statement, skipping one the builder declined to produce, and
// returns how many rows it matched.
func (s *Store) exec(ctx context.Context, statement sqlcore.Statement) (int64, error) {
	if statement.IsZero() {
		return 0, nil
	}

	result := s.handle(ctx).Exec(statement.SQL, statement.Args...)
	if result.Error != nil {
		return 0, fmt.Errorf("hmntsk: %w\nstatement: %s", result.Error, statement.SQL)
	}

	return result.RowsAffected, nil
}

// query runs a statement that returns rows.
func (s *Store) query(ctx context.Context, statement sqlcore.Statement) (sqlcore.Rows, error) {
	rows, err := s.handle(ctx).Raw(statement.SQL, statement.Args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("hmntsk: %w\nstatement: %s", err, statement.SQL)
	}

	return rows, nil
}

// ExecStatement implements [sqlcore.Execer].
func (s *Store) ExecStatement(ctx context.Context, sql string, args ...any) error {
	if err := s.handle(ctx).Exec(sql, args...).Error; err != nil {
		return fmt.Errorf("hmntsk: %w\nstatement: %s", err, sql)
	}

	return nil
}

// QueryStatement implements [sqlcore.Querier].
func (s *Store) QueryStatement(ctx context.Context, sql string, args ...any) (sqlcore.Rows, error) {
	rows, err := s.handle(ctx).Raw(sql, args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("hmntsk: %w\nstatement: %s", err, sql)
	}

	return rows, nil
}

// Migrate applies the engine's schema. It is for tests and development, and it
// is not AutoMigrate: the statements are the ones sqlcore publishes, which are
// the ones the host's own pipeline would apply.
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

	defer closeRows(rows)

	return sqlcore.ScanOutbox(rows)
}

// Create implements [hmntsk.Repository].
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

	rows, err := s.query(ctx, statement)
	if err != nil {
		return 0, err
	}

	defer closeRows(rows)

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("hmntsk: read the current version of task %s: %w", id, err)
		}

		return 0, &hmntsk.NotFoundError{TaskID: id}
	}

	var value any

	if err := rows.Scan(&value); err != nil {
		return 0, fmt.Errorf("hmntsk: read the current version of task %s: %w", id, err)
	}

	return sqlcore.DecodeInt(value)
}

// Get implements [hmntsk.Repository].
func (s *Store) Get(ctx context.Context, id hmntsk.TaskID) (hmntsk.Task, error) {
	rows, err := s.query(ctx, s.builder.SelectTask(id))
	if err != nil {
		return hmntsk.Task{}, err
	}

	tasks, err := sqlcore.ScanTasks(rows)

	closeRows(rows)

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

	closeRows(rows)

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

	closeRows(rows)

	return records, err
}

// Query implements [hmntsk.Repository].
func (s *Store) Query(ctx context.Context, query hmntsk.ResolvedQuery) (hmntsk.Page, error) {
	rows, err := s.query(ctx, s.builder.QueryTasks(query))
	if err != nil {
		return hmntsk.Page{}, err
	}

	tasks, err := sqlcore.ScanTasks(rows)

	closeRows(rows)

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

	defer closeRows(rows)

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

// closeRows releases a result set. sqlcore's Rows is the smallest interface the
// three drivers agree on and does not include Close, so the concrete type is
// asked for it.
func closeRows(rows sqlcore.Rows) {
	if closer, ok := rows.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

// ClaimDueEvents implements [hmntsk.OutboxStore].
//
// Each candidate is claimed by a conditional update that repeats the whole due
// predicate, so two relays racing on one event produce exactly one winner
// without either taking a lock.
func (s *Store) ClaimDueEvents(ctx context.Context, claim hmntsk.OutboxClaim) ([]hmntsk.OutboxEntry, error) {
	ids, err := s.dueEventIDs(ctx, claim)
	if err != nil {
		return nil, err
	}

	claimed := make([]hmntsk.OutboxEntry, 0, len(ids))

	for _, id := range ids {
		affected, err := s.exec(ctx, s.builder.ClaimEvent(id, claim))
		if err != nil {
			return nil, err
		}

		if affected == 0 {
			// Another relay got there first, which is the mechanism working.
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

	defer closeRows(rows)

	var ids []string

	for rows.Next() {
		var value any

		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("hmntsk: scan a due event identifier: %w", err)
		}

		id, err := sqlcore.DecodeString(value)
		if err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hmntsk: read due events: %w", err)
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

	closeRows(rows)

	switch {
	case err != nil:
		return hmntsk.OutboxEntry{}, err
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
// A rows-affected count of zero is not proof the event is gone — a write that
// changed nothing reports the same, which is exactly what a relay recording the
// same outcome twice after a crash writes — so the row is looked up before an
// absence is reported.
func (s *Store) settleOutbox(ctx context.Context, eventID string, statement sqlcore.Statement) error {
	affected, err := s.exec(ctx, statement)
	if err != nil {
		return err
	}

	if affected > 0 {
		return nil
	}

	_, err = s.OutboxEntry(ctx, eventID)

	return err
}
