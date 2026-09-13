package hmntsk_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk"
)

// seedCounts wires a harness over the inbox the count tests share: three tasks
// pooled to finance-approvers, and one approval alice has claimed.
func seedCounts(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)

	h.seedInbox(t, 3, hmntsk.CandidatePool{Groups: []string{"finance-approvers"}})

	approval := h.createApproval(t)

	_, err := h.svc.Claim(t.Context(), hmntsk.TaskRequest{TaskID: approval.ID, Actor: "alice"})
	require.NoError(t, err)

	return h
}

func TestServiceCount(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		query  hmntsk.Query
		assert func(t *testing.T, count int64, err error)
	}

	cases := []testCase{
		{
			name:  "a count matches the query and ignores the page size",
			query: hmntsk.Query{Candidate: "alice", Limit: 2},
			assert: func(t *testing.T, count int64, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(4), count, "three pooled plus the one alice holds")
			},
		},
		{
			name:  "a candidate count resolves eligibility",
			query: hmntsk.Query{Candidate: "bob"},
			assert: func(t *testing.T, count int64, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(3), count, "the task alice holds is not bob's to claim")
			},
		},
		{
			name:  "an assignee count finds held work",
			query: hmntsk.Query{Assignee: "alice"},
			assert: func(t *testing.T, count int64, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(1), count)
			},
		},
		{
			name:  "an unsupported ordering is a validation error",
			query: hmntsk.Query{Candidate: "alice", OrderBy: "bogus"},
			assert: func(t *testing.T, count int64, err error) {
				require.ErrorIs(t, err, hmntsk.ErrValidation)
				assert.Zero(t, count)
			},
		},
	}

	h := seedCounts(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			count, err := h.svc.Count(t.Context(), tc.query)
			tc.assert(t, count, err)
		})
	}
}

func TestServiceCountBuckets(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		buckets map[string]hmntsk.Query
		// resolver sets the expectations on a strict resolver double. A case
		// that sets none fails if the service resolves anything at all.
		resolver func(r *hmntsk.MockGroupResolverMockRecorder)
		assert   func(t *testing.T, counts map[string]int64, err error)
	}

	tooMany := make(map[string]hmntsk.Query, hmntsk.MaxCountBuckets+1)
	for i := range hmntsk.MaxCountBuckets + 1 {
		tooMany[fmt.Sprintf("bucket-%d", i)] = hmntsk.Query{Assignee: "alice"}
	}

	cases := []testCase{
		{
			name:    "an empty bucket set returns an empty result",
			buckets: map[string]hmntsk.Query{},
			assert: func(t *testing.T, counts map[string]int64, err error) {
				require.NoError(t, err)
				assert.NotNil(t, counts)
				assert.Empty(t, counts)
			},
		},
		{
			name:    "more buckets than the limit is a validation error",
			buckets: tooMany,
			assert: func(t *testing.T, counts map[string]int64, err error) {
				require.ErrorIs(t, err, hmntsk.ErrValidation)
				assert.Nil(t, counts)
			},
		},
		{
			name: "an unsupported ordering in any bucket is a validation error",
			buckets: map[string]hmntsk.Query{
				"mine": {Assignee: "alice"},
				"odd":  {Candidate: "alice", OrderBy: "bogus"},
			},
			assert: func(t *testing.T, counts map[string]int64, err error) {
				require.ErrorIs(t, err, hmntsk.ErrValidation)
				assert.Nil(t, counts)
			},
		},
		{
			name: "each bucket counts as its query alone, resolving each candidate once",
			buckets: map[string]hmntsk.Query{
				"mine":      {Assignee: "alice"},
				"available": {Candidate: "alice"},
				"available-ready": {
					Candidate: "alice", Statuses: []hmntsk.Status{hmntsk.StatusReady},
				},
				"bob": {Candidate: "bob"},
			},
			resolver: func(r *hmntsk.MockGroupResolverMockRecorder) {
				r.GroupsOf(gomock.Any(), "alice").Return([]string{"finance-approvers"}, nil).Times(1)
				r.GroupsOf(gomock.Any(), "bob").Return([]string{"finance-approvers"}, nil).Times(1)
			},
			assert: func(t *testing.T, counts map[string]int64, err error) {
				require.NoError(t, err)
				assert.Equal(t, map[string]int64{
					"mine": 1, "available": 4, "available-ready": 3, "bob": 3,
				}, counts)
			},
		},
	}

	h := seedCounts(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := hmntsk.NewMockGroupResolver(gomock.NewController(t))
			if tc.resolver != nil {
				tc.resolver(resolver.EXPECT())
			}

			svc, err := hmntsk.New(h.store,
				hmntsk.WithRegistry(h.svc.Registry()),
				hmntsk.WithGroupResolver(resolver),
			)
			require.NoError(t, err)

			counts, err := svc.CountBuckets(t.Context(), tc.buckets)
			tc.assert(t, counts, err)
		})
	}
}
