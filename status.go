package hmntsk

import "fmt"

// Status is a task's position in the lifecycle. It describes only where the
// task has got to and never the outcome of the work: an approval that was
// denied is a [StatusCompleted] task whose output records the denial. See the
// task-lifecycle capability for the full transition table.
type Status string

// The ten states a task may occupy.
const (
	// StatusCreated is the state of a task that exists but has not yet been
	// offered to anyone; assignment resolution has not completed.
	StatusCreated Status = "CREATED"
	// StatusReady is the state of a task offered to its candidate pool and
	// claimable by any eligible actor.
	StatusReady Status = "READY"
	// StatusReserved is the state of a task claimed by, or assigned to, a
	// single actor who has not started work.
	StatusReserved Status = "RESERVED"
	// StatusInProgress is the state of a task its assignee has started.
	StatusInProgress Status = "IN_PROGRESS"
	// StatusSuspended is the state of a task temporarily withdrawn from
	// circulation; resuming returns it to the state recorded in
	// [Task.SuspendedFrom].
	StatusSuspended Status = "SUSPENDED"
	// StatusCompleted is the terminal state of a task whose actor supplied an
	// output, whatever that output says.
	StatusCompleted Status = "COMPLETED"
	// StatusFailed is the terminal state of a task whose actor reported that
	// they could not perform the work.
	StatusFailed Status = "FAILED"
	// StatusError is the terminal state of a task that hit a system-originated
	// fault, such as assignment resolution failing or an output that cannot be
	// validated. It is never an actor's verdict on the work.
	StatusError Status = "ERROR"
	// StatusExited is the terminal state of a cancelled task.
	StatusExited Status = "EXITED"
	// StatusObsolete is the terminal state of a task superseded by escalation.
	StatusObsolete Status = "OBSOLETE"
)

// allStatuses lists the ten states in lifecycle order. The order is part of the
// published contract only in that it is stable; nothing depends on it.
var allStatuses = [...]Status{
	StatusCreated,
	StatusReady,
	StatusReserved,
	StatusInProgress,
	StatusSuspended,
	StatusCompleted,
	StatusFailed,
	StatusError,
	StatusExited,
	StatusObsolete,
}

// Statuses returns the ten states a task may occupy, in lifecycle order.
func Statuses() []Status {
	out := make([]Status, len(allStatuses))
	copy(out, allStatuses[:])

	return out
}

// String implements [fmt.Stringer].
func (s Status) String() string { return string(s) }

// Valid reports whether s is one of the ten defined states.
func (s Status) Valid() bool {
	for _, candidate := range allStatuses {
		if s == candidate {
			return true
		}
	}

	return false
}

// IsTerminal reports whether no operation may move a task out of s. The
// terminal states are [StatusCompleted], [StatusFailed], [StatusError],
// [StatusExited] and [StatusObsolete].
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusError, StatusExited, StatusObsolete:
		return true
	case StatusCreated, StatusReady, StatusReserved, StatusInProgress, StatusSuspended:
		return false
	default:
		return false
	}
}

// IsSuspendable reports whether a task in s may be suspended. Only [StatusReady],
// [StatusReserved] and [StatusInProgress] qualify, which is also the set of
// states [Task.SuspendedFrom] may hold.
func (s Status) IsSuspendable() bool {
	switch s {
	case StatusReady, StatusReserved, StatusInProgress:
		return true
	case StatusCreated, StatusSuspended, StatusCompleted, StatusFailed,
		StatusError, StatusExited, StatusObsolete:
		return false
	default:
		return false
	}
}

// Priority orders a task against its peers. Lower values are more urgent, as in
// WS-HumanTask: 0 is the most urgent and 10 the least.
type Priority int

// The bounds and the neutral value of the priority scale.
const (
	// PriorityHighest is the most urgent priority.
	PriorityHighest Priority = 0
	// PriorityDefault is the priority a task carries when neither the task nor
	// its type specifies one.
	PriorityDefault Priority = 5
	// PriorityLowest is the least urgent priority.
	PriorityLowest Priority = 10
)

// Valid reports whether p lies within the inclusive range
// [PriorityHighest, PriorityLowest].
func (p Priority) Valid() bool {
	return p >= PriorityHighest && p <= PriorityLowest
}

// String implements [fmt.Stringer].
func (p Priority) String() string { return fmt.Sprintf("%d", int(p)) }
