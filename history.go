package hmntsk

import "time"

// Operation names a lifecycle operation. Operations are the only mutation path:
// nothing in the API sets a task's status directly.
type Operation string

// The lifecycle operations.
const (
	// OpCreate brings a task into existence and resolves its assignment,
	// leaving it in READY, RESERVED or ERROR.
	OpCreate Operation = "Create"
	// OpClaim reserves a pooled task for an eligible actor.
	OpClaim Operation = "Claim"
	// OpRelease returns a reserved task to the pool.
	OpRelease Operation = "Release"
	// OpStart moves a reserved task to IN_PROGRESS.
	OpStart Operation = "Start"
	// OpSaveProgress records partial work. It transitions only the first time,
	// and never produces an event.
	OpSaveProgress Operation = "SaveProgress"
	// OpComplete records the actor's output and closes the task.
	OpComplete Operation = "Complete"
	// OpFail records that the actor could not do the work.
	OpFail Operation = "Fail"
	// OpDelegate reassigns a task to another eligible actor.
	OpDelegate Operation = "Delegate"
	// OpSuspend withdraws a task from circulation.
	OpSuspend Operation = "Suspend"
	// OpResume returns a suspended task to the state it came from.
	OpResume Operation = "Resume"
	// OpEscalate applies the task's escalation policy.
	OpEscalate Operation = "Escalate"
	// OpCancel closes a task at its owner's request.
	OpCancel Operation = "Cancel"
	// OpObsolete closes a task that escalation has superseded.
	OpObsolete Operation = "Obsolete"
	// OpFault closes a task that the system could not carry forward, such as
	// one whose assignment could not be resolved.
	OpFault Operation = "Fault"
)

// String implements [fmt.Stringer].
func (o Operation) String() string { return string(o) }

// TransitionRecord is the audit row written for one accepted lifecycle
// transition. Exactly one is produced per accepted transition, and history is
// append-only: earlier records are never rewritten.
//
// Progress saves are deliberately absent from this log except for the implicit
// RESERVED to IN_PROGRESS move on the first save. Autosave traffic is recorded
// as patch operations instead, so it does not drown the transition history.
type TransitionRecord struct {
	// TaskID is the task the transition belongs to.
	TaskID TaskID `json:"taskId"`
	// Version is the task's version after the transition, which orders the
	// records of one task without a separate sequence column.
	Version int64 `json:"version"`
	// Operation is the lifecycle operation that caused the transition.
	Operation Operation `json:"operation"`
	// From is the state the task was in.
	From Status `json:"from"`
	// To is the state the task moved to. It equals From for a transition that
	// changes the task without moving it, such as escalation by widening.
	To Status `json:"to"`
	// Actor is who performed the operation. It is empty for transitions the
	// system performed on its own behalf, such as a fault.
	Actor string `json:"actor,omitempty"`
	// Comment is the free text the caller supplied, such as a failure reason
	// or a note on a delegation.
	Comment string `json:"comment,omitempty"`
	// At is when the transition happened, UTC at microsecond precision.
	At time.Time `json:"at"`
}
