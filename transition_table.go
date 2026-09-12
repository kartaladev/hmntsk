package hmntsk

import "slices"

// Transition is one ordered pair of states. The engine permits exactly the
// pairs listed in [LegalTransitions] and rejects every other pair with an
// error matching [ErrIllegalTransition].
type Transition struct {
	// From is the state the task is in.
	From Status
	// To is the state the operation would move it to.
	To Status
}

// String implements [fmt.Stringer].
func (t Transition) String() string { return t.From.String() + "->" + t.To.String() }

// legalTransitions is the state machine as data. It is the single authority on
// what may happen next; the operations in transitions.go consult it rather than
// restating it, so an operation cannot quietly permit a move the table forbids.
//
// Notes on the less obvious entries:
//
//   - RESERVED->RESERVED is delegation, which reassigns without releasing.
//   - IN_PROGRESS->RESERVED is also delegation; the new assignee restarts.
//   - SUSPENDED fans out to the three suspendable states because resuming
//     restores whichever one the task came from, recorded in
//     [Task.SuspendedFrom].
//   - There is deliberately no CREATED->EXITED entry. CREATED exists only for
//     the duration of the create operation, before assignment resolution
//     decides between READY, RESERVED and ERROR; no stored task is ever in it,
//     so nothing can cancel one.
//   - There is deliberately no IN_PROGRESS->OBSOLETE entry. Superseding work
//     somebody has already started would discard it silently; escalation of an
//     in-progress task widens or exempts instead.
var legalTransitions = []Transition{
	{StatusCreated, StatusReady},
	{StatusCreated, StatusReserved},
	{StatusCreated, StatusError},

	{StatusReady, StatusReserved},
	{StatusReady, StatusSuspended},
	{StatusReady, StatusObsolete},
	{StatusReady, StatusExited},

	{StatusReserved, StatusReady},
	{StatusReserved, StatusInProgress},
	{StatusReserved, StatusReserved},
	{StatusReserved, StatusSuspended},
	{StatusReserved, StatusObsolete},
	{StatusReserved, StatusExited},

	{StatusInProgress, StatusReserved},
	{StatusInProgress, StatusCompleted},
	{StatusInProgress, StatusFailed},
	{StatusInProgress, StatusError},
	{StatusInProgress, StatusSuspended},
	{StatusInProgress, StatusExited},

	{StatusSuspended, StatusReady},
	{StatusSuspended, StatusReserved},
	{StatusSuspended, StatusInProgress},
	{StatusSuspended, StatusExited},
}

// transitionIndex is legalTransitions in lookup form, built once at
// initialisation so that [CanTransition] is a map probe.
var transitionIndex = func() map[Transition]struct{} {
	index := make(map[Transition]struct{}, len(legalTransitions))
	for _, t := range legalTransitions {
		index[t] = struct{}{}
	}

	return index
}()

// LegalTransitions returns every permitted state pair, in table order. The
// returned slice is a copy; the caller may keep or sort it freely.
func LegalTransitions() []Transition {
	return slices.Clone(legalTransitions)
}

// CanTransition reports whether a task may move from one state to another. It
// is the only place that decides, and every lifecycle operation goes through
// it.
func CanTransition(from, to Status) bool {
	_, ok := transitionIndex[Transition{From: from, To: to}]

	return ok
}

// AllowedTransitions returns the states a task in from may move to, in table
// order. It returns an empty slice for a terminal or unknown state.
func AllowedTransitions(from Status) []Status {
	out := make([]Status, 0, 6)

	for _, t := range legalTransitions {
		if t.From == from {
			out = append(out, t.To)
		}
	}

	return out
}
