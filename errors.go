package hmntsk

import (
	"errors"
	"fmt"
	"strings"
)

// The engine's error taxonomy. Callers match on these with [errors.Is]; the
// concrete types below carry the detail and satisfy the matching sentinel.
//
// Two of them are deliberately nested, because two capabilities describe the
// same condition at different resolutions:
//
//   - [ErrIllegalTransition] matches [ErrConflict], because the lifecycle
//     capability calls a refused transition "a conflict error" and the HTTP
//     contract maps both to 409. It stays distinguishable in the other
//     direction: a stale-version conflict does not match ErrIllegalTransition.
//   - [ErrUnregisteredType] matches [ErrValidation], because the task-type
//     capability calls an unknown type "a validation error" and the HTTP
//     contract maps both to 400.
var (
	// ErrConflict reports that the task could not be changed because the
	// caller's view of it is no longer current, or because the change is not
	// legal from the task's present state. Maps to HTTP 409.
	ErrConflict = errors.New("hmntsk: conflict")

	// ErrIllegalTransition reports that the requested operation is not a legal
	// move from the task's present state. Maps to HTTP 409.
	ErrIllegalTransition = fmt.Errorf("%w: illegal transition", ErrConflict)

	// ErrNotFound reports that no task, type or route with the given identity
	// exists. Maps to HTTP 404.
	ErrNotFound = errors.New("hmntsk: not found")

	// ErrValidation reports that supplied data does not satisfy the schema or
	// the request contract. Maps to HTTP 400.
	ErrValidation = errors.New("hmntsk: validation failed")

	// ErrUnregisteredType reports that the named task type was never
	// registered. Maps to HTTP 400.
	ErrUnregisteredType = fmt.Errorf("%w: task type is not registered", ErrValidation)

	// ErrUnauthorized reports that the acting actor is not permitted to
	// perform the operation: not eligible for the task, or not its assignee.
	// Maps to HTTP 403.
	ErrUnauthorized = errors.New("hmntsk: actor not authorised")

	// ErrGroupResolution reports that the host's [GroupResolver] failed. It is
	// a fault, not a verdict: the engine could not decide eligibility, which is
	// a different outcome from deciding against the actor. It never maps to
	// HTTP 403.
	ErrGroupResolution = errors.New("hmntsk: group resolution failed")

	// ErrConfiguration reports a wiring mistake detected at construction, such
	// as a durable event sink that cannot join the host's transaction.
	ErrConfiguration = errors.New("hmntsk: invalid configuration")
)

// ConflictError reports a mutation refused because the caller's version is no
// longer current. It names the current version so a client can re-read, merge
// and retry, which is what the HTTP contract's 409 body carries.
type ConflictError struct {
	// TaskID is the task the mutation targeted.
	TaskID TaskID
	// Expected is the version the caller supplied.
	Expected int64
	// Current is the version the task actually carries. It is zero when the
	// current version could not be read.
	Current int64
}

// Error implements the error interface.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("hmntsk: conflict on task %s: expected version %d, current version %d",
		e.TaskID, e.Expected, e.Current)
}

// Unwrap makes the error match [ErrConflict].
func (e *ConflictError) Unwrap() error { return ErrConflict }

// TransitionError reports an operation that is not legal from the task's
// present state, including every operation attempted on a terminal task.
type TransitionError struct {
	// TaskID is the task the operation targeted.
	TaskID TaskID
	// Operation names the lifecycle operation that was attempted.
	Operation string
	// From is the state the task was in.
	From Status
	// To is the state the operation would have moved it to. It is empty when
	// the operation has no single target state.
	To Status
}

// Error implements the error interface.
func (e *TransitionError) Error() string {
	if e.To == "" {
		return fmt.Sprintf("hmntsk: %s is not legal on task %s in state %s", e.Operation, e.TaskID, e.From)
	}

	return fmt.Sprintf("hmntsk: %s cannot move task %s from %s to %s",
		e.Operation, e.TaskID, e.From, e.To)
}

// Unwrap makes the error match [ErrIllegalTransition] and, through it,
// [ErrConflict].
func (e *TransitionError) Unwrap() error { return ErrIllegalTransition }

// NotFoundError reports that a task does not exist.
type NotFoundError struct {
	// TaskID is the identifier that was looked up.
	TaskID TaskID
}

// Error implements the error interface.
func (e *NotFoundError) Error() string {
	return fmt.Sprintf("hmntsk: task %s not found", e.TaskID)
}

// Unwrap makes the error match [ErrNotFound].
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// ValidationIssue is one thing wrong with a payload or a request.
type ValidationIssue struct {
	// Pointer is an RFC 6901 JSON Pointer into the offending document, empty
	// when the problem is the document as a whole.
	Pointer string `json:"pointer,omitempty"`
	// Detail describes what is wrong, in terms a client can show a user.
	Detail string `json:"detail"`
}

// String implements [fmt.Stringer].
func (i ValidationIssue) String() string {
	if i.Pointer == "" {
		return i.Detail
	}

	return i.Pointer + ": " + i.Detail
}

// ValidationError reports data that does not satisfy its schema or the request
// contract. It carries every problem found rather than only the first, so a
// client can show a whole form's worth of errors at once.
type ValidationError struct {
	// Subject names what was validated: "input", "output", "progress" or
	// "request".
	Subject string
	// Issues lists every problem found, in document order.
	Issues []ValidationIssue
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	subject := e.Subject
	if subject == "" {
		subject = "value"
	}

	if len(e.Issues) == 0 {
		return "hmntsk: " + subject + " is not valid"
	}

	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.String())
	}

	return "hmntsk: " + subject + " is not valid: " + strings.Join(parts, "; ")
}

// Unwrap makes the error match [ErrValidation].
func (e *ValidationError) Unwrap() error { return ErrValidation }

// UnregisteredTypeError reports a task type that was never registered. It names
// the type, which is what the HTTP contract's 400 body carries.
type UnregisteredTypeError struct {
	// Type is the unknown type name.
	Type string
}

// Error implements the error interface.
func (e *UnregisteredTypeError) Error() string {
	return fmt.Sprintf("hmntsk: task type %q is not registered", e.Type)
}

// Unwrap makes the error match [ErrUnregisteredType] and, through it,
// [ErrValidation].
func (e *UnregisteredTypeError) Unwrap() error { return ErrUnregisteredType }

// AuthorizationError reports an actor who may not perform the operation,
// whether because they are outside the candidate pool or because they are not
// the assignee.
type AuthorizationError struct {
	// TaskID is the task the operation targeted.
	TaskID TaskID
	// Actor is the actor that attempted it.
	Actor string
	// Operation names the lifecycle operation that was attempted.
	Operation string
	// Reason says which check failed, in terms safe to return to a client.
	Reason string
}

// Error implements the error interface.
func (e *AuthorizationError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "actor is not permitted"
	}

	return fmt.Sprintf("hmntsk: %s refused on task %s for actor %q: %s",
		e.Operation, e.TaskID, e.Actor, reason)
}

// Unwrap makes the error match [ErrUnauthorized].
func (e *AuthorizationError) Unwrap() error { return ErrUnauthorized }

// GroupResolutionError reports that the host's [GroupResolver] could not answer.
// It is deliberately not an [AuthorizationError]: the engine did not decide
// against the actor, it failed to decide at all, and a host that maps this to
// 403 would be telling users they lack a permission they may well have.
type GroupResolutionError struct {
	// Actor is the actor whose membership was being resolved, if known.
	Actor string
	// Groups are the groups the resolver was asked about.
	Groups []string
	// Cause is the resolver's own error.
	Cause error
}

// Error implements the error interface.
func (e *GroupResolutionError) Error() string {
	return fmt.Sprintf("hmntsk: resolving groups %v for actor %q: %v", e.Groups, e.Actor, e.Cause)
}

// Unwrap makes the error match both [ErrGroupResolution] and the resolver's own
// error, so a host can still inspect its cause.
func (e *GroupResolutionError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrGroupResolution}
	}

	return []error{ErrGroupResolution, e.Cause}
}

// ConfigurationError reports a wiring mistake found at construction time,
// before any traffic is served.
type ConfigurationError struct {
	// Detail says what is wrong and, where possible, how to fix it.
	Detail string
}

// Error implements the error interface.
func (e *ConfigurationError) Error() string {
	return "hmntsk: invalid configuration: " + e.Detail
}

// Unwrap makes the error match [ErrConfiguration].
func (e *ConfigurationError) Unwrap() error { return ErrConfiguration }
