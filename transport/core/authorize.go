package transportcore

import (
	"context"
	"errors"
	"fmt"

	"github.com/kartaladev/hmntsk"
)

// Me is the value of the candidate and assignee query parameters that names
// the acting user. It is resolved to the actor the host established before
// query authorization runs, and a query naming it with no actor established is
// refused.
const Me = "me"

// QueryAuthorizer decides whether an actor may run an inbox query or count over
// HTTP.
//
// It runs for GET /tasks and GET /tasks/count, after the request is parsed and
// [Me] is resolved, and before the engine is asked anything. Reading one task
// and the lifecycle operations are not queries: they keep the engine's own
// eligibility rules and do not pass through it.
//
// The default is [SelfOnly]. [WithQueryAuthorizer] replaces it wholesale:
// nothing is wrapped or chained, so a policy that extends the default calls
// SelfOnly itself.
type QueryAuthorizer interface {
	// AuthorizeQuery returns nil to permit the query and an error to refuse
	// it. Any error refuses: the contract answers 403 with the error's
	// message, so that message must be safe to show the caller.
	AuthorizeQuery(ctx context.Context, actor string, query hmntsk.Query) error
}

// QueryAuthorizerFunc adapts a function to the [QueryAuthorizer] interface.
type QueryAuthorizerFunc func(ctx context.Context, actor string, query hmntsk.Query) error

// AuthorizeQuery implements [QueryAuthorizer].
func (f QueryAuthorizerFunc) AuthorizeQuery(ctx context.Context, actor string, query hmntsk.Query) error {
	return f(ctx, actor, query)
}

// SelfOnly is the default query authorization policy: an actor may query only
// their own inbox.
//
// It permits a query that names a candidate or an assignee when every one it
// names is the acting user. It refuses a query by group, a query naming neither
// (every task is nobody's own inbox), and any query at all when no acting user
// is established. Its refusals match [hmntsk.ErrUnauthorized].
var SelfOnly QueryAuthorizer = QueryAuthorizerFunc(selfOnly)

// AllowAllPolicy is the type of [AllowAll]: a policy that permits every query,
// count and single-task read.
type AllowAllPolicy struct{}

// Compile-time proof that the one opt-out satisfies both policies.
var (
	_ QueryAuthorizer    = AllowAllPolicy{}
	_ TaskReadAuthorizer = AllowAllPolicy{}
)

// AuthorizeQuery implements [QueryAuthorizer] by permitting the query.
func (AllowAllPolicy) AuthorizeQuery(context.Context, string, hmntsk.Query) error { return nil }

// AuthorizeRead implements [TaskReadAuthorizer] by permitting the read.
func (AllowAllPolicy) AuthorizeRead(context.Context, TaskRead) error { return nil }

// AllowAll permits every query, count and single-task read. It is the explicit
// opt-out from [SelfOnly] and [ParticipantsOnly], for a host that authorizes
// somewhere else, such as in middleware in front of the contract, and it is
// named so that serving every inbox and every task to every caller is never an
// accident. Each policy is still replaced only by passing it to its own option.
var AllowAll = AllowAllPolicy{}

// TaskRead describes one read of a single task, for a [TaskReadAuthorizer] to
// decide.
//
// It carries the engine's eligibility check rather than its answer, so a policy
// that decides on the actor or the task alone never costs a directory call.
type TaskRead struct {
	// Actor is who is reading, as the host's middleware established it. It is
	// never empty when the contract asks a policy: a read with no acting user is
	// refused before the task is looked up.
	Actor string
	// Task is the task being read, as it stands.
	Task hmntsk.Task

	eligible func(ctx context.Context) (bool, error)
}

// NewTaskRead describes a read of task by actor, with eligible as the check
// [TaskRead.Eligible] runs. The contract builds every read it authorizes with
// the engine's own check, [hmntsk.Service.Eligible]; NewTaskRead exists so a
// host can test a policy of its own against reads it describes itself.
//
// A nil eligible makes the actor ineligible.
func NewTaskRead(actor string, task hmntsk.Task, eligible func(ctx context.Context) (bool, error)) TaskRead {
	return TaskRead{Actor: actor, Task: task, eligible: eligible}
}

// Eligible reports whether the actor is eligible for the task by the engine's
// own rule, the one a claim applies: exclusion first, then candidate users,
// then group membership. Membership is resolved only when this is called.
//
// A directory that cannot answer is an error matching
// [hmntsk.ErrGroupResolution]. A policy that returns it unchanged has the read
// answered 500, because the engine could not decide rather than deciding
// against the caller.
func (r TaskRead) Eligible(ctx context.Context) (bool, error) {
	if r.eligible == nil {
		return false, nil
	}

	return r.eligible(ctx)
}

// TaskReadAuthorizer decides whether an actor may read one task over HTTP.
//
// It runs for GET /tasks/{id} and GET /tasks/{id}/history, after a read with no
// acting user has been refused and after the task has been found, and before
// anything about the task is returned. Lifecycle operations do not pass through
// it: they keep the engine's own assignee and eligibility rules.
//
// The default is [ParticipantsOnly]. [WithTaskReadAuthorizer] replaces it
// wholesale: nothing is wrapped or chained, so a policy that extends the
// default calls ParticipantsOnly itself.
type TaskReadAuthorizer interface {
	// AuthorizeRead returns nil to permit the read and an error to refuse it.
	// Any error refuses with 403 and the error's message, so that message must
	// be safe to show the caller, except an error matching
	// [hmntsk.ErrGroupResolution], which answers 500.
	AuthorizeRead(ctx context.Context, read TaskRead) error
}

// TaskReadAuthorizerFunc adapts a function to the [TaskReadAuthorizer]
// interface.
type TaskReadAuthorizerFunc func(ctx context.Context, read TaskRead) error

// AuthorizeRead implements [TaskReadAuthorizer].
func (f TaskReadAuthorizerFunc) AuthorizeRead(ctx context.Context, read TaskRead) error {
	return f(ctx, read)
}

// ParticipantsOnly is the default read authorization policy: an actor may read
// a task they hold, a task they created, or a task they are eligible for.
//
// It checks the holder and the creator first, so neither costs a directory
// call, and resolves eligibility only for anyone else. It refuses every other
// actor, and a read with no acting user. Its refusals match
// [hmntsk.ErrUnauthorized]; a directory failure is returned as itself.
var ParticipantsOnly TaskReadAuthorizer = TaskReadAuthorizerFunc(participantsOnly)

// WithTaskReadAuthorizer replaces the default read authorization policy,
// [ParticipantsOnly], with the host's own, which then decides every single-task
// and history read alone. A nil policy is a configuration error from [New];
// pass [AllowAll] to permit every read.
func WithTaskReadAuthorizer(authorizer TaskReadAuthorizer) Option {
	return func(a *API) { a.readAuthorizer = authorizer }
}

// participantsOnly is the rule [ParticipantsOnly] applies.
func participantsOnly(ctx context.Context, read TaskRead) error {
	switch read.Actor {
	case "":
		return refusal("no acting user is established")
	case read.Task.Assignee, read.Task.CreatedBy:
		return nil
	}

	eligible, err := read.Eligible(ctx)
	if err != nil {
		return err
	}

	if !eligible {
		return refusal("an actor may read only tasks they hold, created or are eligible for")
	}

	return nil
}

// WithQueryAuthorizer replaces the default query authorization policy,
// [SelfOnly], with the host's own, which then decides every query and count
// alone. A nil policy is a configuration error from [New]; pass [AllowAll] to
// permit everything.
func WithQueryAuthorizer(authorizer QueryAuthorizer) Option {
	return func(a *API) { a.authorizer = authorizer }
}

// selfOnly is the rule [SelfOnly] applies.
func selfOnly(_ context.Context, actor string, query hmntsk.Query) error {
	switch {
	case actor == "":
		return refusal("no acting user is established")
	case query.Group != "":
		return refusal("a group's queue is not an actor's own inbox")
	case query.Candidate == "" && query.Assignee == "":
		return refusal("a query must name the acting user as its candidate or assignee")
	case query.Candidate != "" && query.Candidate != actor,
		query.Assignee != "" && query.Assignee != actor:
		return refusal("an actor may query only their own inbox")
	default:
		return nil
	}
}

// refusal reports a query [SelfOnly] refuses.
func refusal(reason string) error {
	return fmt.Errorf("%w: %s", hmntsk.ErrUnauthorized, reason)
}

// refuse turns the error a query or read policy returned into what the contract
// answers with: a refusal, unless the engine could not decide at all.
//
// An error matching [hmntsk.ErrGroupResolution] is returned unchanged, so it
// answers 500. The directory could not say whether the actor may proceed, which
// is not the same as the policy deciding they may not.
func refuse(err error) error {
	if errors.Is(err, hmntsk.ErrGroupResolution) {
		return err
	}

	return &policyRefusedError{cause: err}
}

// policyRefusedError carries a policy's refusal to the error mapping.
//
// Whatever the policy returned, the contract answers 403 with the policy's own
// message. A refusal is a refusal however the policy's error happens to be
// classified, so it unwraps to [hmntsk.ErrUnauthorized] alone: a policy error
// that also matched, say, a validation error must not turn a refusal into a
// 400.
type policyRefusedError struct{ cause error }

// Error implements the error interface.
func (e *policyRefusedError) Error() string { return e.cause.Error() }

// Unwrap makes the error match [hmntsk.ErrUnauthorized].
func (e *policyRefusedError) Unwrap() error { return hmntsk.ErrUnauthorized }
