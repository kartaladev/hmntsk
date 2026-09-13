package transporttest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// runReadAuthorizationCases covers who may read one task and its history.
func runReadAuthorizationCases(t *testing.T, mount Mount) {
	t.Helper()

	t.Run("by default an actor reads only tasks they take part in", func(t *testing.T) { readDefaultPolicy(t, mount) })
	t.Run("a host policy replaces the default", func(t *testing.T) { readHostPolicy(t, mount) })
	t.Run("a directory that cannot answer is a server error, never a refusal", func(t *testing.T) {
		readDirectoryFailure(t, mount)
	})
}

// readDefaultPolicy covers ParticipantsOnly: the holder, the creator and an
// eligible candidate may read, and nobody else.
func readDefaultPolicy(t *testing.T, mount Mount) {
	t.Helper()

	type testCase struct {
		name   string
		path   string
		actor  string
		assert func(t *testing.T, result Result)
	}

	client, _ := NewClient(t, mount)

	// pooled is offered to finance-approvers, alice and bob, and held by nobody.
	pooled := "/tasks/" + client.CreateApproval(t).ID.String()
	// held is claimed by alice.
	held := "/tasks/" + client.CreateApproval(t).ID.String()
	// excluding is offered to finance-approvers with bob excluded.
	excluding := "/tasks/" + client.CreateApproval(t, func(body map[string]any) {
		body["candidates"] = map[string]any{
			"groups": []string{"finance-approvers"}, "excluded": []string{Bob},
		}
	}).ID.String()

	require.Equal(t, transportcore.StatusOK, client.Do(t, http.MethodPost, held+"/claim", Alice, nil).Status)

	cases := []testCase{
		{name: "the holder may read the task", path: held, actor: Alice, assert: readable},
		{name: "an eligible group member may read a pooled task", path: pooled, actor: Bob, assert: readable},
		{name: "the creator may read the task", path: pooled, actor: Owner, assert: readable},
		{name: "the holder may read the history", path: held + "/history", actor: Alice, assert: readable},
		{name: "an outsider is refused the task", path: pooled, actor: Carol, assert: refusedRead},
		{name: "an outsider is refused the history", path: pooled + "/history", actor: Carol, assert: refusedRead},
		{name: "an excluded member is refused", path: excluding, actor: Bob, assert: refusedRead},
		{name: "an anonymous read of an existing task is refused", path: pooled, actor: "", assert: refusedRead},
		{
			name: "an anonymous read of an unknown task is refused, not answered 404", path: "/tasks/no-such-task",
			actor: "", assert: refusedRead,
		},
		{
			name: "an unknown task with an acting user is not found", path: "/tasks/no-such-task", actor: Alice,
			assert: func(t *testing.T, result Result) {
				assert.Equalf(t, transportcore.StatusNotFound, result.Status, "%s", result.Body)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.assert(t, client.Do(t, http.MethodGet, tc.path, tc.actor, nil))
		})
	}
}

// readHostPolicy covers replacing the default: a policy the host supplies
// decides every single-task and history read alone.
func readHostPolicy(t *testing.T, mount Mount) {
	t.Helper()

	type testCase struct {
		name   string
		policy transportcore.TaskReadAuthorizer
		// prepare creates what the case reads and returns its path.
		prepare func(t *testing.T, client *Client) string
		actor   string
		assert  func(t *testing.T, result Result)
	}

	// auditors is the policy a host writes to let an auditor read any task. It
	// composes the default rather than restating it.
	auditors := transportcore.TaskReadAuthorizerFunc(func(ctx context.Context, read transportcore.TaskRead) error {
		if read.Actor == "auditor" {
			return nil
		}

		return transportcore.ParticipantsOnly.AuthorizeRead(ctx, read)
	})

	archived := transportcore.TaskReadAuthorizerFunc(func(ctx context.Context, read transportcore.TaskRead) error {
		if read.Task.Status.IsTerminal() {
			return errors.New("closed tasks are archived")
		}

		return transportcore.ParticipantsOnly.AuthorizeRead(ctx, read)
	})

	// invalid refuses with an error that is also a validation error, which must
	// still be a refusal rather than a bad request.
	invalid := transportcore.TaskReadAuthorizerFunc(func(context.Context, transportcore.TaskRead) error {
		return &hmntsk.ValidationError{Subject: "request", Issues: []hmntsk.ValidationIssue{{
			Detail: "this task is not open to you",
		}}}
	})

	pooled := func(t *testing.T, client *Client) string {
		return "/tasks/" + client.CreateApproval(t).ID.String()
	}

	completed := func(t *testing.T, client *Client) string {
		path := pooled(t, client)

		for _, step := range []string{"claim", "start"} {
			require.Equal(t, transportcore.StatusOK, client.Do(t, http.MethodPost, path+"/"+step, Alice, nil).Status)
		}

		done := client.Do(t, http.MethodPost, path+"/complete", Alice,
			map[string]any{"output": json.RawMessage(`{"approved":true}`)})
		require.Equalf(t, transportcore.StatusOK, done.Status, "%s", done.Body)

		return path
	}

	cases := []testCase{
		{name: "an auditor policy lets an auditor read any task", policy: auditors, prepare: pooled, actor: "auditor", assert: readable},
		{name: "the same policy still keeps carol out", policy: auditors, prepare: pooled, actor: Carol, assert: refusedRead},
		{
			name: "a policy refusing closed tasks refuses even their holder", policy: archived,
			prepare: completed, actor: Alice,
			assert: func(t *testing.T, result Result) {
				refusedRead(t, result)
				assert.Contains(t, result.Error(t).Message, "archived", "the refusal carries the policy's own reason")
			},
		},
		{
			name: "a refusal is 403 whatever kind of error the policy returns", policy: invalid,
			prepare: pooled, actor: Alice, assert: refusedRead,
		},
		{
			name: "AllowAll restores unrestricted reads", policy: transportcore.AllowAll,
			prepare: pooled, actor: Carol, assert: readable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewClient(t, mount, transportcore.WithTaskReadAuthorizer(tc.policy))

			tc.assert(t, client.Do(t, http.MethodGet, tc.prepare(t, client), tc.actor, nil))
		})
	}
}

// brokenDirectory lists group members, so tasks can be created, but cannot say
// which groups an actor belongs to.
type brokenDirectory struct {
	*hmntsk.StaticAssignment
}

// GroupsOf implements [hmntsk.GroupResolver] by failing.
func (brokenDirectory) GroupsOf(context.Context, string) ([]string, error) {
	return nil, errors.New("directory unreachable")
}

// readDirectoryFailure covers a read the default policy cannot decide, because
// the directory it asks is down.
func readDirectoryFailure(t *testing.T, mount Mount) {
	t.Helper()

	client := clientFor(t, mount, newAPI(t, brokenDirectory{suiteDirectory()}))
	id := client.CreateApproval(t).ID.String()

	result := client.Do(t, http.MethodGet, "/tasks/"+id, Carol, nil)

	require.Equalf(t, transportcore.StatusInternalServerError, result.Status, "%s", result.Body)
	assert.Equal(t, transportcore.CodeInternal, result.Error(t).Code)
	assert.NotContains(t, string(result.Body), `"candidates"`, "an undecided read returns nothing of the task")
}

// readable asserts a permitted read.
func readable(t *testing.T, result Result) {
	t.Helper()

	require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)
}

// refusedRead asserts a refused single-task or history read: a refusal in the
// contract's own error shape, and nothing of the task.
func refusedRead(t *testing.T, result Result) {
	t.Helper()

	forbidden(t, result)
	assert.NotContains(t, string(result.Body), `"candidates"`, "a refused read returns nothing of the task")
	assert.NotContains(t, string(result.Body), `"records"`, "a refused history read returns no records")
}
