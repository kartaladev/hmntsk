package hmntsk

import (
	"encoding/json"
	"slices"
	"time"
)

// CandidatePool expresses who may act on a task. An actor is eligible when
// they appear in Users, or belong to one of Groups as resolved by the host's
// [GroupResolver], and do not appear in Excluded. Exclusion always wins.
//
// Membership is resolved at the moment of the operation, never snapshotted at
// creation, so somebody who joins a candidate group today can act on a task
// created yesterday.
type CandidatePool struct {
	// Users are actor identifiers that are eligible outright.
	Users []string `json:"users,omitempty"`
	// Groups are group identifiers whose members are eligible.
	Groups []string `json:"groups,omitempty"`
	// Excluded are actor identifiers that are never eligible, whatever else
	// names them.
	Excluded []string `json:"excluded,omitempty"`
}

// IsEmpty reports whether the pool names nobody at all.
func (p CandidatePool) IsEmpty() bool {
	return len(p.Users) == 0 && len(p.Groups) == 0
}

// Clone returns a deep copy, so that the slices held by a stored pool and by a
// caller's copy cannot alias.
func (p CandidatePool) Clone() CandidatePool {
	return CandidatePool{
		Users:    slices.Clone(p.Users),
		Groups:   slices.Clone(p.Groups),
		Excluded: slices.Clone(p.Excluded),
	}
}

// Equal reports whether two pools name the same actors, order included.
func (p CandidatePool) Equal(other CandidatePool) bool {
	return slices.Equal(p.Users, other.Users) &&
		slices.Equal(p.Groups, other.Groups) &&
		slices.Equal(p.Excluded, other.Excluded)
}

// Task is the aggregate the engine owns. It is a value: lifecycle operations
// are pure functions that return a new Task rather than mutating the receiver,
// so a caller holding a Task never sees a state change that the database
// subsequently rolled back.
//
// Input, Progress and Output are opaque to the engine beyond JSON Schema
// validation. They are stored as supplied — field order, number literals and
// fields the schema does not describe all survive a round trip.
type Task struct {
	// ID identifies the task.
	ID TaskID `json:"id"`
	// Type names the registered task type this task is an instance of.
	Type string `json:"type"`
	// Version is the optimistic-concurrency token. Every accepted mutation
	// increments it, and every mutation is conditional on the value the caller
	// last observed.
	Version int64 `json:"version"`
	// Status is the task's lifecycle position. It never carries a business
	// outcome.
	Status Status `json:"status"`
	// SuspendedFrom holds the state to restore on resume. It is set only while
	// Status is [StatusSuspended], because the resume target is not derivable.
	SuspendedFrom Status `json:"suspendedFrom,omitempty"`
	// Priority orders the task against its peers; lower is more urgent.
	Priority Priority `json:"priority"`
	// Assignee is the actor currently holding the task, empty when it sits in
	// the pool.
	Assignee string `json:"assignee,omitempty"`
	// Candidates is the pool of actors who may claim the task.
	Candidates CandidatePool `json:"candidates"`
	// Correlation ties the task back to the work that asked for it.
	Correlation CorrelationData `json:"correlation,omitzero"`
	// Callback is where the host wants notifications delivered, if anywhere.
	Callback *CallbackTarget `json:"callback,omitempty"`
	// Escalation overrides the type's default escalation policy for this task.
	Escalation *EscalationPolicy `json:"escalation,omitempty"`
	// Input is the payload the task was created with.
	Input json.RawMessage `json:"input,omitempty"`
	// Progress is the assignee's partial work, built up by save-progress
	// patches. It is never validated for completeness.
	Progress json.RawMessage `json:"progress,omitempty"`
	// Output is the payload supplied at completion, validated in full against
	// the type's output schema.
	Output json.RawMessage `json:"output,omitempty"`
	// Reason records why a task failed, errored, was cancelled or was
	// superseded. It is free text for humans, not a machine-readable code.
	Reason string `json:"reason,omitempty"`
	// CreatedBy is the actor or system that created the task.
	CreatedBy string `json:"createdBy,omitempty"`
	// EscalationCount is how many times the task has been escalated.
	EscalationCount int `json:"escalationCount,omitempty"`

	// CreatedAt is when the task was created, UTC at microsecond precision.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the task last changed, UTC at microsecond precision.
	UpdatedAt time.Time `json:"updatedAt"`
	// DueAt is the deadline the escalation sweep measures against. Nil means
	// the task has no deadline and is never overdue.
	DueAt *time.Time `json:"dueAt,omitempty"`
	// StartedAt is when work first began.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// ClosedAt is when the task reached a terminal state.
	ClosedAt *time.Time `json:"closedAt,omitempty"`
	// EscalatedAt is when the task was last escalated.
	EscalatedAt *time.Time `json:"escalatedAt,omitempty"`

	// LockedBy identifies the sweeper currently holding the escalation lease.
	// It is part of the lease mechanism of design decision D10, not a lock on
	// ordinary lifecycle operations.
	LockedBy string `json:"lockedBy,omitempty"`
	// LockedUntil is when the escalation lease expires. A lease whose deadline
	// has passed is available to any sweeper, so a crashed sweeper does not
	// strand a task.
	LockedUntil *time.Time `json:"lockedUntil,omitempty"`
}

// Clone returns a deep copy. Every slice, map and pointer the Task holds is
// copied, so the result shares no mutable state with the receiver.
func (t Task) Clone() Task {
	out := t
	out.Candidates = t.Candidates.Clone()
	out.Correlation = t.Correlation.Clone()
	out.Escalation = t.Escalation.Clone()
	out.Input = cloneRaw(t.Input)
	out.Progress = cloneRaw(t.Progress)
	out.Output = cloneRaw(t.Output)

	if t.Callback != nil {
		cb := t.Callback.Clone()
		out.Callback = &cb
	}

	out.DueAt = cloneTime(t.DueAt)
	out.StartedAt = cloneTime(t.StartedAt)
	out.ClosedAt = cloneTime(t.ClosedAt)
	out.EscalatedAt = cloneTime(t.EscalatedAt)
	out.LockedUntil = cloneTime(t.LockedUntil)

	return out
}

// Normalize returns a copy of the task with every timestamp expressed in the
// form the engine stores: UTC, truncated to whole microseconds. Store adapters
// apply it on the way in so that a value written and read back compares equal.
func (t Task) Normalize() Task {
	out := t.Clone()
	out.CreatedAt = NormalizeTime(t.CreatedAt)
	out.UpdatedAt = NormalizeTime(t.UpdatedAt)
	out.DueAt = normalizeTimePtr(t.DueAt)
	out.StartedAt = normalizeTimePtr(t.StartedAt)
	out.ClosedAt = normalizeTimePtr(t.ClosedAt)
	out.EscalatedAt = normalizeTimePtr(t.EscalatedAt)
	out.LockedUntil = normalizeTimePtr(t.LockedUntil)

	return out
}

// IsOverdue reports whether the task has a deadline that now has passed.
// Terminal and suspended tasks are never overdue, because they are never
// escalated.
func (t Task) IsOverdue(now time.Time) bool {
	if t.DueAt == nil || t.Status.IsTerminal() || t.Status == StatusSuspended {
		return false
	}

	return !now.Before(*t.DueAt)
}

// IsLeased reports whether an escalation lease is held and still valid at now.
func (t Task) IsLeased(now time.Time) bool {
	return t.LockedUntil != nil && now.Before(*t.LockedUntil)
}

// NormalizeTime rounds a timestamp to the precision and zone the engine stores:
// UTC, truncated to whole microseconds. Every supported dialect can represent
// that exactly, which is what makes a stored deadline read back identical.
func NormalizeTime(ts time.Time) time.Time {
	return ts.UTC().Truncate(time.Microsecond)
}

// normalizeTimePtr applies [NormalizeTime] through a pointer, preserving nil.
func normalizeTimePtr(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}

	out := NormalizeTime(*ts)

	return &out
}

// cloneTime copies a time pointer, preserving nil.
func cloneTime(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}

	out := *ts

	return &out
}
