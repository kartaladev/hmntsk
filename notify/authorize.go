package notify

import (
	"context"
	"fmt"
)

// SubscriptionAuthorizer decides whether an acting user may follow a
// recipient's change signals.
//
// The default is [SelfOnly]. A host replaces it wholesale, for example with a
// policy that lets a supervisor follow a team member; nothing is chained, so a
// policy that extends the default calls SelfOnly itself.
type SubscriptionAuthorizer interface {
	// AuthorizeSubscription returns nil to permit the subscription and an
	// error to refuse it. Any error refuses, and is answered as forbidden with
	// its message, so that message must be safe to show the caller.
	AuthorizeSubscription(ctx context.Context, actor, recipient string) error
}

// SubscriptionAuthorizerFunc adapts a function to the [SubscriptionAuthorizer]
// interface.
type SubscriptionAuthorizerFunc func(ctx context.Context, actor, recipient string) error

// AuthorizeSubscription implements [SubscriptionAuthorizer].
func (f SubscriptionAuthorizerFunc) AuthorizeSubscription(ctx context.Context, actor, recipient string) error {
	return f(ctx, actor, recipient)
}

// SelfOnly is the default subscription policy: a user may follow only their
// own notifications. Every notification is addressed to one recipient, so this
// is everything a user's own client needs. It refuses any subscription when no
// acting user is established. Its refusals match [ErrUnauthorized].
var SelfOnly SubscriptionAuthorizer = SubscriptionAuthorizerFunc(selfOnly)

// AllowAll permits every subscription. It is the explicit opt-out from
// [SelfOnly], for a host that authorizes subscriptions somewhere else, and it is
// named so that streaming anyone's signals to anyone is never an accident.
var AllowAll SubscriptionAuthorizer = SubscriptionAuthorizerFunc(func(context.Context, string, string) error {
	return nil
})

// selfOnly is the rule [SelfOnly] applies.
func selfOnly(_ context.Context, actor, recipient string) error {
	switch {
	case actor == "":
		return fmt.Errorf("%w: no acting user is established", ErrUnauthorized)
	case recipient != actor:
		return fmt.Errorf("%w: a user may follow only their own notifications", ErrUnauthorized)
	default:
		return nil
	}
}
