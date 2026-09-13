package hmntsk

import (
	"context"
	"time"
)

// Clock is the engine's source of time. Substituting it makes deadlines,
// leases and history timestamps deterministic in tests.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// SystemClock is the default [Clock]. Its zero value is ready to use.
type SystemClock struct{}

// Now implements [Clock].
func (SystemClock) Now() time.Time { return time.Now() }

// ClockFunc adapts a function to the [Clock] interface.
type ClockFunc func() time.Time

// Now implements [Clock].
func (f ClockFunc) Now() time.Time { return f() }

// Transactor is the host's transaction boundary, joined by the engine and never
// owned by it.
//
// Three rules bind every implementation, and the storage conformance suite
// pins all three:
//
//   - Whoever begins, commits. Do commits only the transaction it began
//     itself. An operation invoked inside a transaction the host started
//     leaves that transaction's disposition entirely to the host.
//   - Nested scopes join and flatten. Calling Do while a transaction is
//     already active runs fn in that transaction: no second transaction, no
//     savepoint. This is not GORM's default, so the GORM adapter has to
//     override it deliberately.
//   - An error or a panic from fn rolls back a transaction Do began, and a
//     panic is re-raised afterwards rather than converted to an error.
//
// The transaction itself travels in the context. That mechanism is confined to
// the adapter modules and is invisible to the host: a host never has to
// remember to thread a handle through, which is the failure mode this port
// exists to remove.
type Transactor interface {
	// Do runs fn inside a transaction, beginning and committing one only if
	// none is already active on ctx.
	Do(ctx context.Context, fn func(ctx context.Context) error) error
	// InTransaction reports whether ctx already carries an active transaction.
	// The engine uses it to decide whether it may dispatch events itself or
	// must hand the dispatch back to the host, which alone knows when its own
	// transaction commits.
	InTransaction(ctx context.Context) bool
}

// Repository is the engine's storage port. Every method runs inside whatever
// transaction is active on the context it is given.
//
// Implementations live in the store/* modules. They execute SQL built by
// store/sqlcore and make no decisions of their own.
type Repository interface {
	// Create inserts a new task and its candidate rows. It returns an error
	// matching [ErrConflict] if a task with that identifier already exists.
	Create(ctx context.Context, task Task) error
	// Get reads one task. It returns an error matching [ErrNotFound] when
	// there is no such task.
	Get(ctx context.Context, id TaskID) (Task, error)
	// Update writes a task conditionally on expectedVersion, which is the
	// version the caller read. It returns a [*ConflictError] naming the
	// current version when no row matched, and must never fall back to an
	// unconditional write.
	Update(ctx context.Context, task Task, expectedVersion int64) error
	// AppendHistory adds transition records. History is append-only.
	AppendHistory(ctx context.Context, records ...TransitionRecord) error
	// History returns a task's transition records in the order they happened.
	History(ctx context.Context, id TaskID) ([]TransitionRecord, error)
	// Query returns a page of tasks matching a query whose group membership
	// has already been resolved.
	Query(ctx context.Context, query ResolvedQuery) (Page, error)
	// Count returns how many tasks a query matches, counting each task once.
	// It applies every filter [Repository.Query] applies and ignores the
	// ordering, the page size and the cursor.
	Count(ctx context.Context, query ResolvedQuery) (int64, error)
	// ClaimOverdue takes a time-bounded lease on up to Limit overdue tasks and
	// returns them. It is the escalation sweep's exclusive-claim mechanism and
	// must work without row-level locking, because one supported dialect has
	// none.
	ClaimOverdue(ctx context.Context, lease LeaseRequest) ([]Task, error)
}

// LeaseRequest asks for a batch of overdue tasks to be claimed exclusively.
type LeaseRequest struct {
	// Now is the instant the sweep considers current. A task is overdue when
	// its due date is at or before it.
	Now time.Time
	// Owner identifies the sweeper, so that an abandoned lease is traceable.
	Owner string
	// Duration is how long the lease holds. Once it expires the task is
	// available again, which is what stops a crashed sweeper from stranding
	// work forever.
	Duration time.Duration
	// Limit caps how many tasks one sweep claims. Zero means the
	// implementation's default.
	Limit int
	// Types, when non-empty, restricts the sweep to these task types.
	Types []string
}

// EventSink is the durable record of events, written inside the same
// transaction as the state change that produced them.
//
// A sink that cannot participate in that transaction reintroduces the dual-write
// problem the outbox exists to solve, so [New] refuses one at construction
// rather than losing events later.
type EventSink interface {
	// Append records events durably within the active transaction.
	Append(ctx context.Context, events []Event) error
	// Transactional reports whether Append participates in the transaction
	// active on the context it is given. An implementation that writes
	// somewhere else — an HTTP endpoint, a message broker, a slice in memory —
	// must return false.
	Transactional() bool
}

// Store is a repository, a transactor, an event sink and the relay's view of
// the outbox, constructed as one value.
//
// They are one interface on purpose. Ports that must share a connection, built
// independently, is a wiring mistake that compiles cleanly and fails at runtime
// as silently split transactions — and documentation telling users to pass the
// same handle does not fail a build. Adapter constructors therefore return a
// single value satisfying all four.
type Store interface {
	Repository
	Transactor
	EventSink
	OutboxStore
}

// EventHandler consumes events after the transaction that produced them has
// committed. Handlers run in-process; anything that must survive a restart
// belongs to a relay reading the durable record instead.
//
// A handler must not assume the request that caused the change is still alive.
type EventHandler interface {
	// HandleEvent reacts to one event.
	HandleEvent(ctx context.Context, event Event) error
}

// EventHandlerFunc adapts a function to the [EventHandler] interface.
type EventHandlerFunc func(ctx context.Context, event Event) error

// HandleEvent implements [EventHandler].
func (f EventHandlerFunc) HandleEvent(ctx context.Context, event Event) error {
	return f(ctx, event)
}
