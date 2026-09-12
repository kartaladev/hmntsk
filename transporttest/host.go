package transporttest

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// runHostCases pins the division of labour: the host decides who is calling,
// and the engine takes that as an input.
func runHostCases(t *testing.T, mount Mount) {
	t.Helper()

	t.Run("the acting actor comes from what the host established", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)
		id := task.ID.String()

		claimed := client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, nil)
		require.Equal(t, transportcore.StatusOK, claimed.Status)
		assert.Equal(t, Alice, claimed.Task(t).Assignee,
			"the engine acts for whoever the host's middleware says is calling")
	})

	t.Run("the engine authenticates nobody itself", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)
		id := task.ID.String()

		// No actor at all. The engine does not challenge, does not consult a
		// credential and does not invent one: it simply has no actor to act
		// for, and the operation is refused on that basis.
		anonymous := client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", "", nil)
		require.Equal(t, transportcore.StatusForbidden, anonymous.Status)
		assert.Equal(t, transportcore.CodeForbidden, anonymous.Error(t).Code)

		assert.Empty(t, anonymous.ContentTypeChallenge(),
			"no authentication scheme is offered, because none is the engine's")
	})

	t.Run("who is calling changes the answer and nothing else", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t)
		id := task.ID.String()

		require.Equal(t, transportcore.StatusOK,
			client.Do(t, http.MethodPost, "/tasks/"+id+"/claim", Alice, nil).Status)

		// The same request, from the wrong actor.
		refused := client.Do(t, http.MethodPost, "/tasks/"+id+"/start", Bob, nil)
		assert.Equal(t, transportcore.StatusForbidden, refused.Status)

		accepted := client.Do(t, http.MethodPost, "/tasks/"+id+"/start", Alice, nil)
		assert.Equal(t, transportcore.StatusOK, accepted.Status)
	})
}
