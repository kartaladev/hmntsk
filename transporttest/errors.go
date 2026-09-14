package transporttest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// runErrorCases pins the mapping from the engine's refusals to status codes.
func runErrorCases(t *testing.T, mount Mount) {
	t.Helper()

	type testCase struct {
		name string
		// act performs the request, given a task the harness has set up.
		act func(t *testing.T, client *Client, id string) Result
		// prepare optionally advances the task first.
		prepare func(t *testing.T, client *Client, id string)
		assert  func(t *testing.T, result Result)
	}

	cases := []testCase{
		{
			name: "a stale version is a conflict that reports the current one",
			prepare: func(t *testing.T, client *Client, id string) {
				require.Equal(t, transportcore.StatusOK,
					client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, nil).Status)
			},
			act: func(t *testing.T, client *Client, id string) Result {
				return client.Do(t, http.MethodPost, "/tasks/"+id+"/start", Alice,
					map[string]any{"version": 1})
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusConflict, result.Status)

				detail := result.Error(t)
				assert.Equal(t, transportcore.CodeConflict, detail.Code)
				require.NotNil(t, detail.CurrentVersion,
					"the body must identify the version to re-read")
				assert.Equal(t, int64(2), *detail.CurrentVersion)
			},
		},
		{
			name: "an illegal transition is a conflict too",
			act: func(t *testing.T, client *Client, id string) Result {
				return client.Do(t, http.MethodPost, "/tasks/"+id+"/complete", Alice,
					map[string]any{"output": json.RawMessage(`{"approved":true}`)})
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusConflict, result.Status)
				assert.Equal(t, transportcore.CodeConflict, result.Error(t).Code)
			},
		},
		{
			name: "a non-assignee is forbidden",
			prepare: func(t *testing.T, client *Client, id string) {
				require.Equal(t, transportcore.StatusOK,
					client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, nil).Status)
				require.Equal(t, transportcore.StatusOK,
					client.Do(t, http.MethodPost, "/tasks/"+id+"/start", Alice, nil).Status)
			},
			act: func(t *testing.T, client *Client, id string) Result {
				return client.Do(t, http.MethodPost, "/tasks/"+id+"/complete", Bob,
					map[string]any{"output": json.RawMessage(`{"approved":true}`)})
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusForbidden, result.Status)
				assert.Equal(t, transportcore.CodeForbidden, result.Error(t).Code)
			},
		},
		{
			name: "an ineligible actor is forbidden",
			act: func(t *testing.T, client *Client, id string) Result {
				return client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Carol, nil)
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusForbidden, result.Status)
				assert.Equal(t, transportcore.CodeForbidden, result.Error(t).Code)
			},
		},
		{
			name: "an unknown task is not found",
			act: func(t *testing.T, client *Client, _ string) Result {
				return client.Do(t, http.MethodGet, "/tasks/no-such-task", Alice, nil)
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusNotFound, result.Status)
				assert.Equal(t, transportcore.CodeNotFound, result.Error(t).Code)
			},
		},
		{
			name: "an operation on an unknown task is not found",
			act: func(t *testing.T, client *Client, _ string) Result {
				return client.Do(t, http.MethodPost, "/tasks/no-such-task/claim", Alice, nil)
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusNotFound, result.Status)
			},
		},
		{
			name: "history for an unknown task is not found",
			act: func(t *testing.T, client *Client, _ string) Result {
				return client.Do(t, http.MethodGet, "/tasks/no-such-task/history", Alice, nil)
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusNotFound, result.Status)
			},
		},
		{
			name: "an unregistered type is a bad request naming the type",
			act: func(t *testing.T, client *Client, _ string) Result {
				return client.Do(t, http.MethodPost, "/tasks", Owner, map[string]any{
					"type":       "approvel",
					"candidates": map[string]any{"users": []string{Alice}},
				})
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusBadRequest, result.Status)

				detail := result.Error(t)
				assert.Equal(t, transportcore.CodeUnregisteredType, detail.Code)
				assert.Equal(t, "approvel", detail.TaskType,
					"the body must name the type the client got wrong")
			},
		},
		{
			name: "a payload that fails the schema is a bad request with the offending field",
			act: func(t *testing.T, client *Client, _ string) Result {
				return client.Do(t, http.MethodPost, "/tasks", Owner, map[string]any{
					"type":       "approval",
					"input":      json.RawMessage(`{"amount":"a lot","justification":"x"}`),
					"candidates": map[string]any{"users": []string{Alice}},
				})
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusBadRequest, result.Status)

				detail := result.Error(t)
				assert.Equal(t, transportcore.CodeValidation, detail.Code)
				require.NotEmpty(t, detail.Issues)

				pointers := make([]string, 0, len(detail.Issues))
				for _, issue := range detail.Issues {
					pointers = append(pointers, issue.Pointer)
				}

				assert.Contains(t, pointers, "/amount")
			},
		},
		{
			name: "an incomplete output is a bad request",
			prepare: func(t *testing.T, client *Client, id string) {
				require.Equal(t, transportcore.StatusOK,
					client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, nil).Status)
				require.Equal(t, transportcore.StatusOK,
					client.Do(t, http.MethodPost, "/tasks/"+id+"/start", Alice, nil).Status)
			},
			act: func(t *testing.T, client *Client, id string) Result {
				return client.Do(t, http.MethodPost, "/tasks/"+id+"/complete", Alice,
					map[string]any{"output": json.RawMessage(`{"note":"looks fine"}`)})
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusBadRequest, result.Status)
				assert.Equal(t, transportcore.CodeValidation, result.Error(t).Code)
			},
		},
		{
			name: "a body that is not JSON is a bad request",
			act: func(t *testing.T, client *Client, id string) Result {
				return client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, "{not json")
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusBadRequest, result.Status)
				assert.Equal(t, transportcore.CodeValidation, result.Error(t).Code)
			},
		},
		{
			name: "an unparseable query parameter is a bad request",
			act: func(t *testing.T, client *Client, _ string) Result {
				return client.Do(t, http.MethodGet, "/tasks?limit=lots", Alice, nil)
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusBadRequest, result.Status)
			},
		},
		{
			name: "an unknown status filter is a bad request",
			act: func(t *testing.T, client *Client, _ string) Result {
				return client.Do(t, http.MethodGet, "/tasks?status=ARCHIVED", Alice, nil)
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusBadRequest, result.Status)
			},
		},
		{
			name: "delegating to an ineligible actor is forbidden",
			prepare: func(t *testing.T, client *Client, id string) {
				require.Equal(t, transportcore.StatusOK,
					client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, nil).Status)
			},
			act: func(t *testing.T, client *Client, id string) Result {
				return client.Do(t, http.MethodPost, "/tasks/"+id+"/delegate", Alice,
					map[string]any{"delegate": Carol})
			},
			assert: func(t *testing.T, result Result) {
				require.Equal(t, transportcore.StatusForbidden, result.Status)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewClient(t, mount)

			task := client.CreateApproval(t)

			if tc.prepare != nil {
				tc.prepare(t, client, task.ID.String())
			}

			result := tc.act(t, client, task.ID.String())

			assert.Contains(t, result.ContentType, "application/json",
				"every answer, failures included, is JSON")

			tc.assert(t, result)
		})
	}

	t.Run("a route the contract does not serve is a JSON 404", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		result := client.DoAbsolute(t, http.MethodGet, "/nothing/here", Alice)
		require.Equal(t, transportcore.StatusNotFound, result.Status)
		assert.Contains(t, result.ContentType, "application/json",
			"a client should not have to tell 'no such route' from 'no such task' by content type")
		assert.Equal(t, transportcore.CodeNotFound, result.Error(t).Code)
	})

	t.Run("a method the contract does not serve on a contract path is a JSON 404 with no Allow", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)

		result := client.Do(t, http.MethodPatch, "/tasks/"+task.ID.String(), Alice, nil)
		require.Equalf(t, transportcore.StatusNotFound, result.Status, "%s", result.Body)
		assert.Contains(t, result.ContentType, "application/json")
		assert.Equal(t, transportcore.CodeNotFound, result.Error(t).Code)
		assert.Empty(t, result.Allow, "the contract answers 404 alike on every binding and never sends Allow")
	})

	t.Run("a refused operation changes nothing", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)
		id := task.ID.String()

		before := client.Do(t, http.MethodGet, "/tasks/"+id, Alice, nil).Task(t)

		refused := client.Do(t, http.MethodPost, "/tasks/"+id+"/complete", Carol,
			map[string]any{"output": json.RawMessage(`{"approved":true}`)})
		require.NotEqual(t, transportcore.StatusOK, refused.Status)

		after := client.Do(t, http.MethodGet, "/tasks/"+id, Alice, nil).Task(t)
		assert.Equal(t, before.Version, after.Version)
		assert.Equal(t, hmntsk.StatusReady, after.Status)
	})
}
