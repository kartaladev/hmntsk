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

// runLifecycleCases exercises every operation the contract exposes.
func runLifecycleCases(t *testing.T, mount Mount) {
	t.Helper()

	t.Run("every operation is reachable and matches a direct invocation", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)
		require.Equal(t, hmntsk.StatusReady, task.Status)

		claimed := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/claim", Alice, nil)
		require.Equal(t, transportcore.StatusOK, claimed.Status)
		assert.Equal(t, hmntsk.StatusReserved, claimed.Task(t).Status)
		assert.Equal(t, Alice, claimed.Task(t).Assignee)

		released := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/release", Alice,
			map[string]any{"comment": "handing back"})
		require.Equal(t, transportcore.StatusOK, released.Status)
		assert.Equal(t, hmntsk.StatusReady, released.Task(t).Status)

		reclaimed := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/claim", Bob, nil)
		require.Equal(t, transportcore.StatusOK, reclaimed.Status)

		started := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/start", Bob, nil)
		require.Equal(t, transportcore.StatusOK, started.Status)
		assert.Equal(t, hmntsk.StatusInProgress, started.Task(t).Status)

		progress := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/progress", Bob,
			map[string]any{"patch": json.RawMessage(`[{"op":"add","path":"/amount","value":100}]`)})
		require.Equalf(t, transportcore.StatusOK, progress.Status, "%s", progress.Body)
		assert.JSONEq(t, `{"amount":100}`, string(progress.Task(t).Progress))

		delegated := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/delegate", Bob,
			map[string]any{"delegate": Alice, "comment": "over to you"})
		require.Equalf(t, transportcore.StatusOK, delegated.Status, "%s", delegated.Body)
		assert.Equal(t, Alice, delegated.Task(t).Assignee)
		assert.JSONEq(t, `{"amount":100}`, string(delegated.Task(t).Progress),
			"delegation must not lose the work already done")

		suspended := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/suspend", Alice, nil)
		require.Equal(t, transportcore.StatusOK, suspended.Status)
		assert.Equal(t, hmntsk.StatusSuspended, suspended.Task(t).Status)

		resumed := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/resume", Alice, nil)
		require.Equal(t, transportcore.StatusOK, resumed.Status)
		assert.Equal(t, hmntsk.StatusReserved, resumed.Task(t).Status)

		escalated := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/escalate", Owner, nil)
		require.Equalf(t, transportcore.StatusOK, escalated.Status, "%s", escalated.Body)
		assert.Contains(t, escalated.Task(t).Candidates.Groups, "managers")

		restarted := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/start", Alice, nil)
		require.Equal(t, transportcore.StatusOK, restarted.Status)

		completed := client.Do(t, http.MethodPost, "/tasks/"+task.ID.String()+"/complete", Alice,
			map[string]any{"output": json.RawMessage(`{"approved":false,"note":"over budget"}`)})
		require.Equalf(t, transportcore.StatusOK, completed.Status, "%s", completed.Body)
		assert.Equal(t, hmntsk.StatusCompleted, completed.Task(t).Status,
			"a denial is a completion; the outcome lives in the payload")

		read := client.Do(t, http.MethodGet, "/tasks/"+task.ID.String(), Alice, nil)
		require.Equal(t, transportcore.StatusOK, read.Status)
		assert.Equal(t, hmntsk.StatusCompleted, read.Task(t).Status)

		history := client.Do(t, http.MethodGet, "/tasks/"+task.ID.String()+"/history", Alice, nil)
		require.Equal(t, transportcore.StatusOK, history.Status)

		var log transportcore.HistoryResponse

		history.Decode(t, &log)
		assert.GreaterOrEqual(t, len(log.Records), 10, "every transition is recorded")
		assert.Equal(t, hmntsk.OpCreate, log.Records[0].Operation)
	})

	t.Run("failing a task is a terminal outcome of its own", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)
		id := task.ID.String()

		require.Equal(t, transportcore.StatusOK,
			client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, nil).Status)
		require.Equal(t, transportcore.StatusOK,
			client.Do(t, http.MethodPost, "/tasks/"+id+"/start", Alice, nil).Status)

		failed := client.Do(t, http.MethodPost, "/tasks/"+id+"/fail", Alice,
			map[string]any{"comment": "I do not have the authority"})
		require.Equalf(t, transportcore.StatusOK, failed.Status, "%s", failed.Body)
		assert.Equal(t, hmntsk.StatusFailed, failed.Task(t).Status)
		assert.Empty(t, failed.Task(t).Output, "failure requires no output")
	})

	t.Run("cancelling closes a task from any live state", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)

		cancelled := client.Do(t, http.MethodDelete, "/tasks/"+task.ID.String(), Owner,
			map[string]any{"comment": "no longer needed"})
		require.Equalf(t, transportcore.StatusOK, cancelled.Status, "%s", cancelled.Body)
		assert.Equal(t, hmntsk.StatusExited, cancelled.Task(t).Status)
	})

	t.Run("a single-candidate task is reserved on creation", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t, func(body map[string]any) {
			body["candidates"] = map[string]any{"users": []string{Alice}}
		})

		assert.Equal(t, hmntsk.StatusReserved, task.Status)
		assert.Equal(t, Alice, task.Assignee)
	})

	t.Run("a create may carry its own identifier, deadline and callback", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t, func(body map[string]any) {
			body["id"] = "019243af-9f1c-7000-8000-0123456789ab"
			body["deadlineSeconds"] = 7200
			body["priority"] = 0
			body["callback"] = map[string]any{
				"address":             "https://host.example/hook",
				"referenceParameters": json.RawMessage(`{"corr":"abc"}`),
			}
		})

		assert.Equal(t, hmntsk.TaskID("019243af-9f1c-7000-8000-0123456789ab"), task.ID)
		assert.Equal(t, hmntsk.PriorityHighest, task.Priority)
		require.NotNil(t, task.DueAt)
		require.NotNil(t, task.Callback)
		assert.Equal(t, "https://host.example/hook", task.Callback.Address)
		assert.JSONEq(t, `{"corr":"abc"}`, string(task.Callback.ReferenceParameters))
	})
}
