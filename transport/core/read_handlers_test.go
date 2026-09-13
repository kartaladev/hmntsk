package transportcore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// TestSingleTaskReadsAreAuthorizedInOrder pins the order a single-task read and a
// history read check things in: no actor, then the lookup, then the policy.
func TestSingleTaskReadsAreAuthorizedInOrder(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name      string
		operation string
		actor     string
		unknown   bool
		policy    error
		assert    func(t *testing.T, response transportcore.Response, asked bool)
	}

	status := func(expected int, wantAsked bool) func(t *testing.T, response transportcore.Response, asked bool) {
		return func(t *testing.T, response transportcore.Response, asked bool) {
			assert.Equalf(t, expected, response.Status, "%s", response.Body)
			assert.Equal(t, wantAsked, asked, "whether the policy was asked")
		}
	}

	refusedWithNothing := func(t *testing.T, response transportcore.Response, asked bool) {
		require.Equalf(t, transportcore.StatusForbidden, response.Status, "%s", response.Body)
		assert.True(t, asked)
		assert.NotContains(t, string(response.Body), `"records"`, "a refused history read returns no records")
		assert.NotContains(t, string(response.Body), `"candidates"`, "a refused read returns nothing of the task")
	}

	cases := []testCase{
		{name: "read: no actor is refused before the task is looked up", operation: "getTask", unknown: true, assert: status(transportcore.StatusForbidden, false)},
		{name: "read: no actor is refused for an existing task too", operation: "getTask", assert: status(transportcore.StatusForbidden, false)},
		{name: "read: an unknown task with an actor is not found", operation: "getTask", actor: "alice", unknown: true, assert: status(transportcore.StatusNotFound, false)},
		{name: "read: a refused read is forbidden", operation: "getTask", actor: "alice", policy: errors.New("not yours"), assert: refusedWithNothing},
		{name: "read: a permitted read returns the task", operation: "getTask", actor: "alice", assert: status(transportcore.StatusOK, true)},
		{name: "history: no actor is refused before the task is looked up", operation: "getTaskHistory", unknown: true, assert: status(transportcore.StatusForbidden, false)},
		{name: "history: an unknown task with an actor is not found", operation: "getTaskHistory", actor: "alice", unknown: true, assert: status(transportcore.StatusNotFound, false)},
		{name: "history: a refused read returns no records", operation: "getTaskHistory", actor: "alice", policy: errors.New("not yours"), assert: refusedWithNothing},
		{name: "history: a permitted read returns the records", operation: "getTaskHistory", actor: "alice", assert: status(transportcore.StatusOK, true)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry := hmntsk.NewRegistry()
			require.NoError(t, registry.Register(hmntsk.TypeSpec{Name: "note", Title: "Note"}))

			svc, err := hmntsk.New(memstore.New(), hmntsk.WithRegistry(registry))
			require.NoError(t, err)

			created, err := svc.Create(t.Context(), hmntsk.CreateRequest{
				Type: "note", Actor: "owner", Input: json.RawMessage(`{}`),
				Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
			})
			require.NoError(t, err)

			asked := false
			policy := transportcore.TaskReadAuthorizerFunc(func(context.Context, transportcore.TaskRead) error {
				asked = true

				return tc.policy
			})

			api, err := transportcore.New(svc, transportcore.WithTaskReadAuthorizer(policy))
			require.NoError(t, err)

			id := string(created.Task.ID)
			if tc.unknown {
				id = "no-such-task"
			}

			response := routeFor(t, api, tc.operation).Handler(t.Context(), transportcore.Request{
				Method: "GET", Params: map[string]string{"id": id}, Actor: tc.actor,
			})

			tc.assert(t, response, asked)
		})
	}
}

// routeFor finds a route in the contract by its operation identifier.
func routeFor(t *testing.T, api *transportcore.API, operationID string) transportcore.Route {
	t.Helper()

	for _, route := range api.Routes() {
		if route.OperationID == operationID {
			return route
		}
	}

	require.Failf(t, "no such route", "operation %q", operationID)

	return transportcore.Route{}
}
