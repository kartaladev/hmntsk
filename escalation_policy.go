package hmntsk

import "slices"

// EscalationAction says what escalating a task does to it, beyond producing an
// escalation event.
type EscalationAction string

// The escalation actions.
const (
	// EscalationNotify produces the escalation event and changes nothing else.
	// It is the zero value, so a policy that names no action only announces.
	EscalationNotify EscalationAction = ""
	// EscalationWiden adds the policy's users and groups to the task's
	// candidate pool. Actors already eligible stay eligible.
	EscalationWiden EscalationAction = "WIDEN"
	// EscalationSupersede moves the task to [StatusObsolete], because the work
	// has been replaced rather than reassigned.
	EscalationSupersede EscalationAction = "SUPERSEDE"
)

// Valid reports whether a is a defined action.
func (a EscalationAction) Valid() bool {
	switch a {
	case EscalationNotify, EscalationWiden, EscalationSupersede:
		return true
	default:
		return false
	}
}

// String implements [fmt.Stringer].
func (a EscalationAction) String() string {
	if a == EscalationNotify {
		return "NOTIFY"
	}

	return string(a)
}

// EscalationPolicy describes what happens when a task passes its deadline. A
// type supplies a default through its [TypeSpec]; a task may override it at
// creation.
//
// The engine never notifies anyone. It applies the policy, records the
// transition and produces an event; choosing a channel and sending a message
// belongs to a consumer of that event.
type EscalationPolicy struct {
	// Action is what escalation does to the task itself.
	Action EscalationAction `json:"action,omitempty"`
	// AddUsers are candidate users added to the pool by [EscalationWiden].
	AddUsers []string `json:"addUsers,omitempty"`
	// AddGroups are candidate groups added to the pool by [EscalationWiden].
	AddGroups []string `json:"addGroups,omitempty"`
	// ExemptInProgress leaves a task alone once its assignee has started work,
	// on the grounds that somebody is already on it.
	ExemptInProgress bool `json:"exemptInProgress,omitempty"`
	// MaxEscalations caps how many times one task may be escalated. Zero means
	// no cap.
	MaxEscalations int `json:"maxEscalations,omitempty"`
}

// Clone returns a deep copy, so that the slices held by a stored policy and by
// a caller's copy cannot alias.
func (p *EscalationPolicy) Clone() *EscalationPolicy {
	if p == nil {
		return nil
	}

	out := *p
	out.AddUsers = slices.Clone(p.AddUsers)
	out.AddGroups = slices.Clone(p.AddGroups)

	return &out
}

// Equal reports whether two policies describe the same behaviour. It is used to
// decide whether a repeated type registration is identical or conflicting.
func (p *EscalationPolicy) Equal(other *EscalationPolicy) bool {
	if p == nil || other == nil {
		return p == nil && other == nil
	}

	return p.Action == other.Action &&
		p.ExemptInProgress == other.ExemptInProgress &&
		p.MaxEscalations == other.MaxEscalations &&
		slices.Equal(p.AddUsers, other.AddUsers) &&
		slices.Equal(p.AddGroups, other.AddGroups)
}
