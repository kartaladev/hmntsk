package transporttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// runInboxCases covers querying and paging.
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
		client, _ := NewClient(t, mount)

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
		client, _ := NewClient(t, mount)

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

		result := client.Do(t, http.MethodGet, "/tasks?limit=2&order=desc", Alice, nil)
		require.Equal(t, transportcore.StatusOK, result.Status)

		var page transportcore.PageResponse

		result.Decode(t, &page)
		require.Len(t, page.Tasks, 2)
		assert.Equal(t, ids[len(ids)-1], page.Tasks[0].ID)
		require.NotEmpty(t, page.NextCursor)

		next := client.Do(t, http.MethodGet,
			fmt.Sprintf("/tasks?limit=2&order=desc&cursor=%s", url.QueryEscape(page.NextCursor)),
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
}
