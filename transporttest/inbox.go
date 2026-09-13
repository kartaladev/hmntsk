package transporttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// runInboxCases covers querying, paging, ordering, counting and who may do
// each.
func runInboxCases(t *testing.T, mount Mount) {
	t.Helper()

	t.Run("an inbox returns mixed types in one response", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		client.CreateApproval(t)

		note := client.Do(t, http.MethodPost, "/tasks", Owner, map[string]any{
			"type":       "note",
			"input":      json.RawMessage(`{"body":"anything at all","seq":9007199254740993}`),
			"candidates": map[string]any{"groups": []string{"finance-approvers"}},
		})
		require.Equalf(t, transportcore.StatusCreated, note.Status, "%s", note.Body)

		result := client.Do(t, http.MethodGet, "/tasks?candidate="+Alice, Alice, nil)
		require.Equal(t, transportcore.StatusOK, result.Status)

		var page transportcore.PageResponse

		result.Decode(t, &page)
		require.Len(t, page.Tasks, 2)

		byType := make(map[string]hmntsk.Task, 2)
		for _, task := range page.Tasks {
			byType[task.Type] = task
		}

		require.Contains(t, byType, "approval")
		require.Contains(t, byType, "note")
		assert.Contains(t, string(byType["note"].Input), "9007199254740993",
			"payloads must survive a query as well as a read")
	})

	t.Run("an inbox distinguishes held work from claimable work", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		held := client.CreateApproval(t)
		client.CreateApproval(t)

		require.Equal(t, transportcore.StatusOK,
			client.Do(t, http.MethodPost, "/tasks/"+held.ID.String()+"/claim", Alice, nil).Status)

		result := client.Do(t, http.MethodGet, "/tasks?candidate="+Alice, Alice, nil)

		var page transportcore.PageResponse

		result.Decode(t, &page)
		require.Len(t, page.Tasks, 2)

		assigned, claimable := 0, 0

		for _, task := range page.Tasks {
			if task.Assignee == Alice {
				assigned++

				continue
			}

			claimable++
		}

		assert.Equal(t, 1, assigned)
		assert.Equal(t, 1, claimable)
	})

	t.Run("an excluded actor does not see the task", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		client.CreateApproval(t, func(body map[string]any) {
			body["candidates"] = map[string]any{
				"groups":   []string{"finance-approvers"},
				"excluded": []string{Bob},
			}
		})

		result := client.Do(t, http.MethodGet, "/tasks?candidate="+Bob, Bob, nil)

		var page transportcore.PageResponse

		result.Decode(t, &page)
		assert.Empty(t, page.Tasks)
	})

	t.Run("filters narrow by status, type and correlation", func(t *testing.T) {
		// The filters are under test here, not who may use them, so the
		// contract is served without the self-only default.
		client, _ := NewClient(t, mount, transportcore.WithQueryAuthorizer(transportcore.AllowAll))

		reserved := client.CreateApproval(t)
		client.CreateApproval(t, func(body map[string]any) {
			body["correlation"] = map[string]any{"ownerType": "process", "ownerRef": "p-2"}
		})

		require.Equal(t, transportcore.StatusOK,
			client.Do(t, http.MethodPost, "/tasks/"+reserved.ID.String()+"/claim", Alice, nil).Status)

		for query, want := range map[string]int{
			"?status=RESERVED":              1,
			"?status=READY":                 1,
			"?status=READY&status=RESERVED": 2,
			"?type=approval":                2,
			"?type=note":                    0,
			"?ownerRef=p-1":                 1,
			"?ownerRef=p-2":                 1,
			"?ownerRef=p-3":                 0,
			"?assignee=" + Alice:            1,
			"?assignee=" + Bob:              0,
		} {
			result := client.Do(t, http.MethodGet, "/tasks"+query, Alice, nil)
			require.Equalf(t, transportcore.StatusOK, result.Status, "%s", query)

			var page transportcore.PageResponse

			result.Decode(t, &page)
			assert.Lenf(t, page.Tasks, want, "query %s", query)
		}
	})

	t.Run("paging is stable while tasks are being created", func(t *testing.T) {
		// Paging over every task, not one actor's inbox, so the contract is
		// served without the self-only default.
		client, _ := NewClient(t, mount, transportcore.WithQueryAuthorizer(transportcore.AllowAll))

		const seeded = 7

		for range seeded {
			client.CreateApproval(t)
		}

		seen := make(map[hmntsk.TaskID]int)
		cursor := ""

		for range 20 {
			path := "/tasks?limit=3"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}

			result := client.Do(t, http.MethodGet, path, Alice, nil)
			require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)

			var page transportcore.PageResponse

			result.Decode(t, &page)

			for _, task := range page.Tasks {
				seen[task.ID]++
			}

			// Somebody else is creating work while this client pages.
			client.CreateApproval(t)

			if page.NextCursor == "" {
				break
			}

			cursor = page.NextCursor
		}

		for id, count := range seen {
			assert.Equalf(t, 1, count, "task %s came back on more than one page", id)
		}

		assert.GreaterOrEqual(t, len(seen), seeded,
			"every task that existed when paging began must be returned")
	})

	t.Run("newest-first paging works too", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		var ids []hmntsk.TaskID

		for range 4 {
			ids = append(ids, client.CreateApproval(t).ID)
		}

		result := client.Do(t, http.MethodGet, "/tasks?candidate="+Alice+"&limit=2&direction=desc", Alice, nil)
		require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)

		var page transportcore.PageResponse

		result.Decode(t, &page)
		require.Len(t, page.Tasks, 2)
		assert.Equal(t, ids[len(ids)-1], page.Tasks[0].ID)
		require.NotEmpty(t, page.NextCursor)

		next := client.Do(t, http.MethodGet,
			fmt.Sprintf("/tasks?candidate=%s&limit=2&direction=desc&cursor=%s", Alice, url.QueryEscape(page.NextCursor)),
			Alice, nil)

		var second transportcore.PageResponse

		next.Decode(t, &second)
		require.Len(t, second.Tasks, 2)
		assert.NotEqual(t, page.Tasks[0].ID, second.Tasks[0].ID)
	})

	t.Run("an empty inbox is an empty list, not null", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		result := client.Do(t, http.MethodGet, "/tasks?candidate="+Carol, Carol, nil)
		require.Equal(t, transportcore.StatusOK, result.Status)
		assert.Contains(t, string(result.Body), `"tasks":[]`,
			"a client iterating the response must not have to test for null first")
	})

	t.Run("an inbox can be ordered by urgency", func(t *testing.T) { inboxUrgency(t, mount) })
	t.Run("by default an actor reads only their own inbox", func(t *testing.T) { inboxDefaultPolicy(t, mount) })
	t.Run("a host policy replaces the default", func(t *testing.T) { inboxHostPolicy(t, mount) })
}

// inboxUrgency pages an inbox in urgency order: priority, then due date with
// tasks that have no deadline last, then creation.
func inboxUrgency(t *testing.T, mount Mount) {
	t.Helper()

	client, _ := NewClient(t, mount)

	at := func(hour int) string {
		return time.Date(2030, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}

	create := func(taskType string, priority int, due string) hmntsk.TaskID {
		t.Helper()

		return client.CreateApproval(t, func(body map[string]any) {
			body["type"] = taskType
			body["priority"] = priority

			if due != "" {
				body["dueAt"] = due
			}
		}).ID
	}

	// Notes have no default deadline, so the one created without a due date
	// really has none.
	lenient := create("approval", 5, at(3))
	undated := create("note", 1, "")
	pressing := create("approval", 1, at(2))
	critical := create("approval", 0, at(5))
	imminent := create("approval", 5, at(1))

	var got []hmntsk.TaskID

	cursor := ""

	for range 10 {
		path := "/tasks?candidate=" + Alice + "&orderBy=urgency&direction=asc&limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}

		result := client.Do(t, http.MethodGet, path, Alice, nil)
		require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)

		var page transportcore.PageResponse

		result.Decode(t, &page)

		for _, task := range page.Tasks {
			got = append(got, task.ID)
		}

		if page.NextCursor == "" {
			break
		}

		cursor = page.NextCursor
	}

	assert.Equal(t, []hmntsk.TaskID{critical, pressing, undated, imminent, lenient}, got,
		"most urgent first, in pages of two with no repeats or gaps")
}

// inboxDefaultPolicy covers the self-only default: an actor may query and count
// their own inbox, named directly or as me, and nothing else.
func inboxDefaultPolicy(t *testing.T, mount Mount) {
	t.Helper()

	type testCase struct {
		name   string
		path   string
		actor  string
		assert func(t *testing.T, result Result)
	}

	client, _ := NewClient(t, mount)
	client.CreateApproval(t)

	tasks := func(expected int) func(t *testing.T, result Result) {
		return func(t *testing.T, result Result) {
			require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)
			assert.Len(t, pageIDs(t, result), expected)
		}
	}

	badRequest := func(t *testing.T, result Result) {
		assert.Equalf(t, transportcore.StatusBadRequest, result.Status, "%s", result.Body)
	}

	cases := []testCase{
		{
			name: "candidate me is the acting user's inbox",
			path: "/tasks?candidate=me", actor: Alice, assert: tasks(1),
		},
		{
			name: "naming yourself is your own inbox too",
			path: "/tasks?candidate=" + Alice, actor: Alice, assert: tasks(1),
		},
		{
			name: "assignee me is the acting user's held work",
			path: "/tasks?assignee=me", actor: Alice, assert: tasks(0),
		},
		{
			name: "another actor's inbox is refused",
			path: "/tasks?candidate=" + Bob, actor: Alice, assert: forbidden,
		},
		{
			name: "a group's queue is refused",
			path: "/tasks?group=finance-approvers", actor: Alice, assert: forbidden,
		},
		{
			name: "a group is refused even beside your own inbox",
			path: "/tasks?candidate=me&group=finance-approvers", actor: Alice, assert: forbidden,
		},
		{
			name: "a query naming neither a candidate nor an assignee is refused",
			path: "/tasks?status=READY", actor: Alice, assert: forbidden,
		},
		{
			name: "me with no acting user is refused",
			path: "/tasks?assignee=me", actor: "", assert: forbidden,
		},
		{
			name: "your own count is what your own query matches",
			path: "/tasks/count?candidate=me", actor: Alice,
			assert: func(t *testing.T, result Result) {
				assert.Equal(t, int64(1), countOf(t, result))
			},
		},
		{
			name: "a count is refused like a query",
			path: "/tasks/count?candidate=" + Bob, actor: Alice, assert: forbidden,
		},
		{
			name: "a count with no acting user is refused",
			path: "/tasks/count?candidate=me", actor: "", assert: forbidden,
		},
		{
			name: "an unsupported ordering is a bad request",
			path: "/tasks?candidate=me&orderBy=bogus", actor: Alice, assert: badRequest,
		},
		{
			name: "an unsupported direction is a bad request",
			path: "/tasks?candidate=me&direction=sideways", actor: Alice, assert: badRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.assert(t, client.Do(t, http.MethodGet, tc.path, tc.actor, nil))
		})
	}
}

// inboxHostPolicy covers replacing the default: a policy the host supplies
// decides every query and count alone.
func inboxHostPolicy(t *testing.T, mount Mount) {
	t.Helper()

	type testCase struct {
		name   string
		policy transportcore.QueryAuthorizer
		path   string
		actor  string
		assert func(t *testing.T, result Result, finance, managers hmntsk.TaskID)
	}

	// supervisors is the policy a host writes to let a supervisor see a team
	// queue. It composes the default rather than restating it.
	supervisors := transportcore.QueryAuthorizerFunc(
		func(ctx context.Context, actor string, query hmntsk.Query) error {
			if actor == Carol && query.Group == "finance-approvers" {
				return nil
			}

			return transportcore.SelfOnly.AuthorizeQuery(ctx, actor, query)
		})

	closed := transportcore.QueryAuthorizerFunc(func(context.Context, string, hmntsk.Query) error {
		return errors.New("inboxes are closed at the weekend")
	})

	// invalid refuses with an error that is also a validation error, which must
	// still be a refusal rather than a bad request.
	invalid := transportcore.QueryAuthorizerFunc(func(context.Context, string, hmntsk.Query) error {
		return &hmntsk.ValidationError{Subject: "request", Issues: []hmntsk.ValidationIssue{{
			Detail: "this inbox is not open to you",
		}}}
	})

	cases := []testCase{
		{
			name: "a refusal is 403 whatever kind of error the policy returns", policy: invalid,
			path: "/tasks?candidate=me", actor: Alice,
			assert: func(t *testing.T, result Result, _, _ hmntsk.TaskID) { forbidden(t, result) },
		},
		{
			name: "a supervisor policy lets carol see a team queue", policy: supervisors,
			path: "/tasks?group=finance-approvers", actor: Carol,
			assert: func(t *testing.T, result Result, finance, _ hmntsk.TaskID) {
				require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)
				assert.Equal(t, []hmntsk.TaskID{finance}, pageIDs(t, result),
					"the group's queue, and only the group's")
			},
		},
		{
			name: "the same policy counts the team queue", policy: supervisors,
			path: "/tasks/count?group=finance-approvers", actor: Carol,
			assert: func(t *testing.T, result Result, _, _ hmntsk.TaskID) {
				assert.Equal(t, int64(1), countOf(t, result))
			},
		},
		{
			name: "the same policy still keeps carol out of alice's inbox", policy: supervisors,
			path: "/tasks?candidate=" + Alice, actor: Carol,
			assert: func(t *testing.T, result Result, _, _ hmntsk.TaskID) { forbidden(t, result) },
		},
		{
			name: "a policy refusing everything refuses an actor's own inbox", policy: closed,
			path: "/tasks?candidate=me", actor: Alice,
			assert: func(t *testing.T, result Result, _, _ hmntsk.TaskID) {
				forbidden(t, result)
				assert.Contains(t, result.Error(t).Message, "closed at the weekend",
					"the refusal carries the policy's own reason")
			},
		},
		{
			// By type rather than status: managers has one member, so its task
			// is reserved for carol the moment it is created.
			name: "AllowAll restores unrestricted queries", policy: transportcore.AllowAll,
			path: "/tasks?type=approval", actor: Alice,
			assert: func(t *testing.T, result Result, finance, managers hmntsk.TaskID) {
				require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)
				assert.ElementsMatch(t, []hmntsk.TaskID{finance, managers}, pageIDs(t, result))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewClient(t, mount, transportcore.WithQueryAuthorizer(tc.policy))

			finance := client.CreateApproval(t).ID
			managers := client.CreateApproval(t, func(body map[string]any) {
				body["candidates"] = map[string]any{"groups": []string{"managers"}}
			}).ID

			tc.assert(t, client.Do(t, http.MethodGet, tc.path, tc.actor, nil), finance, managers)
		})
	}
}

// forbidden asserts a refusal: 403 in the contract's own error shape, and no
// tasks.
func forbidden(t *testing.T, result Result) {
	t.Helper()

	require.Equalf(t, transportcore.StatusForbidden, result.Status, "%s", result.Body)
	assert.Equal(t, transportcore.CodeForbidden, result.Error(t).Code)
	assert.NotContains(t, string(result.Body), `"tasks"`, "a refused query returns no tasks")
}

// pageIDs decodes a page and lists its task identifiers, in order.
func pageIDs(t *testing.T, result Result) []hmntsk.TaskID {
	t.Helper()

	var page transportcore.PageResponse

	result.Decode(t, &page)

	ids := make([]hmntsk.TaskID, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		ids = append(ids, task.ID)
	}

	return ids
}

// countOf decodes a count response, requiring a 200 with a count field.
func countOf(t *testing.T, result Result) int64 {
	t.Helper()

	require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)

	var body struct {
		Count *int64 `json:"count"`
	}

	result.Decode(t, &body)
	require.NotNilf(t, body.Count, "a count answers {\"count\": n}: %s", result.Body)

	return *body.Count
}
