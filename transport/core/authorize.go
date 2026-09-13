package transportcore

import (
	"context"
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

// AllowAll permits every query and count. It is the explicit opt-out from
// [SelfOnly], for a host that authorizes queries somewhere else, such as in
// middleware in front of the contract, and it is named so that serving every
// inbox to every caller is never an accident.
var AllowAll QueryAuthorizer = QueryAuthorizerFunc(func(context.Context, string, hmntsk.Query) error {
	return nil
})

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

// queryRefusedError carries a policy's refusal to the error mapping.
//
// Whatever the policy returned, the contract answers 403 with the policy's own
// message. A refusal is a refusal however the policy's error happens to be
// classified, so it unwraps to [hmntsk.ErrUnauthorized] alone: a policy error
// that also matched, say, a validation error must not turn a refusal into a
// 400.
type queryRefusedError struct{ cause error }

// Error implements the error interface.
func (e *queryRefusedError) Error() string { return e.cause.Error() }

// Unwrap makes the error match [hmntsk.ErrUnauthorized].
func (e *queryRefusedError) Unwrap() error { return hmntsk.ErrUnauthorized }
