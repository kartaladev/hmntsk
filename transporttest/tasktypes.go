package transporttest

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// runTaskTypeCases covers the endpoint that lets a client render a form for a
// kind of work it has never seen.
func runTaskTypeCases(t *testing.T, mount Mount) {
	t.Helper()

	t.Run("a client can retrieve the schemas of an unfamiliar type", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		// A client finds a task of a type it does not recognise in an inbox.
		task := client.CreateApproval(t)

		result := client.Do(t, http.MethodGet, "/task-types/"+task.Type, Alice, nil)
		require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)

		var spec transportcore.TaskTypeResponse

		result.Decode(t, &spec)
		assert.Equal(t, "approval", spec.Name)
		assert.Equal(t, "Approval", spec.Title)
		assert.JSONEq(t, ApprovalInputSchema, string(spec.InputSchema),
			"the schemas come back as they were registered")
		assert.JSONEq(t, ApprovalOutputSchema, string(spec.OutputSchema))
		assert.Equal(t, int64(3600), spec.DefaultDeadlineSeconds)
	})

	t.Run("a client can link a task to its business form", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		result := client.Do(t, http.MethodGet, "/task-types/approval", Alice, nil)
		require.Equalf(t, transportcore.StatusOK, result.Status, "%s", result.Body)

		var spec struct {
			Metadata map[string]string `json:"metadata"`
		}

		result.Decode(t, &spec)
		assert.Equal(t, approvalMetadata(), spec.Metadata,
			"metadata comes back exactly as registered, host keys beside the well-known ones")
	})

	t.Run("every registered type is listed", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		result := client.Do(t, http.MethodGet, "/task-types", Alice, nil)
		require.Equal(t, transportcore.StatusOK, result.Status)

		var list transportcore.TaskTypeListResponse

		result.Decode(t, &list)
		require.Len(t, list.Types, 2)

		names := make([]string, 0, len(list.Types))
		for _, spec := range list.Types {
			names = append(names, spec.Name)
		}

		assert.Equal(t, []string{"approval", "note"}, names, "listed by name")
	})

	t.Run("an unregistered type is not found", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		result := client.Do(t, http.MethodGet, "/task-types/approvel", Alice, nil)
		require.Equal(t, transportcore.StatusNotFound, result.Status)
		assert.Equal(t, "approvel", result.Error(t).TaskType)
	})
}
