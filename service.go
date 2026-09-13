package hmntsk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// Service is the engine. It owns the task lifecycle and nothing above it: the
// host supplies the store, the directory and the clock, and consumes what the
// engine publishes.
//
// A Service is safe for concurrent use.
type Service struct {
	store           Store
	registry        *Registry
	resolver        GroupResolver
	strategy        AssignmentStrategy
	clock           Clock
	ids             IDGenerator
	eventIDs        *UUIDv7Generator
	handlers        []EventHandler
	onDispatchError func(ctx context.Context, err error)
}

// Option configures a [Service] at construction.
type Option func(*Service)

// WithRegistry supplies the task type registry. The default is a fresh, empty
// registry owned by the service.
func WithRegistry(registry *Registry) Option {
	return func(s *Service) {
		if registry != nil {
			s.registry = registry
		}
	}
}

// WithGroupResolver supplies the directory used to resolve group membership.
func WithGroupResolver(resolver GroupResolver) Option {
	return func(s *Service) { s.resolver = resolver }
}

// WithAssignmentStrategy supplies the rule for placing a newly created task.
// The default reserves a task for its single eligible actor and pools the rest.
func WithAssignmentStrategy(strategy AssignmentStrategy) Option {
	return func(s *Service) {
		if strategy != nil {
			s.strategy = strategy
		}
	}
}

// WithClock supplies the engine's source of time.
func WithClock(clock Clock) Option {
	return func(s *Service) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// WithIDGenerator supplies the task identifier scheme.
func WithIDGenerator(generator IDGenerator) Option {
	return func(s *Service) {
		if generator != nil {
			s.ids = generator
		}
	}
}

// WithEventHandlers adds in-process consumers, run after the transaction that
// produced the events commits.
func WithEventHandlers(handlers ...EventHandler) Option {
	return func(s *Service) {
		for _, handler := range handlers {
			if handler != nil {
				s.handlers = append(s.handlers, handler)
			}
		}
	}
}

// WithDispatchErrorHandler supplies a hook for errors raised by in-process
// handlers after a commit.
//
// Such an error cannot fail the operation: the state change is already durable,
// and so are its events, so a relay will deliver them regardless. The default
// hook does nothing, which is safe but silent; a host that runs in-process
// handlers should supply one that logs.
func WithDispatchErrorHandler(handler func(ctx context.Context, err error)) Option {
	return func(s *Service) {
		if handler != nil {
			s.onDispatchError = handler
		}
	}
}

// New builds a service over a store.
//
// It refuses, rather than tolerates, a store whose event sink cannot join the
// host's transaction. Such a configuration works in tests and loses events in
// production, exactly when they matter, so it fails here instead.
func New(store Store, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, &ConfigurationError{Detail: "a store is required"}
	}

	svc := &Service{
		store:           store,
		registry:        NewRegistry(),
		strategy:        SingleCandidateStrategy{},
		clock:           SystemClock{},
		ids:             NewUUIDv7Generator(),
		eventIDs:        NewUUIDv7Generator(),
		onDispatchError: func(context.Context, error) {},
	}

	for _, opt := range opts {
		opt(svc)
	}

	if !store.Transactional() {
		return nil, &ConfigurationError{
			Detail: "the durable event sink must participate in the host's transaction; " +
				"an in-memory or remote sink loses every event produced by a transaction that " +
				"commits and then crashes before delivery",
		}
	}

	return svc, nil
}

// Registry returns the task type registry this service validates against.
func (s *Service) Registry() *Registry { return s.registry }

// Register adds a task type. It is shorthand for Registry().Register.
func (s *Service) Register(spec TypeSpec) error { return s.registry.Register(spec) }

// Result is what a lifecycle operation produced.
type Result struct {
	// Task is the task as it now stands.
	Task Task
	// Events are the events the operation produced, already recorded durably.
	Events []Event

	pending []Event
	service *Service
}

// Pending reports whether the events still have to be dispatched by the host.
// It is true exactly when the operation ran inside a transaction the host began,
// because only the host knows when that transaction commits.
func (r Result) Pending() bool { return len(r.pending) > 0 }

// Dispatch delivers the result's events to in-process consumers. It is a no-op
// unless [Result.Pending] is true, so a host may call it unconditionally.
//
// Call it after committing, never before: a consumer that reads back a task it
// was told about must find the committed state.
func (r Result) Dispatch(ctx context.Context) error {
	if r.service == nil || len(r.pending) == 0 {
		return nil
	}

	return r.service.deliver(ctx, r.pending)
}

// TaskRequest identifies a task and who is acting on it. Every lifecycle
// operation takes one.
type TaskRequest struct {
	// TaskID names the task. It is required.
	TaskID TaskID
	// Actor is who is performing the operation, as the host's middleware
	// established it. The engine authenticates nobody.
	Actor string
	// Version is the version the caller last observed. When set, the mutation
	// is refused with a [*ConflictError] if the task has moved on. When nil,
	// the version read inside the transaction is used, which still detects a
	// concurrent writer.
	Version *int64
	// Comment is free text recorded in history: a reason for a failure or
	// cancellation, a note on a delegation.
	Comment string
}

// CompleteRequest completes a task with an output payload.
type CompleteRequest struct {
	TaskRequest
	// Output is validated in full against the type's output schema, required
	// fields included, and then stored exactly as supplied.
	Output json.RawMessage
}

// DelegateRequest reassigns a task to another actor.
type DelegateRequest struct {
	TaskRequest
	// Target is the actor to delegate to. They must be eligible for the task.
	Target string
}

// SaveProgressRequest records partial work.
type SaveProgressRequest struct {
	TaskRequest
	// Patch is an RFC 6902 JSON Patch applied to the task's saved progress.
	// A patch handles nested structures and arrays that a shallow merge cannot,
	// and it is itself the field-level audit record.
	Patch json.RawMessage
}

// Create registers a new task: it validates the payload against the type's
// input schema, applies the type's defaults, resolves the candidate pool and
// places the task in RESERVED, READY or ERROR.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Result, error) {
	spec, err := s.registry.Lookup(req.Type)
	if err != nil {
		return Result{}, err
	}

	if err := s.registry.ValidateInput(req.Type, defaultedPayload(req.Input)); err != nil {
		return Result{}, err
	}

	id := req.ID
	if id.IsZero() {
		id, err = s.ids.NewTaskID()
		if err != nil {
			return Result{}, err
		}
	}

	hostLed := s.store.InTransaction(ctx)

	var (
		task   Task
		events []Event
	)

	err = s.store.Do(ctx, func(ctx context.Context) error {
		now := s.clock.Now()

		created := spec.NewTask(id, req, now)

		placed, produced, err := Assign(ctx, s.resolver, s.strategy, created, now)
		if err != nil {
			return err
		}

		if err := s.store.Create(ctx, placed); err != nil {
			return err
		}

		if err := s.persistOutcome(ctx, produced, recordsOf(produced)); err != nil {
			return err
		}

		task, events = placed, produced

		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return s.finish(ctx, task, events, hostLed)
}

// Get reads one task. It participates in whatever transaction is active.
func (s *Service) Get(ctx context.Context, id TaskID) (Task, error) {
	return s.store.Get(ctx, id)
}

// Eligible reports whether actor may act on task as a candidate, applying
// exclusion, candidate users and group membership exactly as a claim does. It
// is [IsEligible] over the directory this service was built with, which stays
// private to the engine.
//
// Group membership is resolved only when the pool names groups and the actor
// is neither excluded nor a candidate user. A directory that fails, or none
// configured for a pool that needs one, is an error matching
// [ErrGroupResolution] and never a quiet refusal.
func (s *Service) Eligible(ctx context.Context, task Task, actor string) (bool, error) {
	return IsEligible(ctx, s.resolver, task.Candidates, actor)
}

// History returns a task's transition records, oldest first.
func (s *Service) History(ctx context.Context, id TaskID) ([]TransitionRecord, error) {
	return s.store.History(ctx, id)
}

// Claim reserves a pooled task for an eligible actor.
func (s *Service) Claim(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(ctx context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Claim(req.Actor, now)
		if err != nil {
			return task, nil, nil, err
		}

		if err := Authorize(ctx, s.resolver, task, OpClaim, req.Actor); err != nil {
			return task, nil, nil, err
		}

		return next, events, recordsOf(events), nil
	})
}

// Release returns a reserved task to its pool.
func (s *Service) Release(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Release(req.Actor, req.Comment, now)

		return next, events, recordsOf(events), err
	})
}

// Start moves a reserved task to IN_PROGRESS.
func (s *Service) Start(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Start(req.Actor, now)

		return next, events, recordsOf(events), err
	})
}

// Complete closes a task with the actor's output.
func (s *Service) Complete(ctx context.Context, req CompleteRequest) (Result, error) {
	return s.mutate(ctx, req.TaskRequest, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Complete(req.Actor, req.Output, req.Comment, now)
		if err != nil {
			return task, nil, nil, err
		}

		if err := s.registry.ValidateOutput(task.Type, defaultedPayload(req.Output)); err != nil {
			return task, nil, nil, err
		}

		return next, events, recordsOf(events), nil
	})
}

// Fail records that the actor could not perform the work.
func (s *Service) Fail(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Fail(req.Actor, req.Comment, now)

		return next, events, recordsOf(events), err
	})
}

// Delegate reassigns a task to another eligible actor without losing work.
func (s *Service) Delegate(ctx context.Context, req DelegateRequest) (Result, error) {
	return s.mutate(ctx, req.TaskRequest, func(ctx context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Delegate(req.Actor, req.Target, req.Comment, now)
		if err != nil {
			return task, nil, nil, err
		}

		if err := AuthorizeDelegate(ctx, s.resolver, task, req.Actor, req.Target); err != nil {
			return task, nil, nil, err
		}

		return next, events, recordsOf(events), nil
	})
}

// Suspend withdraws a task from circulation.
func (s *Service) Suspend(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Suspend(req.Actor, req.Comment, now)

		return next, events, recordsOf(events), err
	})
}

// Resume returns a suspended task to the state it came from.
func (s *Service) Resume(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Resume(req.Actor, req.Comment, now)

		return next, events, recordsOf(events), err
	})
}

// Escalate applies the task's escalation policy. It takes the same path whether
// an operator invoked it or the sweep did.
func (s *Service) Escalate(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Escalate(req.Actor, s.escalationPolicy(task), req.Comment, now)

		return next, events, recordsOf(events), err
	})
}

// Cancel closes a task at its owner's request.
func (s *Service) Cancel(ctx context.Context, req TaskRequest) (Result, error) {
	return s.mutate(ctx, req, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		next, events, err := task.Cancel(req.Actor, req.Comment, now)

		return next, events, recordsOf(events), err
	})
}

// SaveProgress applies an RFC 6902 patch to a task's saved progress.
//
// The merged draft is checked for shape only: every field present must agree
// with the input schema, but nothing has to be there, because a draft is
// incomplete by definition. Full validation happens at completion.
//
// The first save on a reserved task starts it, and that implicit start is
// recorded in history. No save produces an event: no consumer is blocked on a
// draft, and emitting one per keystroke-driven autosave would drown the stream
// the events exist to carry.
func (s *Service) SaveProgress(ctx context.Context, req SaveProgressRequest) (Result, error) {
	return s.mutate(ctx, req.TaskRequest, func(_ context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error) {
		// Only the first save moves the task. A later one changes the draft
		// without leaving IN_PROGRESS, which is not a transition at all and so
		// is not the transition table's business.
		to := task.Status

		switch task.Status {
		case StatusReserved:
			to = StatusInProgress

			if err := task.requireFrom(OpSaveProgress, to, StatusReserved); err != nil {
				return task, nil, nil, err
			}
		case StatusInProgress:
		default:
			return task, nil, nil, &TransitionError{
				TaskID: task.ID, Operation: OpSaveProgress.String(),
				From: task.Status, To: StatusInProgress,
			}
		}

		if err := task.requireAssignee(OpSaveProgress, req.Actor); err != nil {
			return task, nil, nil, err
		}

		merged, err := applyJSONPatch(task.Progress, req.Patch)
		if err != nil {
			return task, nil, nil, err
		}

		if err := s.registry.ValidateShape(task.Type, merged); err != nil {
			return task, nil, nil, err
		}

		started := task.Status == StatusReserved

		next, produced := task.record(OpSaveProgress, to, req.Actor, req.Comment, now, func(next *Task) {
			next.Progress = merged

			if started && next.StartedAt == nil {
				next.StartedAt = timePtr(now)
			}
		})

		// A progress save never produces an event, and only the implicit start
		// is coarse enough to belong in the transition log.
		if !started {
			return next, nil, nil, nil
		}

		return next, nil, recordsOf(produced), nil
	})
}

// Query returns a page of tasks. When the query names a candidate, their group
// membership is resolved here, so that no store adapter needs a directory of
// its own.
//
// An ordering outside the supported set is a validation error.
func (s *Service) Query(ctx context.Context, query Query) (Page, error) {
	resolved, err := s.resolve(ctx, query, nil)
	if err != nil {
		return Page{}, err
	}

	return s.store.Query(ctx, resolved)
}

// MaxCountBuckets is the most buckets one [Service.CountBuckets] call counts. It
// stops an unbounded row of badges from issuing an unbounded number of queries.
// A host needing more calls [Service.Count] for each bucket itself.
const MaxCountBuckets = 32

// Count returns how many tasks a query matches. It applies every filter
// [Service.Query] applies, eligibility included, and ignores the ordering, the
// page size and the cursor. Each task is counted once.
func (s *Service) Count(ctx context.Context, query Query) (int64, error) {
	resolved, err := s.resolve(ctx, query, nil)
	if err != nil {
		return 0, err
	}

	return s.store.Count(ctx, resolved)
}

// CountBuckets counts a set of host-named queries, such as the badges of an
// inbox, and returns one count per name. Each count equals [Service.Count] of
// that bucket's query alone.
//
// Each distinct candidate's groups are resolved once for the whole call. The
// counts run on the context given, so inside a host transaction they read the
// same state and agree with each other.
//
// More than [MaxCountBuckets] buckets, or any bucket with an unsupported
// ordering, is a validation error, and nothing is counted.
func (s *Service) CountBuckets(ctx context.Context, buckets map[string]Query) (map[string]int64, error) {
	if len(buckets) > MaxCountBuckets {
		return nil, &ValidationError{Subject: "request", Issues: []ValidationIssue{{
			Detail: fmt.Sprintf("%d buckets exceed the limit of %d", len(buckets), MaxCountBuckets),
		}}}
	}

	names := slices.Sorted(maps.Keys(buckets))

	for _, name := range names {
		if err := buckets[name].validate(); err != nil {
			return nil, err
		}
	}

	groups := make(map[string][]string)
	counts := make(map[string]int64, len(buckets))

	for _, name := range names {
		resolved, err := s.resolve(ctx, buckets[name], groups)
		if err != nil {
			return nil, err
		}

		count, err := s.store.Count(ctx, resolved)
		if err != nil {
			return nil, err
		}

		counts[name] = count
	}

	return counts, nil
}

// resolve validates a query and resolves its candidate's group membership. A
// non-nil cache holds memberships already resolved, and gains the ones this
// call resolves.
func (s *Service) resolve(ctx context.Context, query Query, cache map[string][]string) (ResolvedQuery, error) {
	if err := query.validate(); err != nil {
		return ResolvedQuery{}, err
	}

	resolved := ResolvedQuery{Query: query.Clone()}

	if query.Candidate == "" {
		return resolved, nil
	}

	if groups, ok := cache[query.Candidate]; ok {
		resolved.CandidateGroups = groups

		return resolved, nil
	}

	if s.resolver == nil {
		return ResolvedQuery{}, &GroupResolutionError{
			Actor: query.Candidate,
			Cause: &ConfigurationError{Detail: "no group resolver is configured"},
		}
	}

	groups, err := s.resolver.GroupsOf(ctx, query.Candidate)
	if err != nil {
		return ResolvedQuery{}, &GroupResolutionError{Actor: query.Candidate, Cause: err}
	}

	if cache != nil {
		cache[query.Candidate] = groups
	}

	resolved.CandidateGroups = groups

	return resolved, nil
}

// mutate is the pipeline every lifecycle operation runs: join the host's
// transaction, read the task, check the caller's version, compute the pure
// transition, write the new state conditionally, append history and the durable
// events, and then — only after a transaction the engine itself began has
// committed — dispatch.
func (s *Service) mutate(
	ctx context.Context,
	req TaskRequest,
	step func(ctx context.Context, task Task, now time.Time) (Task, []Event, []TransitionRecord, error),
) (Result, error) {
	if req.TaskID.IsZero() {
		return Result{}, &ValidationError{
			Subject: "request",
			Issues:  []ValidationIssue{{Pointer: "/taskId", Detail: "is required"}},
		}
	}

	hostLed := s.store.InTransaction(ctx)

	var (
		task   Task
		events []Event
	)

	err := s.store.Do(ctx, func(ctx context.Context) error {
		current, err := s.store.Get(ctx, req.TaskID)
		if err != nil {
			return err
		}

		if req.Version != nil && *req.Version != current.Version {
			return &ConflictError{
				TaskID: req.TaskID, Expected: *req.Version, Current: current.Version,
			}
		}

		next, produced, records, err := step(ctx, current, s.clock.Now())
		if err != nil {
			return err
		}

		if err := s.store.Update(ctx, next, current.Version); err != nil {
			return err
		}

		if err := s.persistOutcome(ctx, produced, records); err != nil {
			return err
		}

		task, events = next, produced

		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return s.finish(ctx, task, events, hostLed)
}

// persistOutcome writes the history records and the durable events of one
// transition, inside the transaction that produced them.
func (s *Service) persistOutcome(ctx context.Context, events []Event, records []TransitionRecord) error {
	if len(records) > 0 {
		if err := s.store.AppendHistory(ctx, records...); err != nil {
			return err
		}
	}

	if len(events) == 0 {
		return nil
	}

	stamped, err := s.stamp(events)
	if err != nil {
		return err
	}

	copy(events, stamped)

	return s.store.Append(ctx, stamped)
}

// finish decides who dispatches. When the engine began the transaction it has
// just committed and may deliver; when the host began it, the engine cannot
// observe the commit and hands the dispatch back.
func (s *Service) finish(ctx context.Context, task Task, events []Event, hostLed bool) (Result, error) {
	result := Result{Task: task, Events: events, service: s}

	if hostLed {
		result.pending = events

		return result, nil
	}

	if err := s.deliver(ctx, events); err != nil {
		s.onDispatchError(ctx, err)
	}

	return result, nil
}

// deliver runs the in-process handlers.
//
// It detaches the context from the request's cancellation while keeping its
// values, so that logging and tracing still work but a client hanging up in the
// instant after a commit does not drop the notification. Without that,
// notifications are lost under exactly the conditions where they matter.
func (s *Service) deliver(ctx context.Context, events []Event) error {
	if len(events) == 0 || len(s.handlers) == 0 {
		return nil
	}

	detached := context.WithoutCancel(ctx)

	var errs []error

	for _, event := range events {
		for _, handler := range s.handlers {
			if err := handler.HandleEvent(detached, event); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// stamp assigns an identifier to every event that lacks one.
func (s *Service) stamp(events []Event) ([]Event, error) {
	out := make([]Event, len(events))
	copy(out, events)

	for i := range out {
		if out[i].ID != "" {
			continue
		}

		id, err := s.eventIDs.NewTaskID()
		if err != nil {
			return nil, err
		}

		out[i].ID = id.String()
	}

	return out, nil
}

// escalationPolicy returns the policy governing a task: its own override, or
// its type's default.
func (s *Service) escalationPolicy(task Task) *EscalationPolicy {
	if task.Escalation != nil {
		return task.Escalation
	}

	spec, err := s.registry.Lookup(task.Type)
	if err != nil {
		return nil
	}

	return spec.DefaultEscalation
}

// recordsOf extracts the transition records carried by a set of events. There
// is exactly one record per accepted transition and one event per record, so
// this is a projection and not a decision.
func recordsOf(events []Event) []TransitionRecord {
	if len(events) == 0 {
		return nil
	}

	records := make([]TransitionRecord, 0, len(events))
	for _, event := range events {
		records = append(records, event.Transition)
	}

	return records
}

// defaultedPayload substitutes an empty JSON object for an absent payload, so
// that a schema requiring fields rejects "nothing supplied" for the same reason
// it rejects "{}" rather than failing to parse.
func defaultedPayload(payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 {
		return json.RawMessage(`{}`)
	}

	return payload
}
