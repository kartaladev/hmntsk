package transportcore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

func TestSelfOnly(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		actor  string
		query  hmntsk.Query
		assert func(t *testing.T, err error)
	}

	permitted := func(t *testing.T, err error) { require.NoError(t, err) }

	refused := func(t *testing.T, err error) {
		require.ErrorIs(t, err, hmntsk.ErrUnauthorized,
			"a host calling the policy itself can tell a refusal from a failure")
	}

	cases := []testCase{
		{
			name: "an actor may query what they are a candidate for", actor: "alice",
			query: hmntsk.Query{Candidate: "alice"}, assert: permitted,
		},
		{
			name: "an actor may query what they hold", actor: "alice",
			query: hmntsk.Query{Assignee: "alice"}, assert: permitted,
		},
		{
			name: "filters and orderings do not change whose inbox it is", actor: "alice",
			query: hmntsk.Query{
				Candidate: "alice", Assignee: "alice", OwnerType: "invoice",
				Statuses: []hmntsk.Status{hmntsk.StatusReady}, OrderBy: hmntsk.OrderUrgency,
			},
			assert: permitted,
		},
		{
			name: "another actor's candidacy is refused", actor: "alice",
			query: hmntsk.Query{Candidate: "bob"}, assert: refused,
		},
		{
			name: "another actor's held work is refused", actor: "alice",
			query: hmntsk.Query{Assignee: "bob"}, assert: refused,
		},
		{
			name: "every named actor must be the acting one", actor: "alice",
			query: hmntsk.Query{Candidate: "alice", Assignee: "bob"}, assert: refused,
		},
		{
			name: "a group's queue is refused", actor: "alice",
			query: hmntsk.Query{Group: "finance-approvers"}, assert: refused,
		},
		{
			name: "a group is refused even beside the actor's own inbox", actor: "alice",
			query: hmntsk.Query{Candidate: "alice", Group: "finance-approvers"}, assert: refused,
		},
		{
			name: "a query naming nobody is refused, because every task is nobody's inbox", actor: "alice",
			query: hmntsk.Query{Statuses: []hmntsk.Status{hmntsk.StatusReady}}, assert: refused,
		},
		{
			name: "no acting user is refused whatever the query names", actor: "",
			query: hmntsk.Query{Candidate: "alice"}, assert: refused,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, transportcore.SelfOnly.AuthorizeQuery(t.Context(), tc.actor, tc.query))
		})
	}
}

func TestAllowAll(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		actor  string
		query  hmntsk.Query
		assert func(t *testing.T, err error)
	}

	permitted := func(t *testing.T, err error) { require.NoError(t, err) }

	cases := []testCase{
		{name: "another actor's inbox", actor: "alice", query: hmntsk.Query{Candidate: "bob"}, assert: permitted},
		{name: "a group's queue", actor: "alice", query: hmntsk.Query{Group: "finance-approvers"}, assert: permitted},
		{name: "a query naming nobody", actor: "alice", query: hmntsk.Query{}, assert: permitted},
		{name: "no acting user", actor: "", query: hmntsk.Query{Assignee: "bob"}, assert: permitted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, transportcore.AllowAll.AuthorizeQuery(t.Context(), tc.actor, tc.query))
		})
	}
}

func TestQueryAuthorizerFuncPassesItsArgumentsThrough(t *testing.T) {
	t.Parallel()

	refusal := errors.New("closed")

	var (
		gotActor string
		gotQuery hmntsk.Query
	)

	policy := transportcore.QueryAuthorizerFunc(func(_ context.Context, actor string, query hmntsk.Query) error {
		gotActor, gotQuery = actor, query

		return refusal
	})

	err := policy.AuthorizeQuery(t.Context(), "carol", hmntsk.Query{Group: "finance-approvers"})

	require.ErrorIs(t, err, refusal)
	assert.Equal(t, "carol", gotActor)
	assert.Equal(t, "finance-approvers", gotQuery.Group)
}

func TestNewRefusesANilQueryAuthorizer(t *testing.T) {
	t.Parallel()

	svc, err := hmntsk.New(memstore.New())
	require.NoError(t, err)

	api, err := transportcore.New(svc, transportcore.WithQueryAuthorizer(nil))

	require.ErrorIs(t, err, hmntsk.ErrConfiguration,
		"a missing policy is a wiring mistake, refused before any traffic rather than serving unauthorized")
	assert.Nil(t, api)
}
