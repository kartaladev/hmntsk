package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kartaladev/hmntsk/notify"
)

func TestSubscriptionAuthorizers(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		authorizer notify.SubscriptionAuthorizer
		actor      string
		recipient  string
		assert     func(t *testing.T, err error)
	}

	permitted := func(t *testing.T, err error) {
		t.Helper()
		assert.NoError(t, err)
	}

	refused := func(t *testing.T, err error) {
		t.Helper()
		assert.ErrorIs(t, err, notify.ErrUnauthorized)
	}

	supervisors := notify.SubscriptionAuthorizerFunc(func(_ context.Context, actor, recipient string) error {
		if actor == "alice" && recipient == "bob" {
			return nil
		}

		return notify.SelfOnly.AuthorizeSubscription(context.Background(), actor, recipient)
	})

	cases := []testCase{
		{name: "self only permits a user following their own notifications", authorizer: notify.SelfOnly, actor: "alice", recipient: "alice", assert: permitted},
		{name: "self only refuses following someone else", authorizer: notify.SelfOnly, actor: "alice", recipient: "bob", assert: refused},
		{name: "self only refuses when no acting user is established", authorizer: notify.SelfOnly, actor: "", recipient: "", assert: refused},
		{name: "self only refuses an actor with no recipient", authorizer: notify.SelfOnly, actor: "alice", recipient: "", assert: refused},
		{name: "allow all permits following someone else", authorizer: notify.AllowAll, actor: "alice", recipient: "bob", assert: permitted},
		{name: "allow all permits even with no acting user", authorizer: notify.AllowAll, actor: "", recipient: "bob", assert: permitted},
		{name: "a host policy can permit a supervisor", authorizer: supervisors, actor: "alice", recipient: "bob", assert: permitted},
		{name: "a host policy extending self only still refuses others", authorizer: supervisors, actor: "bob", recipient: "alice", assert: refused},
		{
			name: "a policy function receives the actor and recipient",
			authorizer: notify.SubscriptionAuthorizerFunc(func(_ context.Context, actor, recipient string) error {
				if actor != "carol" || recipient != "dave" {
					return errors.New("wrong arguments")
				}

				return nil
			}),
			actor: "carol", recipient: "dave", assert: permitted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.authorizer.AuthorizeSubscription(t.Context(), tc.actor, tc.recipient))
		})
	}
}
