package notify_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// validDraft is a draft that passes validation, for cases to break one field of.
func validDraft() notify.Draft {
	return notify.Draft{
		Recipient:      "alice",
		SourceID:       "event-1",
		Subject:        "task-1",
		SubjectVersion: 3,
		Kind:           "offer",
		Title:          "A task is available",
		Links:          map[string]string{"task": "/v1/tasks/task-1"},
		Data:           json.RawMessage(`{"b":1,"a":2.50}`),
	}
}

func TestDraftValidate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		draft  func(d *notify.Draft)
		assert func(t *testing.T, err error)
	}

	issue := func(t *testing.T, err error, pointer string) {
		t.Helper()

		require.ErrorIs(t, err, notify.ErrValidation)

		var validation *notify.ValidationError
		require.ErrorAs(t, err, &validation)

		pointers := make([]string, 0, len(validation.Issues))
		for _, issue := range validation.Issues {
			pointers = append(pointers, issue.Pointer)
		}

		assert.Contains(t, pointers, pointer)
	}

	cases := []testCase{
		{
			name:  "a complete draft is valid",
			draft: func(*notify.Draft) {},
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name:  "a draft with no title, links or data is valid",
			draft: func(d *notify.Draft) { d.Title, d.Links, d.Data = "", nil, nil },
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name:   "a recipient is required",
			draft:  func(d *notify.Draft) { d.Recipient = "" },
			assert: func(t *testing.T, err error) { issue(t, err, "/recipient") },
		},
		{
			name:   "a source is required",
			draft:  func(d *notify.Draft) { d.SourceID = "" },
			assert: func(t *testing.T, err error) { issue(t, err, "/sourceId") },
		},
		{
			name:   "a subject is required",
			draft:  func(d *notify.Draft) { d.Subject = "" },
			assert: func(t *testing.T, err error) { issue(t, err, "/subject") },
		},
		{
			name:   "a kind is required",
			draft:  func(d *notify.Draft) { d.Kind = "" },
			assert: func(t *testing.T, err error) { issue(t, err, "/kind") },
		},
		{
			name:   "a negative subject version is refused",
			draft:  func(d *notify.Draft) { d.SubjectVersion = -1 },
			assert: func(t *testing.T, err error) { issue(t, err, "/subjectVersion") },
		},
		{
			name:   "a kind longer than 100 bytes is refused",
			draft:  func(d *notify.Draft) { d.Kind = strings.Repeat("k", 101) },
			assert: func(t *testing.T, err error) { issue(t, err, "/kind") },
		},
		{
			name:  "a kind of exactly 100 bytes is valid",
			draft: func(d *notify.Draft) { d.Kind = strings.Repeat("k", 100) },
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name:   "a subject longer than 255 bytes is refused",
			draft:  func(d *notify.Draft) { d.Subject = strings.Repeat("s", 256) },
			assert: func(t *testing.T, err error) { issue(t, err, "/subject") },
		},
		{
			name:   "a source longer than 255 bytes is refused",
			draft:  func(d *notify.Draft) { d.SourceID = strings.Repeat("s", 256) },
			assert: func(t *testing.T, err error) { issue(t, err, "/sourceId") },
		},
		{
			name:   "a recipient longer than 255 bytes is refused",
			draft:  func(d *notify.Draft) { d.Recipient = strings.Repeat("r", 256) },
			assert: func(t *testing.T, err error) { issue(t, err, "/recipient") },
		},
		{
			name:   "data that is not JSON is refused",
			draft:  func(d *notify.Draft) { d.Data = json.RawMessage(`{"open":`) },
			assert: func(t *testing.T, err error) { issue(t, err, "/data") },
		},
		{
			name:  "every problem is reported at once",
			draft: func(d *notify.Draft) { d.Recipient, d.Kind = "", "" },
			assert: func(t *testing.T, err error) {
				issue(t, err, "/recipient")
				issue(t, err, "/kind")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			draft := validDraft()
			tc.draft(&draft)

			tc.assert(t, draft.Validate())
		})
	}
}

func TestNotificationJSONKeepsOpaqueContentExact(t *testing.T) {
	t.Parallel()

	n := notify.Notification{
		ID: "n-1", Recipient: "alice", SourceID: "event-1", Subject: "task-1", SubjectVersion: 3,
		Kind: "offer", State: notify.StateActive, Data: json.RawMessage(`{"b":1,"a":2.50}`),
	}

	encoded, err := json.Marshal(n)
	require.NoError(t, err)

	assert.Contains(t, string(encoded), `"data":{"b":1,"a":2.50}`)
	assert.Contains(t, string(encoded), `"state":"ACTIVE"`)
	assert.NotContains(t, string(encoded), `"readAt"`)
}
