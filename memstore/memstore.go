// Package memstore is an in-memory [hmntsk.Store].
//
// It exists so that the engine's own behaviour can be tested without a
// database, and so that the shared storage conformance suite has a reference
// implementation to check itself against before it is pointed at a real one. It
// is not a production store: it keeps everything in one process's heap and
// forgets it on exit.
//
// It is nonetheless a faithful one. Transactions are serialised and staged, so
// a rollback really does discard everything the scope wrote; nested scopes join
// rather than nest; the event sink writes inside the transaction; and mutations
// are conditional on the version the caller read.
package memstore

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kartaladev/hmntsk"
)

// Store is an in-memory repository, transactor and event sink.
//
// It is safe for concurrent use. Transactions are serialised against one
// another: a second transaction waits for the first to finish rather than
// interleaving with it, which is stricter than any real database and makes the
// conformance suite's concurrency cases deterministic without making them
// weaker — a losing writer still loses on the version check.
type Store struct {
	// txMu serialises transactions against one another.
	txMu sync.Mutex
	// dataMu guards the committed state. It is separate from txMu so that a
	// read outside a transaction does not block behind an open one — a real
	// database would serve that read from another connection, and the
	// conformance suite relies on being able to make it.
	dataMu  sync.RWMutex
	tasks   map[hmntsk.TaskID]hmntsk.Task
	history map[hmntsk.TaskID][]hmntsk.TransitionRecord
	outbox  []outboxRow
}

// Compile-time proof that the in-memory store satisfies the whole Store
// contract — repository, transactor and event sink — as one value, which is
// what design decision D5 requires of every adapter.
var _ hmntsk.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{
		tasks:   make(map[hmntsk.TaskID]hmntsk.Task),
		history: make(map[hmntsk.TaskID][]hmntsk.TransitionRecord),
	}
}

// contextKey is the private type under which a transaction travels in a
// context. It is private so that nothing outside this package can forge one.
type contextKey struct{}

// tx is one transaction's staged writes. Nothing it holds is visible to another
// transaction until it commits.
type tx struct {
	store   *Store
	tasks   map[hmntsk.TaskID]hmntsk.Task
	history map[hmntsk.TaskID][]hmntsk.TransitionRecord
	// outbox holds rows appended by this scope, which no other scope can see
	// until it commits.
	outbox []outboxRow
	// outboxEdits holds changes to rows that were already committed, keyed by
	// event identifier. A lease and a settlement are both edits, and staging
	// them separately from the appends keeps a rollback honest about each.
	outboxEdits map[string]outboxRow
}

// ContextWithTx returns a context carrying tx, so that a host can open a
// transaction itself and have the engine join it.
func (s *Store) ContextWithTx(ctx context.Context) (scoped context.Context, done func(commit bool)) {
	s.txMu.Lock()

	transaction := s.newTx()

	done = func(commit bool) {
		defer s.txMu.Unlock()

		if commit {
			transaction.commit()
		}
	}

	return context.WithValue(ctx, contextKey{}, transaction), done
}

// newTx stages a transaction. The caller must already hold s.txMu.
func (s *Store) newTx() *tx {
	return &tx{
		store:       s,
		tasks:       make(map[hmntsk.TaskID]hmntsk.Task),
		history:     make(map[hmntsk.TaskID][]hmntsk.TransitionRecord),
		outboxEdits: make(map[string]outboxRow),
	}
}

// commit merges the staged writes into the store. The caller must hold s.txMu.
func (t *tx) commit() {
	t.store.dataMu.Lock()
	defer t.store.dataMu.Unlock()

	maps.Copy(t.store.tasks, t.tasks)

	for id, records := range t.history {
		t.store.history[id] = append(t.store.history[id], records...)
	}

	t.store.outbox = append(t.store.outbox, t.outbox...)

	for id, edited := range t.outboxEdits {
		for i, row := range t.store.outbox {
			if row.event.ID == id {
				t.store.outbox[i] = edited

				break
			}
		}
	}
}

// txFrom returns the transaction active on ctx, if any.
func txFrom(ctx context.Context) *tx {
	transaction, _ := ctx.Value(contextKey{}).(*tx)

	return transaction
}

// InTransaction implements [hmntsk.Transactor].
func (s *Store) InTransaction(ctx context.Context) bool { return txFrom(ctx) != nil }

// Do implements [hmntsk.Transactor].
//
// A nested call joins the active transaction and flattens into it: no second
// transaction, no savepoint, so an inner failure aborts the whole scope. A
// panic rolls back and is re-raised rather than converted into an error, and a
// context cancelled while fn ran rolls back too.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if txFrom(ctx) != nil {
		return fn(ctx)
	}

	s.txMu.Lock()
	defer s.txMu.Unlock()

	transaction := s.newTx()
	scoped := context.WithValue(ctx, contextKey{}, transaction)

	// Nothing a transaction writes touches the store until commit, so a panic
	// unwinding through here discards the scope simply by never reaching the
	// commit below, and is re-raised untouched rather than becoming an error.
	if err := fn(scoped); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	transaction.commit()

	return nil
}

// Transactional implements [hmntsk.EventSink].
func (s *Store) Transactional() bool { return true }

// Append implements [hmntsk.EventSink].
func (s *Store) Append(ctx context.Context, events []hmntsk.Event) error {
	transaction := txFrom(ctx)
	if transaction == nil {
		return &hmntsk.ConfigurationError{
			Detail: "events may only be appended inside a transaction",
		}
	}

	for _, event := range events {
		// A recorded event is due as soon as it is recorded: the outbox exists
		// so that delivery can happen after the commit, not later than it.
		due := hmntsk.NormalizeTime(event.OccurredAt)
		transaction.outbox = append(transaction.outbox, outboxRow{
			event:         event,
			nextAttemptAt: &due,
		})
	}

	return nil
}

// Events returns every event committed so far. It is the durable record a relay
// would read.
func (s *Store) Events() []hmntsk.Event {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()

	events := make([]hmntsk.Event, 0, len(s.outbox))
	for _, row := range s.outbox {
		events = append(events, row.event)
	}

	return events
}

// Create implements [hmntsk.Repository].
func (s *Store) Create(ctx context.Context, task hmntsk.Task) error {
	transaction := txFrom(ctx)
	if transaction == nil {
		return errOutsideTransaction("Create")
	}

	if _, err := transaction.get(task.ID); err == nil {
		return &hmntsk.ConflictError{TaskID: task.ID}
	}

	transaction.tasks[task.ID] = task.Normalize()

	return nil
}

// Get implements [hmntsk.Repository]. It reads through an active transaction
// when there is one, so a scope sees its own uncommitted writes.
func (s *Store) Get(ctx context.Context, id hmntsk.TaskID) (hmntsk.Task, error) {
	if transaction := txFrom(ctx); transaction != nil {
		return transaction.get(id)
	}

	s.dataMu.RLock()
	defer s.dataMu.RUnlock()

	task, ok := s.tasks[id]
	if !ok {
		return hmntsk.Task{}, &hmntsk.NotFoundError{TaskID: id}
	}

	return task.Clone(), nil
}

// get reads a task through the transaction's staged writes.
func (t *tx) get(id hmntsk.TaskID) (hmntsk.Task, error) {
	if task, ok := t.tasks[id]; ok {
		return task.Clone(), nil
	}

	t.store.dataMu.RLock()
	defer t.store.dataMu.RUnlock()

	if task, ok := t.store.tasks[id]; ok {
		return task.Clone(), nil
	}

	return hmntsk.Task{}, &hmntsk.NotFoundError{TaskID: id}
}

// Update implements [hmntsk.Repository]. The write is conditional on the
// version the caller read; a stale write changes nothing and reports the
// current version.
func (s *Store) Update(ctx context.Context, task hmntsk.Task, expectedVersion int64) error {
	transaction := txFrom(ctx)
	if transaction == nil {
		return errOutsideTransaction("Update")
	}

	current, err := transaction.get(task.ID)
	if err != nil {
		return err
	}

	if current.Version != expectedVersion {
		return &hmntsk.ConflictError{
			TaskID: task.ID, Expected: expectedVersion, Current: current.Version,
		}
	}

	transaction.tasks[task.ID] = task.Normalize()

	return nil
}

// AppendHistory implements [hmntsk.Repository].
func (s *Store) AppendHistory(ctx context.Context, records ...hmntsk.TransitionRecord) error {
	if len(records) == 0 {
		return nil
	}

	transaction := txFrom(ctx)
	if transaction == nil {
		return errOutsideTransaction("AppendHistory")
	}

	for _, record := range records {
		record.At = hmntsk.NormalizeTime(record.At)
		transaction.history[record.TaskID] = append(transaction.history[record.TaskID], record)
	}

	return nil
}

// History implements [hmntsk.Repository].
func (s *Store) History(ctx context.Context, id hmntsk.TaskID) ([]hmntsk.TransitionRecord, error) {
	if transaction := txFrom(ctx); transaction != nil {
		transaction.store.dataMu.RLock()
		committed := slices.Clone(transaction.store.history[id])
		transaction.store.dataMu.RUnlock()

		return append(committed, transaction.history[id]...), nil
	}

	s.dataMu.RLock()
	defer s.dataMu.RUnlock()

	return slices.Clone(s.history[id]), nil
}

// snapshot returns every stored task, reading through an active transaction.
func (s *Store) snapshot(ctx context.Context) []hmntsk.Task {
	if transaction := txFrom(ctx); transaction != nil {
		transaction.store.dataMu.RLock()
		merged := make(map[hmntsk.TaskID]hmntsk.Task, len(transaction.store.tasks)+len(transaction.tasks))
		maps.Copy(merged, transaction.store.tasks)
		transaction.store.dataMu.RUnlock()

		maps.Copy(merged, transaction.tasks)

		return slices.Collect(maps.Values(merged))
	}

	s.dataMu.RLock()
	defer s.dataMu.RUnlock()

	return slices.Collect(maps.Values(s.tasks))
}

// Query implements [hmntsk.Repository].
func (s *Store) Query(ctx context.Context, query hmntsk.ResolvedQuery) (hmntsk.Page, error) {
	candidates := s.snapshot(ctx)

	matched := make([]hmntsk.Task, 0, len(candidates))

	for _, task := range candidates {
		if matches(task, query) {
			matched = append(matched, task.Clone())
		}
	}

	sort.Slice(matched, func(i, j int) bool {
		if query.Descending {
			return matched[i].ID > matched[j].ID
		}

		return matched[i].ID < matched[j].ID
	})

	matched = afterCursor(matched, query)

	limit := query.EffectiveLimit()

	page := hmntsk.Page{}
	if len(matched) > limit {
		matched = matched[:limit]
		page.NextCursor = matched[len(matched)-1].ID.String()
	}

	page.Tasks = matched

	return page, nil
}

// afterCursor drops everything up to and including the cursor position.
func afterCursor(tasks []hmntsk.Task, query hmntsk.ResolvedQuery) []hmntsk.Task {
	if query.Cursor == "" {
		return tasks
	}

	for i, task := range tasks {
		beyond := task.ID.String() > query.Cursor
		if query.Descending {
			beyond = task.ID.String() < query.Cursor
		}

		if beyond {
			return tasks[i:]
		}
	}

	return nil
}

// matches reports whether a task satisfies every filter in the query.
func matches(task hmntsk.Task, query hmntsk.ResolvedQuery) bool {
	if query.Assignee != "" && task.Assignee != query.Assignee {
		return false
	}

	if len(query.Statuses) > 0 && !slices.Contains(query.Statuses, task.Status) {
		return false
	}

	if len(query.Types) > 0 && !slices.Contains(query.Types, task.Type) {
		return false
	}

	if query.OwnerType != "" && task.Correlation.OwnerType != query.OwnerType {
		return false
	}

	if query.OwnerRef != "" && task.Correlation.OwnerRef != query.OwnerRef {
		return false
	}

	if query.ActivityKey != "" && task.Correlation.ActivityKey != query.ActivityKey {
		return false
	}

	if query.DueBefore != nil && (task.DueAt == nil || !task.DueAt.Before(*query.DueBefore)) {
		return false
	}

	return matchesCandidate(task, query)
}

// matchesCandidate applies the eligibility filter, using the group membership
// the service already resolved.
func matchesCandidate(task hmntsk.Task, query hmntsk.ResolvedQuery) bool {
	if query.Candidate == "" {
		return true
	}

	if slices.Contains(task.Candidates.Excluded, query.Candidate) {
		return false
	}

	if task.Assignee != "" {
		// Work somebody already holds is not work anybody else may act on,
		// whatever the pool says.
		return task.Assignee == query.Candidate
	}

	if slices.Contains(task.Candidates.Users, query.Candidate) {
		return true
	}

	for _, group := range query.CandidateGroups {
		if slices.Contains(task.Candidates.Groups, group) {
			return true
		}
	}

	return false
}

// ClaimOverdue implements [hmntsk.Repository]. The lease is taken by a
// conditional update on the lease columns, never by a row lock, so the same
// mechanism works on a dialect that has no locking at all.
func (s *Store) ClaimOverdue(ctx context.Context, lease hmntsk.LeaseRequest) ([]hmntsk.Task, error) {
	transaction := txFrom(ctx)
	if transaction == nil {
		return nil, errOutsideTransaction("ClaimOverdue")
	}

	now := hmntsk.NormalizeTime(lease.Now)

	limit := lease.Limit
	if limit <= 0 {
		limit = 100
	}

	candidates := s.snapshot(ctx)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	claimed := make([]hmntsk.Task, 0, limit)

	for _, task := range candidates {
		if len(claimed) == limit {
			break
		}

		if !task.IsOverdue(now) || task.IsLeased(now) {
			continue
		}

		if len(lease.Types) > 0 && !slices.Contains(lease.Types, task.Type) {
			continue
		}

		leased := task.Clone()
		leased.LockedBy = lease.Owner
		until := now.Add(lease.Duration)
		leased.LockedUntil = &until

		transaction.tasks[leased.ID] = leased.Normalize()
		claimed = append(claimed, leased.Normalize())
	}

	return claimed, nil
}

// errOutsideTransaction reports a repository call made with no transaction
// active, which would write outside the host's atomicity guarantee.
func errOutsideTransaction(operation string) error {
	return &hmntsk.ConfigurationError{
		Detail: strings.ToLower(operation) + " must run inside a transaction",
	}
}

// Len returns how many tasks are stored. It is for tests.
func (s *Store) Len() int {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()

	return len(s.tasks)
}

// Clock returns a clock fixed at the given instant, advancing by step on every
// read. It is a convenience for tests that need deterministic timestamps.
func Clock(start time.Time, step time.Duration) hmntsk.Clock {
	var (
		mu      sync.Mutex
		current = start
	)

	return hmntsk.ClockFunc(func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		now := current
		current = current.Add(step)

		return now
	})
}
