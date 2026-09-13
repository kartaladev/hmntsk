package storetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// runInboxCases covers what a host builds an inbox from: every supported
// ordering with exact paging, continuation tokens bound to their ordering, a
// group's queue, and counts.
//
// Every adapter must return identical sequences for identical data. That is the
// point of running these here rather than per adapter: the three dialects
// disagree about where NULL sorts, and the no-deadline tasks in the fixture are
// there to catch an adapter that inherited its dialect's opinion.
func runInboxCases(t *testing.T, factory Factory) {
	t.Helper()

	h := factory(t)
	task := seedInbox(t, h)

	t.Run("orderings", func(t *testing.T) { inboxOrderings(t, h, task) })
	t.Run("cursors", func(t *testing.T) { inboxCursors(t, h, task) })
	t.Run("groups", func(t *testing.T) { inboxGroups(t, h, task) })
	t.Run("counts", func(t *testing.T) { inboxCounts(t, h, task) })
	t.Run("paging is stable while tasks are created", func(t *testing.T) {
		inboxStablePaging(t, factory)
	})
}

// seedInbox stores the inbox fixture and returns a function naming its tasks by
// number, in creation order:
//
//	#  priority  due    pool                           held by  owner type
//	1  1         +2h    finance-approvers              -        process
//	2  1         none   finance-approvers              -        process
//	3  0         +3h    managers                       -        invoice
//	4  5         +1h    finance-approvers              -        invoice
//	5  1         +2h    finance-approvers              alice    process
//	6  5         none   alice, finance-approvers       -        process
//	7  0         none   bob                            -        process
//	8  5         +1h    finance-approvers              -        invoice
//
// Tasks 1 and 5, and tasks 4 and 8, tie on both priority and due date, so only
// the creation tiebreak separates them. Task 6 is within alice's reach twice
// over, by name and by group, which is what a count must not double.
func seedInbox(t *testing.T, h Harness) func(numbers ...int) []hmntsk.TaskID {
	t.Helper()

	soon, later, latest := Reference.Add(time.Hour), Reference.Add(2*time.Hour), Reference.Add(3*time.Hour)
	finance := hmntsk.CandidatePool{Groups: []string{"finance-approvers"}}

	type fixture struct {
		priority  hmntsk.Priority
		due       *time.Time
		pool      hmntsk.CandidatePool
		assignee  string
		ownerType string
	}

	fixtures := []fixture{
		{priority: 1, due: &later, pool: finance, ownerType: "process"},
		{priority: 1, pool: finance, ownerType: "process"},
		{priority: 0, due: &latest, pool: hmntsk.CandidatePool{Groups: []string{"managers"}}, ownerType: "invoice"},
		{priority: 5, due: &soon, pool: finance, ownerType: "invoice"},
		{priority: 1, due: &later, pool: finance, assignee: Assignee, ownerType: "process"},
		{
			priority: 5, ownerType: "process",
			pool: hmntsk.CandidatePool{Users: []string{Assignee}, Groups: []string{"finance-approvers"}},
		},
		{priority: 0, pool: hmntsk.CandidatePool{Users: []string{OtherActor}}, ownerType: "process"},
		{priority: 5, due: &soon, pool: finance, ownerType: "invoice"},
	}

	var seq ids

	// Index zero is unused, so that a task's index is its number in the table.
	stored := make([]hmntsk.TaskID, len(fixtures)+1)

	for i, f := range fixtures {
		id := seq.next()

		Seed(t, h, NewTask(id, func(task *hmntsk.Task) {
			task.Priority = f.priority
			task.DueAt = f.due
			task.Candidates = f.pool
			task.Assignee = f.assignee
			task.Correlation.OwnerType = f.ownerType

			if f.assignee != "" {
				task.Status = hmntsk.StatusReserved
			}
		}))

		stored[i+1] = id
	}

	return func(numbers ...int) []hmntsk.TaskID {
		out := make([]hmntsk.TaskID, 0, len(numbers))
		for _, n := range numbers {
			out = append(out, stored[n])
		}

		return out
	}
}

// collect pages through a query and returns every identifier in the order the
// pages delivered them.
func collect(t *testing.T, h Harness, query hmntsk.ResolvedQuery) []hmntsk.TaskID {
	t.Helper()

	var out []hmntsk.TaskID

	for range 50 {
		page, err := h.Store.Query(t.Context(), query)
		require.NoError(t, err)

		for _, task := range page.Tasks {
			out = append(out, task.ID)
		}

		if page.NextCursor == "" {
			return out
		}

		query.Cursor = page.NextCursor
	}

	t.Fatal("paging did not terminate")

	return nil
}

// inboxOrderings covers every ordering in both directions, each read whole and
// read two tasks at a time.
func inboxOrderings(t *testing.T, h Harness, task func(numbers ...int) []hmntsk.TaskID) {
	t.Helper()

	type testCase struct {
		name   string
		query  hmntsk.Query
		groups []string
		assert func(t *testing.T, unpaged, paged []hmntsk.TaskID)
	}

	inOrder := func(numbers ...int) func(t *testing.T, unpaged, paged []hmntsk.TaskID) {
		return func(t *testing.T, unpaged, paged []hmntsk.TaskID) {
			assert.Equal(t, task(numbers...), unpaged, "read whole")
			assert.Equal(t, task(numbers...), paged,
				"pages of two must concatenate to the same sequence, with no repeats or gaps")
		}
	}

	cases := []testCase{
		{
			name:   "creation order is the default",
			query:  hmntsk.Query{},
			assert: inOrder(1, 2, 3, 4, 5, 6, 7, 8),
		},
		{
			name:   "creation order, descending",
			query:  hmntsk.Query{Descending: true},
			assert: inOrder(8, 7, 6, 5, 4, 3, 2, 1),
		},
		{
			name:   "priority puts the most urgent first",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderPriority},
			assert: inOrder(3, 7, 1, 2, 5, 4, 6, 8),
		},
		{
			name:   "priority, descending, puts the least urgent first",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderPriority, Descending: true},
			assert: inOrder(8, 6, 4, 5, 2, 1, 7, 3),
		},
		{
			name:   "due date puts tasks without a deadline last",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderDue},
			assert: inOrder(4, 8, 1, 5, 3, 2, 6, 7),
		},
		{
			name:   "due date, descending, still puts tasks without a deadline last",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderDue, Descending: true},
			assert: inOrder(3, 5, 1, 8, 4, 7, 6, 2),
		},
		{
			name:   "urgency is priority, then due date with no deadline last, then creation",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderUrgency},
			assert: inOrder(3, 7, 1, 5, 2, 4, 8, 6),
		},
		{
			name:   "urgency, descending, keeps tasks without a deadline last within a priority",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderUrgency, Descending: true},
			assert: inOrder(8, 4, 6, 5, 1, 2, 3, 7),
		},
		{
			name:   "urgency over one candidate's eligible work",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderUrgency, Candidate: Assignee},
			groups: []string{"finance-approvers"},
			assert: inOrder(1, 5, 2, 4, 8, 6),
		},
		{
			name:   "due date over a correlation filter, descending",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderDue, Descending: true, OwnerType: "invoice"},
			assert: inOrder(3, 8, 4),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			whole := tc.query
			whole.Limit = hmntsk.MaxQueryLimit

			paged := tc.query
			paged.Limit = 2

			tc.assert(t,
				collect(t, h, hmntsk.ResolvedQuery{Query: whole, CandidateGroups: tc.groups}),
				collect(t, h, hmntsk.ResolvedQuery{Query: paged, CandidateGroups: tc.groups}),
			)
		})
	}
}

// inboxCursors covers continuation tokens: bound to the ordering and direction
// that produced them, and refused otherwise rather than reinterpreted.
func inboxCursors(t *testing.T, h Harness, task func(numbers ...int) []hmntsk.TaskID) {
	t.Helper()

	type testCase struct {
		name   string
		query  hmntsk.Query
		assert func(t *testing.T, page hmntsk.Page, err error)
	}

	cursorOf := func(query hmntsk.Query) string {
		page, err := h.Store.Query(t.Context(), hmntsk.ResolvedQuery{Query: query})
		require.NoError(t, err)
		require.NotEmpty(t, page.NextCursor)

		return page.NextCursor
	}

	created := cursorOf(hmntsk.Query{Limit: 2})
	byPriority := cursorOf(hmntsk.Query{Limit: 2, OrderBy: hmntsk.OrderPriority})

	refused := func(t *testing.T, page hmntsk.Page, err error) {
		require.ErrorIs(t, err, hmntsk.ErrValidation)
		assert.Empty(t, page.Tasks)
	}

	cases := []testCase{
		{
			name:  "a cursor continues the query that produced it",
			query: hmntsk.Query{Limit: 2, Cursor: created},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Equal(t, task(3, 4), taskIDs(page.Tasks))
			},
		},
		{
			name:   "a creation-order cursor is refused under priority order",
			query:  hmntsk.Query{Limit: 2, OrderBy: hmntsk.OrderPriority, Cursor: created},
			assert: refused,
		},
		{
			name:   "a priority-order cursor is refused under creation order",
			query:  hmntsk.Query{Limit: 2, Cursor: byPriority},
			assert: refused,
		},
		{
			name:   "a cursor is refused when the direction changes",
			query:  hmntsk.Query{Limit: 2, Descending: true, Cursor: created},
			assert: refused,
		},
		{
			name:   "an unsupported ordering is refused, never read as creation order",
			query:  hmntsk.Query{Limit: 2, OrderBy: "bogus"},
			assert: refused,
		},
		{
			name:   "a malformed cursor is refused",
			query:  hmntsk.Query{Limit: 2, Cursor: "not-a-cursor"},
			assert: refused,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := h.Store.Query(t.Context(), hmntsk.ResolvedQuery{Query: tc.query})
			tc.assert(t, page, err)
		})
	}
}

// inboxGroups covers a group's queue: the pool as configured, with no
// membership resolved.
func inboxGroups(t *testing.T, h Harness, task func(numbers ...int) []hmntsk.TaskID) {
	t.Helper()

	type testCase struct {
		name   string
		query  hmntsk.Query
		assert func(t *testing.T, page hmntsk.Page, err error)
	}

	matching := func(numbers ...int) func(t *testing.T, page hmntsk.Page, err error) {
		return func(t *testing.T, page hmntsk.Page, err error) {
			require.NoError(t, err)
			assert.Equal(t, task(numbers...), taskIDs(page.Tasks))
		}
	}

	cases := []testCase{
		{
			name:   "a group's queue is every task whose pool names it, held or not",
			query:  hmntsk.Query{Group: "finance-approvers"},
			assert: matching(1, 2, 4, 5, 6, 8),
		},
		{
			name:   "a team queue narrowed to ready work",
			query:  hmntsk.Query{Group: "finance-approvers", Statuses: []hmntsk.Status{hmntsk.StatusReady}},
			assert: matching(1, 2, 4, 6, 8),
		},
		{
			name:   "a group combined with correlation",
			query:  hmntsk.Query{Group: "finance-approvers", OwnerType: "invoice"},
			assert: matching(4, 8),
		},
		{
			name:   "a group ordered by urgency",
			query:  hmntsk.Query{Group: "finance-approvers", OrderBy: hmntsk.OrderUrgency},
			assert: matching(1, 5, 2, 4, 8, 6),
		},
		{
			name:  "a group no pool names matches nothing",
			query: hmntsk.Query{Group: "nobody"},
			assert: func(t *testing.T, page hmntsk.Page, err error) {
				require.NoError(t, err)
				assert.Empty(t, page.Tasks)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := h.Store.Query(t.Context(), hmntsk.ResolvedQuery{Query: tc.query})
			tc.assert(t, page, err)
		})
	}
}

// inboxCounts covers counting: every filter a query applies, each task once,
// and nothing about pages.
func inboxCounts(t *testing.T, h Harness, _ func(numbers ...int) []hmntsk.TaskID) {
	t.Helper()

	type testCase struct {
		name   string
		query  hmntsk.ResolvedQuery
		assert func(t *testing.T, count int64, err error)
	}

	counts := func(expected int64, msgAndArgs ...any) func(t *testing.T, count int64, err error) {
		return func(t *testing.T, count int64, err error) {
			require.NoError(t, err)
			assert.Equal(t, expected, count, msgAndArgs...)
		}
	}

	urgent, err := h.Store.Query(t.Context(), hmntsk.ResolvedQuery{
		Query: hmntsk.Query{Limit: 1, OrderBy: hmntsk.OrderUrgency},
	})
	require.NoError(t, err)
	require.NotEmpty(t, urgent.NextCursor)

	cases := []testCase{
		{
			name:   "an empty query counts every task",
			query:  hmntsk.ResolvedQuery{},
			assert: counts(8),
		},
		{
			name: "a count ignores the page size, the ordering and the cursor",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{
				Group: "finance-approvers", Limit: 1, OrderBy: hmntsk.OrderUrgency, Cursor: urgent.NextCursor,
			}},
			assert: counts(6),
		},
		{
			name: "a task within reach by name and by group is counted once",
			query: hmntsk.ResolvedQuery{
				Query:           hmntsk.Query{Candidate: Assignee},
				CandidateGroups: []string{"finance-approvers"},
			},
			assert: counts(6, "tasks 1, 2, 4, 5, 6 and 8, with task 6 matching twice over"),
		},
		{
			name:   "a count applies the group filter",
			query:  hmntsk.ResolvedQuery{Query: hmntsk.Query{Group: "managers"}},
			assert: counts(1),
		},
		{
			name:   "a count applies correlation and status filters",
			query:  hmntsk.ResolvedQuery{Query: hmntsk.Query{OwnerType: "process", Statuses: []hmntsk.Status{hmntsk.StatusReady}}},
			assert: counts(4),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			count, err := h.Store.Count(t.Context(), tc.query)
			tc.assert(t, count, err)
		})
	}
}

// inboxStablePaging pages under urgency ordering while more urgent work keeps
// arriving, which lands before the cursor, and must never make an earlier task
// come round again.
func inboxStablePaging(t *testing.T, factory Factory) {
	t.Helper()

	h := factory(t)
	seedInbox(t, h)

	arrivals := ids{n: 100}
	due := Reference.Add(time.Minute)

	seen := make(map[hmntsk.TaskID]int)
	query := hmntsk.ResolvedQuery{Query: hmntsk.Query{Limit: 2, OrderBy: hmntsk.OrderUrgency}}

	for pages := 0; ; pages++ {
		require.Less(t, pages, 50, "paging must terminate")

		page, err := h.Store.Query(t.Context(), query)
		require.NoError(t, err)

		for _, task := range page.Tasks {
			seen[task.ID]++
		}

		Seed(t, h, NewTask(arrivals.next(), func(task *hmntsk.Task) {
			task.Priority = hmntsk.PriorityHighest
			task.DueAt = &due
		}))

		if page.NextCursor == "" {
			break
		}

		query.Cursor = page.NextCursor
	}

	for id, count := range seen {
		assert.Equalf(t, 1, count, "task %s was returned on more than one page", id)
	}

	assert.GreaterOrEqual(t, len(seen), 8, "every task that existed at the start must be returned")
}

// taskIDs lists the identifiers of tasks, in order.
func taskIDs(tasks []hmntsk.Task) []hmntsk.TaskID {
	out := make([]hmntsk.TaskID, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}

	return out
}
