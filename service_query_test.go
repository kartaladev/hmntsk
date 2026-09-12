package hmntsk_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// seedInbox creates count approval tasks pooled to finance-approvers.
func (h *harness) seedInbox(t *testing.T, count int, pool hmntsk.CandidatePool) []hmntsk.TaskID {
	t.Helper()

	ids := make([]hmntsk.TaskID, 0, count)

	for i := range count {
		result, err := h.svc.Create(t.Context(), hmntsk.CreateRequest{
			Type:        "freeform",
			Input:       json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)),
			Candidates:  &pool,
			Correlation: hmntsk.CorrelationData{OwnerType: "process", OwnerRef: "p-1"},
		})
		require.NoError(t, err)

		ids = append(ids, result.Task.ID)
	}

	return ids
}

func TestServiceQueryFilters(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		query  hmntsk.Query
		assert func(t *testing.T, page hmntsk.Page, err error)
	}

	cases := []testCase{
		{
			name:  "an empty query matches everything",
			query: hmntsk.Query{},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Len(t, page.Tasks, 5)
			},
		},
		{
			name:  "a candidate sees the pooled tasks they may claim",
			query: hmntsk.Query{Candidate: "alice"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Len(t, page.Tasks, 5, "three pooled plus the two reserved for alice")
			},
		},
		{
			name:  "an excluded actor does not see the task",
			query: hmntsk.Query{Candidate: "bob"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)

				for _, task := range page.Tasks {
					assert.NotContains(t, task.Candidates.Excluded, "bob")
				}

				assert.Len(t, page.Tasks, 3,
					"the excluded task and the two held by alice are all absent")
			},
		},
		{
			name:  "an outsider sees nothing",
			query: hmntsk.Query{Candidate: "mallory"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Empty(t, page.Tasks)
			},
		},
		{
			name:  "an assignee filter distinguishes held work from claimable work",
			query: hmntsk.Query{Assignee: "alice"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				require.Len(t, page.Tasks, 2)

				for _, task := range page.Tasks {
					assert.Equal(t, "alice", task.Assignee)
				}
			},
		},
		{
			name:  "a status filter narrows to one state",
			query: hmntsk.Query{Statuses: []hmntsk.Status{hmntsk.StatusReserved}},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Len(t, page.Tasks, 2)
			},
		},
		{
			name:  "a type filter narrows to one kind of work",
			query: hmntsk.Query{Types: []string{"approval"}},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Len(t, page.Tasks, 1)
			},
		},
		{
			name:  "a correlation lookup matches on stored fields",
			query: hmntsk.Query{OwnerType: "process", OwnerRef: "p-1"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Len(t, page.Tasks, 5)
			},
		},
		{
			name:  "a correlation lookup that matches nothing returns nothing",
			query: hmntsk.Query{OwnerRef: "p-999"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Empty(t, page.Tasks)
			},
		},
		{
			name:  "mixed types come back in one response with their payloads intact",
			query: hmntsk.Query{Candidate: "alice"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)

				types := make(map[string]int)
				for _, task := range page.Tasks {
					types[task.Type]++
					assert.NotEmpty(t, task.Input)
				}

				assert.Len(t, types, 2)
			},
		},
	}

	h := newHarness(t)

	h.seedInbox(t, 3, hmntsk.CandidatePool{Groups: []string{"finance-approvers"}})
	h.seedInbox(t, 1, hmntsk.CandidatePool{
		Groups: []string{"finance-approvers"}, Excluded: []string{"bob"},
	})

	approval := h.createApproval(t)

	_, err := h.svc.Claim(t.Context(), hmntsk.TaskRequest{TaskID: approval.ID, Actor: "alice"})
	require.NoError(t, err)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page, err := h.svc.Query(t.Context(), tc.query)
			tc.assert(t, page, err)
		})
	}
}

func TestServiceQueryPagingIsStableWhileTasksAreCreated(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := t.Context()
	pool := hmntsk.CandidatePool{Groups: []string{"finance-approvers"}}

	h.seedInbox(t, 7, pool)

	seen := make(map[hmntsk.TaskID]int)
	cursor := ""
	pages := 0

	for {
		page, err := h.svc.Query(ctx, hmntsk.Query{Limit: 3, Cursor: cursor})
		require.NoError(t, err)

		for _, task := range page.Tasks {
			seen[task.ID]++
		}

		pages++

		// A writer inserting tasks between pages must not disturb what has
		// already been returned.
		h.seedInbox(t, 2, pool)

		if page.NextCursor == "" {
			break
		}

		cursor = page.NextCursor

		require.Less(t, pages, 20, "paging must terminate")
	}

	for id, count := range seen {
		assert.Equalf(t, 1, count, "task %s was returned on more than one page", id)
	}

	assert.GreaterOrEqual(t, len(seen), 7, "every task that existed at the start must be returned")
}

func TestServiceQueryDescendingPaging(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ids := h.seedInbox(t, 5, hmntsk.CandidatePool{Groups: []string{"finance-approvers"}})

	page, err := h.svc.Query(t.Context(), hmntsk.Query{Limit: 2, Descending: true})
	require.NoError(t, err)
	require.Len(t, page.Tasks, 2)
	assert.Equal(t, ids[len(ids)-1], page.Tasks[0].ID, "newest first")

	next, err := h.svc.Query(t.Context(), hmntsk.Query{
		Limit: 2, Descending: true, Cursor: page.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, next.Tasks, 2)
	assert.NotEqual(t, page.Tasks[0].ID, next.Tasks[0].ID)
	assert.NotEqual(t, page.Tasks[1].ID, next.Tasks[0].ID)
}

func TestServiceQueryLimits(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		limit  int
		assert func(t *testing.T, effective int)
	}

	cases := []testCase{
		{
			name: "zero means the default", limit: 0,
			assert: func(t *testing.T, effective int) {
				assert.Equal(t, hmntsk.DefaultQueryLimit, effective)
			},
		},
		{
			name: "a negative limit means the default", limit: -5,
			assert: func(t *testing.T, effective int) {
				assert.Equal(t, hmntsk.DefaultQueryLimit, effective)
			},
		},
		{
			name: "an explicit limit is honoured", limit: 7,
			assert: func(t *testing.T, effective int) { assert.Equal(t, 7, effective) },
		},
		{
			name: "an excessive limit is capped", limit: hmntsk.MaxQueryLimit * 10,
			assert: func(t *testing.T, effective int) {
				assert.Equal(t, hmntsk.MaxQueryLimit, effective)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.Query{Limit: tc.limit}.EffectiveLimit())
		})
	}
}
