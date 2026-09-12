package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// runRepositoryCases covers reading, writing, history and querying.
func runRepositoryCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("RoundTrip", func(t *testing.T) { repositoryRoundTrip(t, factory) })
	t.Run("Update", func(t *testing.T) { repositoryUpdate(t, factory) })
	t.Run("History", func(t *testing.T) { repositoryHistory(t, factory) })
	t.Run("Query", func(t *testing.T) { repositoryQuery(t, factory) })
	t.Run("Lease", func(t *testing.T) { repositoryLease(t, factory) })
}

// repositoryRoundTrip asserts that everything the engine stores comes back.
func repositoryRoundTrip(t *testing.T, factory Factory) {
	t.Helper()

	type testCase struct {
		name   string
		task   hmntsk.Task
		assert func(t *testing.T, stored hmntsk.Task)
	}

	var seq ids

	cases := []testCase{
		{
			name: "a pooled task round-trips whole",
			task: NewTask(seq.next()),
			assert: func(t *testing.T, stored hmntsk.Task) {
				assert.Equal(t, hmntsk.StatusReady, stored.Status)
				assert.Equal(t, TaskType, stored.Type)
				assert.Equal(t, int64(1), stored.Version)
				assert.Equal(t, []string{Assignee, OtherActor}, stored.Candidates.Users)
				assert.Equal(t, []string{"finance-approvers"}, stored.Candidates.Groups)
				assert.Equal(t, []string{"mallory"}, stored.Candidates.Excluded)
				assert.Equal(t, "p-1", stored.Correlation.OwnerRef)
				require.NotNil(t, stored.Callback)
				assert.Equal(t, "https://host.example/hook", stored.Callback.Address)
			},
		},
		{
			name: "an opaque payload comes back byte for byte",
			task: NewTask(seq.next(), func(task *hmntsk.Task) {
				task.Input = []byte(`{"zulu":1,"amount":9007199254740993,"rate":1.500,"unknown":{"a":[1,null,true]}}`)
			}),
			assert: func(t *testing.T, stored hmntsk.Task) {
				//nolint:testifylint // byte-exact comparison is the property under test.
				assert.Equal(t,
					`{"zulu":1,"amount":9007199254740993,"rate":1.500,"unknown":{"a":[1,null,true]}}`,
					string(stored.Input),
					"key order, number literals and undescribed fields must all survive the "+
						"database; a native JSON column type would quietly rewrite all three")
			},
		},
		{
			name: "reference parameters are stored opaquely",
			task: NewTask(seq.next()),
			assert: func(t *testing.T, stored hmntsk.Task) {
				require.NotNil(t, stored.Callback)
				//nolint:testifylint // reference parameters are echoed verbatim or not at all.
				assert.Equal(t, `{"corr":"abc","seq":9007199254740993}`,
					string(stored.Callback.ReferenceParameters))
			},
		},
		{
			name: "a suspended task keeps the state it must return to",
			task: NewTask(seq.next(), func(task *hmntsk.Task) {
				task.Status = hmntsk.StatusSuspended
				task.SuspendedFrom = hmntsk.StatusInProgress
				task.Assignee = Assignee
			}),
			assert: func(t *testing.T, stored hmntsk.Task) {
				assert.Equal(t, hmntsk.StatusSuspended, stored.Status)
				assert.Equal(t, hmntsk.StatusInProgress, stored.SuspendedFrom)
				assert.Equal(t, Assignee, stored.Assignee)
			},
		},
		{
			name: "a task with no deadline, callback or pool round-trips too",
			task: NewTask(seq.next(), func(task *hmntsk.Task) {
				task.DueAt = nil
				task.Callback = nil
				task.Candidates = hmntsk.CandidatePool{}
				task.Input = nil
			}),
			assert: func(t *testing.T, stored hmntsk.Task) {
				assert.Nil(t, stored.DueAt)
				assert.Nil(t, stored.Callback)
				assert.True(t, stored.Candidates.IsEmpty())
				assert.Empty(t, stored.Input)
			},
		},
		{
			name: "a completed task keeps its output and closing time",
			task: NewTask(seq.next(), func(task *hmntsk.Task) {
				task.Status = hmntsk.StatusCompleted
				task.Assignee = Assignee
				task.Output = []byte(`{"approved":false}`)
				closed := Reference.Add(time.Hour)
				task.ClosedAt = &closed
			}),
			assert: func(t *testing.T, stored hmntsk.Task) {
				assert.JSONEq(t, `{"approved":false}`, string(stored.Output))
				require.NotNil(t, stored.ClosedAt)
				assert.True(t, Reference.Add(time.Hour).Equal(*stored.ClosedAt))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := factory(t)
			tc.assert(t, Seed(t, h, tc.task))
		})
	}
}

// repositoryUpdate covers the conditional write that is the engine's only
// concurrency mechanism.
func repositoryUpdate(t *testing.T, factory Factory) {
	t.Helper()

	type testCase struct {
		name     string
		expected int64
		mutate   func(task *hmntsk.Task)
		assert   func(t *testing.T, h Harness, id hmntsk.TaskID, err error)
	}

	cases := []testCase{
		{
			name:     "a write against the observed version succeeds",
			expected: 1,
			mutate: func(task *hmntsk.Task) {
				task.Version = 2
				task.Status = hmntsk.StatusReserved
				task.Assignee = Assignee
			},
			assert: func(t *testing.T, h Harness, id hmntsk.TaskID, err error) {
				require.NoError(t, err)

				stored, getErr := h.Store.Get(t.Context(), id)
				require.NoError(t, getErr)
				assert.Equal(t, int64(2), stored.Version, "the version must advance on every mutation")
				assert.Equal(t, hmntsk.StatusReserved, stored.Status)
				assert.Equal(t, Assignee, stored.Assignee)
			},
		},
		{
			name:     "a write against a stale version is refused and reports the current one",
			expected: 99,
			mutate: func(task *hmntsk.Task) {
				task.Version = 100
				task.Status = hmntsk.StatusExited
			},
			assert: func(t *testing.T, h Harness, id hmntsk.TaskID, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConflict)

				var conflict *hmntsk.ConflictError

				require.ErrorAs(t, err, &conflict)
				assert.Equal(t, int64(1), conflict.Current)
				assert.Equal(t, int64(99), conflict.Expected)

				stored, getErr := h.Store.Get(t.Context(), id)
				require.NoError(t, getErr)
				assert.Equal(t, hmntsk.StatusReady, stored.Status, "a refused write changes nothing")
				assert.Equal(t, int64(1), stored.Version)
			},
		},
		{
			name:     "a candidate pool can be widened in place",
			expected: 1,
			mutate: func(task *hmntsk.Task) {
				task.Version = 2
				task.Candidates.Groups = append(task.Candidates.Groups, "managers")
			},
			assert: func(t *testing.T, h Harness, id hmntsk.TaskID, err error) {
				require.NoError(t, err)

				stored, getErr := h.Store.Get(t.Context(), id)
				require.NoError(t, getErr)
				assert.ElementsMatch(t,
					[]string{"finance-approvers", "managers"}, stored.Candidates.Groups)
				assert.ElementsMatch(t, []string{Assignee, OtherActor}, stored.Candidates.Users)
			},
		},
		{
			name:     "an assignee can be cleared",
			expected: 1,
			mutate: func(task *hmntsk.Task) {
				task.Version = 2
				task.Assignee = ""
			},
			assert: func(t *testing.T, h Harness, id hmntsk.TaskID, err error) {
				require.NoError(t, err)

				stored, getErr := h.Store.Get(t.Context(), id)
				require.NoError(t, getErr)
				assert.Empty(t, stored.Assignee)
			},
		},
	}

	var seq ids

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := factory(t)
			task := Seed(t, h, NewTask(seq.next(), func(task *hmntsk.Task) {
				task.Assignee = ""
			}))

			updated := task.Clone()
			tc.mutate(&updated)

			err := h.Store.Do(t.Context(), func(ctx context.Context) error {
				return h.Store.Update(ctx, updated, tc.expected)
			})

			tc.assert(t, h, task.ID, err)
		})
	}
}

// repositoryHistory asserts the transition log is append-only and complete.
func repositoryHistory(t *testing.T, factory Factory) {
	t.Helper()

	h := factory(t)

	var seq ids

	task := Seed(t, h, NewTask(seq.next()))

	first := NewRecord(task, hmntsk.OpCreate, hmntsk.StatusCreated, hmntsk.StatusReady)
	second := NewRecord(task, hmntsk.OpClaim, hmntsk.StatusReady, hmntsk.StatusReserved)
	second.Version = 2
	second.At = Reference.Add(time.Minute)

	require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
		return h.Store.AppendHistory(ctx, first)
	}))

	require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
		return h.Store.AppendHistory(ctx, second)
	}))

	records, err := h.Store.History(t.Context(), task.ID)
	require.NoError(t, err)
	require.Len(t, records, 2, "earlier records must remain readable")

	assert.Equal(t, hmntsk.OpCreate, records[0].Operation)
	assert.Equal(t, hmntsk.OpClaim, records[1].Operation)
	assert.Equal(t, Assignee, records[1].Actor)
	assert.Equal(t, "recorded by the conformance suite", records[1].Comment)
	assert.True(t, Reference.Add(time.Minute).Equal(records[1].At))
	assert.Equal(t, hmntsk.StatusReady, records[1].From)
	assert.Equal(t, hmntsk.StatusReserved, records[1].To)

	missing, err := h.Store.History(t.Context(), "no-such-task")
	require.NoError(t, err)
	assert.Empty(t, missing)
}

// repositoryQuery covers the filters the inbox depends on, and paging.
func repositoryQuery(t *testing.T, factory Factory) {
	t.Helper()

	type testCase struct {
		name   string
		query  hmntsk.ResolvedQuery
		assert func(t *testing.T, page hmntsk.Page, err error)
	}

	var seq ids

	pooled := seq.next()
	held := seq.next()
	excluded := seq.next()
	otherProcess := seq.next()
	done := seq.next()

	cases := []testCase{
		{
			name:  "every task matches an empty query",
			query: hmntsk.ResolvedQuery{},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Len(t, page.Tasks, 5)
			},
		},
		{
			name:  "an assignee filter finds held work",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Assignee: Assignee}},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				require.Len(t, page.Tasks, 1)
				assert.Equal(t, held, page.Tasks[0].ID)
			},
		},
		{
			name: "a candidate sees pooled work they may claim",
			query: hmntsk.ResolvedQuery{
				Query:           hmntsk.Query{Candidate: OtherActor},
				CandidateGroups: []string{"finance-approvers"},
			},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)

				found := make([]hmntsk.TaskID, 0, len(page.Tasks))
				for _, task := range page.Tasks {
					found = append(found, task.ID)
				}

				assert.Contains(t, found, pooled)
				assert.NotContains(t, found, held, "work somebody else holds is not claimable")
			},
		},
		{
			name: "an excluded actor does not see the task",
			query: hmntsk.ResolvedQuery{
				Query:           hmntsk.Query{Candidate: "mallory"},
				CandidateGroups: []string{"finance-approvers"},
			},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)

				for _, task := range page.Tasks {
					assert.NotEqual(t, excluded, task.ID)
				}
			},
		},
		{
			name: "a status filter narrows to one state",
			query: hmntsk.ResolvedQuery{
				Query: hmntsk.Query{Statuses: []hmntsk.Status{hmntsk.StatusCompleted}},
			},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				require.Len(t, page.Tasks, 1)
				assert.Equal(t, done, page.Tasks[0].ID)
			},
		},
		{
			name: "a correlation lookup inspects no payload",
			query: hmntsk.ResolvedQuery{
				Query: hmntsk.Query{OwnerType: "process", OwnerRef: "p-2"},
			},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				require.Len(t, page.Tasks, 1)
				assert.Equal(t, otherProcess, page.Tasks[0].ID)
			},
		},
		{
			name:  "a type filter narrows to one kind of work",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Types: []string{"other"}}},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Empty(t, page.Tasks)
			},
		},
		{
			name:  "a page is capped and reports where to continue",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Limit: 2}},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Len(t, page.Tasks, 2)
				assert.NotEmpty(t, page.NextCursor)
			},
		},
		{
			name:  "the last page reports no continuation",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Limit: 50}},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Empty(t, page.NextCursor)
			},
		},
	}

	h := factory(t)

	Seed(t, h, NewTask(pooled))
	Seed(t, h, NewTask(held, func(task *hmntsk.Task) {
		task.Status = hmntsk.StatusReserved
		task.Assignee = Assignee
	}))
	Seed(t, h, NewTask(excluded, func(task *hmntsk.Task) {
		task.Candidates.Excluded = []string{"mallory"}
		task.Candidates.Users = nil
	}))
	Seed(t, h, NewTask(otherProcess, func(task *hmntsk.Task) {
		task.Correlation.OwnerRef = "p-2"
	}))
	Seed(t, h, NewTask(done, func(task *hmntsk.Task) {
		task.Status = hmntsk.StatusCompleted
		task.Assignee = OtherActor
	}))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := h.Store.Query(t.Context(), tc.query)
			tc.assert(t, page, err)
		})
	}

	t.Run("paging returns every task exactly once", func(t *testing.T) {
		seen := make(map[hmntsk.TaskID]int)
		cursor := ""

		for range 10 {
			page, err := h.Store.Query(t.Context(), hmntsk.ResolvedQuery{
				Query: hmntsk.Query{Limit: 2, Cursor: cursor},
			})
			require.NoError(t, err)

			for _, task := range page.Tasks {
				seen[task.ID]++
			}

			if page.NextCursor == "" {
				break
			}

			cursor = page.NextCursor
		}

		assert.Len(t, seen, 5)

		for id, count := range seen {
			assert.Equalf(t, 1, count, "task %s appeared on more than one page", id)
		}
	})
}

// repositoryLease covers the exclusive claiming the escalation sweep needs.
func repositoryLease(t *testing.T, factory Factory) {
	t.Helper()

	var seq ids

	overdue := seq.next()
	future := seq.next()
	finished := seq.next()
	paused := seq.next()

	h := factory(t)

	past := Reference.Add(-time.Hour)
	ahead := Reference.Add(time.Hour)

	Seed(t, h, NewTask(overdue, func(task *hmntsk.Task) { task.DueAt = &past }))
	Seed(t, h, NewTask(future, func(task *hmntsk.Task) { task.DueAt = &ahead }))
	Seed(t, h, NewTask(finished, func(task *hmntsk.Task) {
		task.DueAt = &past
		task.Status = hmntsk.StatusCompleted
		task.Assignee = Assignee
	}))
	Seed(t, h, NewTask(paused, func(task *hmntsk.Task) {
		task.DueAt = &past
		task.Status = hmntsk.StatusSuspended
		task.SuspendedFrom = hmntsk.StatusReady
	}))

	claim := func(t *testing.T, owner string) []hmntsk.Task {
		t.Helper()

		var claimed []hmntsk.Task

		require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
			var err error

			claimed, err = h.Store.ClaimOverdue(ctx, hmntsk.LeaseRequest{
				Now: Reference, Owner: owner, Duration: time.Minute, Limit: 10,
			})

			return err
		}))

		return claimed
	}

	first := claim(t, "sweeper-1")
	require.Len(t, first, 1, "only the overdue, working task is claimable")
	assert.Equal(t, overdue, first[0].ID)
	assert.Equal(t, "sweeper-1", first[0].LockedBy)
	require.NotNil(t, first[0].LockedUntil)

	second := claim(t, "sweeper-2")
	assert.Empty(t, second, "a leased task must not be claimed twice")

	// Once the lease has expired the task is available again, so a sweeper that
	// crashed without releasing it does not strand the work forever.
	var afterExpiry []hmntsk.Task

	require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
		var err error

		afterExpiry, err = h.Store.ClaimOverdue(ctx, hmntsk.LeaseRequest{
			Now:      Reference.Add(2 * time.Minute),
			Owner:    "sweeper-3",
			Duration: time.Minute,
			Limit:    10,
		})

		return err
	}))

	require.Len(t, afterExpiry, 1)
	assert.Equal(t, overdue, afterExpiry[0].ID)
	assert.Equal(t, "sweeper-3", afterExpiry[0].LockedBy)
}
