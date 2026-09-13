package hmntsk

import (
	"encoding/json"
	"slices"
	"time"
)

// The lifecycle transitions are pure functions on [Task]. Each returns a new
// task value, the events the transition produced, and an error; none mutates
// the receiver, on success or on failure.
//
// That matters because the write can still roll back underneath the caller. A
// mutating receiver would leave a caller holding a task that says RESERVED
// after a claim the database refused. It also makes the whole state machine
// table-testable with no infrastructure at all.
//
// These functions enforce the state machine and the assignee rule, both of
// which are decidable from the task alone. They do not resolve group
// membership: eligibility needs the host's [GroupResolver] and therefore a
// context, so the [Service] checks it before calling in.

// Activate completes creation by placing a new task in the pool or reserving it
// for the single actor assignment resolved to. It moves CREATED to READY when
// assignee is empty and to RESERVED otherwise.
func (t Task) Activate(actor, assignee string, now time.Time) (Task, []Event, error) {
	to := StatusReady
	if assignee != "" {
		to = StatusReserved
	}

	if err := t.requireFrom(OpCreate, to, StatusCreated); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpCreate, to, actor, "", now, func(next *Task) {
		next.Assignee = assignee
	})

	return next, events, nil
}

// Fault closes a task that the system could not carry forward: assignment
// resolution failed, or an output could not be validated. It is never an
// actor's verdict on the work — that is [Task.Fail].
func (t Task) Fault(reason string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpFault, StatusError, StatusCreated, StatusInProgress); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpFault, StatusError, "", reason, now, func(next *Task) {
		next.Reason = reason
	})

	return next, events, nil
}

// Claim reserves a pooled task for actor. The caller is responsible for having
// established that actor is eligible.
func (t Task) Claim(actor string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpClaim, StatusReserved, StatusReady); err != nil {
		return t, nil, err
	}

	if err := requireActor(t.ID, OpClaim, actor); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpClaim, StatusReserved, actor, "", now, func(next *Task) {
		next.Assignee = actor
	})

	return next, events, nil
}

// Release returns a reserved task to its pool. The candidate pool is left
// exactly as it was, so the actor who released it may claim it again.
func (t Task) Release(actor, comment string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpRelease, StatusReady, StatusReserved); err != nil {
		return t, nil, err
	}

	if err := t.requireAssignee(OpRelease, actor); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpRelease, StatusReady, actor, comment, now, func(next *Task) {
		next.Assignee = ""
	})

	return next, events, nil
}

// Start moves a reserved task to IN_PROGRESS. The first successful progress
// save performs the same move implicitly.
func (t Task) Start(actor string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpStart, StatusInProgress, StatusReserved); err != nil {
		return t, nil, err
	}

	if err := t.requireAssignee(OpStart, actor); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpStart, StatusInProgress, actor, "", now, func(next *Task) {
		if next.StartedAt == nil {
			started := NormalizeTime(now)
			next.StartedAt = &started
		}
	})

	return next, events, nil
}

// Complete closes a task with the actor's output. The output's meaning is the
// task type's business, not the engine's: an approval that was denied completes
// here exactly as one that was granted, and the two are distinguishable only by
// reading the output.
//
// The caller is responsible for having validated output against the type's
// output schema.
func (t Task) Complete(actor string, output json.RawMessage, comment string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpComplete, StatusCompleted, StatusInProgress); err != nil {
		return t, nil, err
	}

	if err := t.requireAssignee(OpComplete, actor); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpComplete, StatusCompleted, actor, comment, now, func(next *Task) {
		next.Output = cloneRaw(output)
	})
	events[0].Output = cloneRaw(output)

	return next, events, nil
}

// Fail records that the actor could not perform the work. No output is
// required, and the reason is kept in history.
func (t Task) Fail(actor, reason string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpFail, StatusFailed, StatusInProgress); err != nil {
		return t, nil, err
	}

	if err := t.requireAssignee(OpFail, actor); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpFail, StatusFailed, actor, reason, now, func(next *Task) {
		next.Reason = reason
	})

	return next, events, nil
}

// Delegate reassigns a task to another actor without losing work. The task
// becomes RESERVED for the new assignee and keeps any saved progress; if it had
// been started, StartedAt is kept too, because the work itself did not restart.
//
// The caller is responsible for having established that target is eligible.
func (t Task) Delegate(actor, target, comment string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpDelegate, StatusReserved, StatusReserved, StatusInProgress); err != nil {
		return t, nil, err
	}

	if err := t.requireAssignee(OpDelegate, actor); err != nil {
		return t, nil, err
	}

	if target == "" {
		return t, nil, &ValidationError{
			Subject: "request",
			Issues:  []ValidationIssue{{Pointer: "/delegate", Detail: "no delegate was named"}},
		}
	}

	next, events := t.record(OpDelegate, StatusReserved, actor, comment, now, func(next *Task) {
		next.Assignee = target
	})

	return next, events, nil
}

// Suspend withdraws a task from circulation, recording the state to return to.
// A suspended task is never escalated.
func (t Task) Suspend(actor, comment string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpSuspend, StatusSuspended, StatusReady, StatusReserved, StatusInProgress); err != nil {
		return t, nil, err
	}

	if err := t.requireAssigneeIfHeld(OpSuspend, actor); err != nil {
		return t, nil, err
	}

	from := t.Status

	next, events := t.record(OpSuspend, StatusSuspended, actor, comment, now, func(next *Task) {
		next.SuspendedFrom = from
	})

	return next, events, nil
}

// Resume returns a suspended task to the exact state it occupied before
// suspension, with its assignee and saved progress intact.
func (t Task) Resume(actor, comment string, now time.Time) (Task, []Event, error) {
	if !t.SuspendedFrom.IsSuspendable() {
		return t, nil, &TransitionError{
			TaskID: t.ID, Operation: OpResume.String(), From: t.Status, To: t.SuspendedFrom,
		}
	}

	if err := t.requireFrom(OpResume, t.SuspendedFrom, StatusSuspended); err != nil {
		return t, nil, err
	}

	if err := t.requireAssigneeIfHeld(OpResume, actor); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpResume, t.SuspendedFrom, actor, comment, now, func(next *Task) {
		next.SuspendedFrom = ""
	})

	return next, events, nil
}

// Escalate applies the task's escalation policy. It is an ordinary lifecycle
// operation: the sweep and a direct operator call take exactly the same path.
//
// A policy that widens adds its users and groups to the candidate pool and
// leaves the status alone, so everyone already eligible stays eligible. A
// policy that supersedes hands off to [Task.Obsolete], which is where the
// obsolescence event comes from. A nil policy, or one that names no action,
// only announces.
//
// The escalation lease is deliberately left in place. Widening does not move
// the deadline, so the task is still overdue the instant this returns; without
// the lease the next sweep would escalate it again immediately, and again after
// that. Leaving the lease to expire gives one escalation per lease period,
// which is the back-off, and a policy's MaxEscalations caps the total.
//
// The engine notifies nobody. Choosing a channel and sending a message belongs
// to a consumer of the event this produces.
func (t Task) Escalate(actor string, policy *EscalationPolicy, comment string, now time.Time) (Task, []Event, error) {
	if policy != nil && policy.Action == EscalationSupersede {
		return t.Obsolete(actor, comment, now)
	}

	if !slices.Contains([]Status{StatusReady, StatusReserved, StatusInProgress}, t.Status) {
		return t, nil, &TransitionError{
			TaskID: t.ID, Operation: OpEscalate.String(), From: t.Status, To: t.Status,
		}
	}

	// Escalation by widening does not move the task, so it is the one operation
	// that does not consult the transition table: there is no self-transition
	// to permit. Everything else about it is an ordinary transition.
	next, events := t.record(OpEscalate, t.Status, actor, comment, now, func(next *Task) {
		next.EscalationCount = t.EscalationCount + 1
		next.EscalatedAt = timePtr(now)

		if policy != nil && policy.Action == EscalationWiden {
			next.Candidates.Users = appendMissing(next.Candidates.Users, policy.AddUsers)
			next.Candidates.Groups = appendMissing(next.Candidates.Groups, policy.AddGroups)
		}
	})

	return next, events, nil
}

// Obsolete closes a task that escalation has superseded. It accepts no further
// operations afterwards.
func (t Task) Obsolete(actor, reason string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(OpObsolete, StatusObsolete, StatusReady, StatusReserved); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpObsolete, StatusObsolete, actor, reason, now, func(next *Task) {
		next.Reason = reason
	})

	return next, events, nil
}

// Cancel closes a task at its owner's request, from any state a stored task can
// be in.
func (t Task) Cancel(actor, reason string, now time.Time) (Task, []Event, error) {
	if err := t.requireFrom(
		OpCancel, StatusExited,
		StatusReady, StatusReserved, StatusInProgress, StatusSuspended,
	); err != nil {
		return t, nil, err
	}

	next, events := t.record(OpCancel, StatusExited, actor, reason, now, func(next *Task) {
		next.Reason = reason
	})

	return next, events, nil
}

// requireFrom validates a transition against the state machine. from lists the
// states the operation itself accepts, which is usually narrower than the
// transition table: the table permits RESERVED to RESERVED for delegation, but
// that does not make it a legal claim.
//
// Every operation runs this check before it checks the actor, because the
// lifecycle capability requires an operation that is illegal from the task's
// present state to fail as a conflict even when the actor is also wrong. A
// pooled task has no assignee, so checking the actor first would report every
// premature completion as a permission problem.
func (t Task) requireFrom(op Operation, to Status, from ...Status) error {
	if slices.Contains(from, t.Status) && CanTransition(t.Status, to) {
		return nil
	}

	return &TransitionError{TaskID: t.ID, Operation: op.String(), From: t.Status, To: to}
}

// record builds the next task value, its transition record and its event. It
// asks no questions: every caller has already decided the transition is legal.
func (t Task) record(
	op Operation,
	to Status,
	actor, comment string,
	now time.Time,
	mutate func(next *Task),
) (Task, []Event) {
	at := NormalizeTime(now)

	next := t.Clone()
	next.Status = to
	next.Version = t.Version + 1
	next.UpdatedAt = at

	if mutate != nil {
		mutate(&next)
	}

	if to.IsTerminal() {
		if next.ClosedAt == nil {
			closed := at
			next.ClosedAt = &closed
		}

		next.LockedBy = ""
		next.LockedUntil = nil
	}

	transition := TransitionRecord{
		TaskID:    t.ID,
		Version:   next.Version,
		Operation: op,
		From:      t.Status,
		To:        to,
		Actor:     actor,
		Comment:   comment,
		At:        at,
	}

	event := Event{
		Type:        operationEventTypes[op],
		TaskID:      next.ID,
		TaskType:    next.Type,
		Status:      next.Status,
		Version:     next.Version,
		Actor:       actor,
		Assignee:    next.Assignee,
		Candidates:  snapshotPool(next.Candidates),
		CreatedBy:   next.CreatedBy,
		OccurredAt:  at,
		Correlation: next.Correlation.Clone(),
		Transition:  transition,
	}

	// A holder is named only when there was one and the transition replaced
	// them, so that "the holder changed" is something a consumer reads rather
	// than computes.
	if t.Assignee != "" && t.Assignee != next.Assignee {
		event.PreviousAssignee = t.Assignee
	}

	if next.Callback != nil {
		callback := next.Callback.Clone()
		event.Callback = &callback
	}

	switch op {
	case OpFail, OpCancel, OpObsolete, OpFault:
		event.Reason = comment
	case OpCreate, OpClaim, OpRelease, OpStart, OpSaveProgress, OpComplete,
		OpDelegate, OpSuspend, OpResume, OpEscalate:
	}

	return next, []Event{event}
}

// snapshotPool copies a pool for an event, so that the event never aliases the
// task's slices. Empty slices become nil: a store that round-trips the event
// through JSON drops them either way, and the snapshot must read back equal on
// every store.
func snapshotPool(pool CandidatePool) CandidatePool {
	return CandidatePool{
		Users:    cloneNonEmpty(pool.Users),
		Groups:   cloneNonEmpty(pool.Groups),
		Excluded: cloneNonEmpty(pool.Excluded),
	}
}

// cloneNonEmpty copies values, returning nil rather than an empty slice.
func cloneNonEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	return slices.Clone(values)
}

// requireActor rejects an operation invoked without an acting actor.
func requireActor(id TaskID, op Operation, actor string) error {
	if actor != "" {
		return nil
	}

	return &AuthorizationError{TaskID: id, Operation: op.String(), Reason: "no acting actor was supplied"}
}

// requireAssignee rejects any actor other than the task's current assignee.
// Comparison is case-sensitive on every supported dialect, which is why the
// identifier columns pin their collation.
func (t Task) requireAssignee(op Operation, actor string) error {
	if err := requireActor(t.ID, op, actor); err != nil {
		return err
	}

	if t.Assignee == "" {
		return &AuthorizationError{
			TaskID: t.ID, Actor: actor, Operation: op.String(),
			Reason: "task has no assignee",
		}
	}

	if t.Assignee != actor {
		return &AuthorizationError{
			TaskID: t.ID, Actor: actor, Operation: op.String(),
			Reason: "actor is not the assignee",
		}
	}

	return nil
}

// requireAssigneeIfHeld applies the assignee rule only when somebody holds the
// task. Suspending a pooled task has no assignee to check against.
func (t Task) requireAssigneeIfHeld(op Operation, actor string) error {
	if t.Assignee == "" {
		return requireActor(t.ID, op, actor)
	}

	return t.requireAssignee(op, actor)
}

// appendMissing appends the entries of extra that base does not already hold,
// preserving the order of both and never aliasing either.
func appendMissing(base, extra []string) []string {
	out := slices.Clone(base)

	for _, candidate := range extra {
		if !slices.Contains(out, candidate) {
			out = append(out, candidate)
		}
	}

	return out
}

// timePtr returns a pointer to a normalized copy of ts.
func timePtr(ts time.Time) *time.Time {
	out := NormalizeTime(ts)

	return &out
}
