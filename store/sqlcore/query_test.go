package sqlcore_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

func TestQueryTasksFilters(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		query  hmntsk.ResolvedQuery
		assert func(t *testing.T, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name:  "an empty query filters on nothing",
			query: hmntsk.ResolvedQuery{},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.NotContains(t, statement.SQL, "WHERE")
				require.Len(t, statement.Args, 1, "only the page size is bound")
				assert.Equal(t, int64(hmntsk.DefaultQueryLimit+1), statement.Args[0])
			},
		},
		{
			name:  "an assignee filter is a column comparison",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Assignee: "alice", Limit: 10}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `WHERE "t"."assignee" = $1`)
				assert.Equal(t, "alice", statement.Args[0])
				assert.Equal(t, int64(11), statement.Args[1],
					"one row beyond the page, so the caller can tell whether another exists")
			},
		},
		{
			name: "a correlation lookup inspects no payload",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{
				OwnerType: "process", OwnerRef: "p-1", ActivityKey: "approve",
			}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `"t"."owner_type" = $1`)
				assert.Contains(t, statement.SQL, `"t"."owner_ref" = $2`)
				assert.Contains(t, statement.SQL, `"t"."activity_key" = $3`)
				assert.Equal(t, []any{"process", "p-1", "approve", int64(51)}, statement.Args,
					"correlation filters must bind in a fixed order, or ? placeholders shuffle")
				_, where, found := strings.Cut(statement.SQL, " WHERE ")
				require.True(t, found)
				assert.NotContains(t, where, "input")
				assert.NotContains(t, where, "output")
				assert.NotContains(t, where, "progress")
			},
		},
		{
			name: "statuses and types become IN lists",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{
				Statuses: []hmntsk.Status{hmntsk.StatusReady, hmntsk.StatusReserved},
				Types:    []string{"approval"},
			}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `"t"."status" IN ($1, $2)`)
				assert.Contains(t, statement.SQL, `"t"."task_type" IN ($3)`)
				assert.Equal(t, []any{"READY", "RESERVED", "approval", int64(51)}, statement.Args)
			},
		},
		{
			name: "an eligibility filter reaches the child table, never an array",
			query: hmntsk.ResolvedQuery{
				Query:           hmntsk.Query{Candidate: "alice"},
				CandidateGroups: []string{"finance-approvers", "managers"},
			},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `EXISTS (SELECT 1 FROM "task_candidates"`)
				assert.Contains(t, statement.SQL, `NOT EXISTS (SELECT 1 FROM "task_candidates"`)
				assert.Contains(t, statement.SQL, `"t"."assignee" IS NULL`,
					"pooled work is what a candidate may claim")
				assert.Contains(t, statement.SQL, `"c"."kind" = $1 AND "c"."value" = $2`)
				assert.Contains(t, statement.SQL, `"c"."value" IN ($4, $5)`)
				assert.Equal(t, []any{
					"user", "alice", "group", "finance-approvers", "managers",
					"alice", "excluded", "alice", int64(51),
				}, statement.Args)
			},
		},
		{
			name:  "a candidate with no groups omits the group disjunct entirely",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Candidate: "alice"}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.NotContains(t, statement.SQL, `"c"."kind" = $3`)
				assert.Equal(t, []any{"user", "alice", "alice", "excluded", "alice", int64(51)},
					statement.Args)
			},
		},
		{
			name:  "a due-before filter excludes tasks with no deadline",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{DueBefore: &reference}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `"t"."due_at" IS NOT NULL`)
				assert.Contains(t, statement.SQL, `"t"."due_at" < $1`)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlcore.New(sqlcore.PostgreSQL).QueryTasks(tc.query))
		})
	}
}

func TestQueryTasksPaginationIsDeterministicAcrossDialects(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		query  hmntsk.ResolvedQuery
		assert func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name:  "the default order is by identifier ascending",
			query: hmntsk.ResolvedQuery{},
			assert: func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL,
					"ORDER BY "+dialect.Quote("t")+"."+dialect.Quote("id")+" ASC")
			},
		},
		{
			name:  "descending reverses it",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Descending: true}},
			assert: func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL,
					"ORDER BY "+dialect.Quote("t")+"."+dialect.Quote("id")+" DESC")
			},
		},
		{
			name:  "a cursor continues strictly beyond the last identifier returned",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Cursor: "task-7"}},
			assert: func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, dialect.Quote("t")+"."+dialect.Quote("id")+" > ")
				assert.Contains(t, statement.Args, "task-7")
			},
		},
		{
			name:  "a descending cursor walks the other way",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Cursor: "task-7", Descending: true}},
			assert: func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, dialect.Quote("t")+"."+dialect.Quote("id")+" < ")
			},
		},
		{
			name:  "an excessive page size is capped",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Limit: 100000}},
			assert: func(t *testing.T, _ sqlcore.Dialect, statement sqlcore.Statement) {
				assert.Contains(t, statement.Args, int64(hmntsk.MaxQueryLimit+1))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, dialect := range sqlcore.Dialects() {
				t.Run(dialect.Name(), func(t *testing.T) {
					t.Parallel()

					statement := sqlcore.New(dialect).QueryTasks(tc.query)

					assert.NotContains(t, statement.SQL, "OFFSET",
						"offset paging repeats and skips rows while tasks are being created")

					tc.assert(t, dialect, statement)
				})
			}
		})
	}
}

func TestSelectOverdueAndClaimLeaseShareOnePredicate(t *testing.T) {
	t.Parallel()

	lease := hmntsk.LeaseRequest{
		Now: reference, Owner: "sweeper-1", Duration: time.Minute, Limit: 10,
	}

	for _, dialect := range sqlcore.Dialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			t.Parallel()

			b := sqlcore.New(dialect)

			selection := b.SelectOverdue(lease)
			claim := b.ClaimLease("task-1", lease)

			for _, fragment := range []string{
				dialect.Quote("due_at") + " IS NOT NULL",
				dialect.Quote("status") + " IN (",
				dialect.Quote("locked_until") + " IS NULL OR ",
			} {
				assert.Containsf(t, selection.SQL, fragment, "selection: %s", selection.SQL)
				assert.Containsf(t, claim.SQL, fragment,
					"the claim must repeat the whole predicate, or two sweepers racing on "+
						"one row could both win: %s", claim.SQL)
			}

			assert.Contains(t, claim.SQL, dialect.Quote("locked_by")+" = ")
			assert.NotContains(t, claim.SQL, dialect.Quote("version")+" =",
				"a lease is sweeper bookkeeping and must not advance the task's version")

			if dialect.SupportsSkipLocked() {
				assert.Contains(t, selection.SQL, "FOR UPDATE SKIP LOCKED")
			} else {
				assert.NotContains(t, selection.SQL, "FOR UPDATE",
					"SQLite has no row-level locking, so the lease is the whole mechanism")
			}
		})
	}
}

func TestSelectOverdueExcludesTerminalAndSuspendedTasks(t *testing.T) {
	t.Parallel()

	statement := sqlcore.New(sqlcore.PostgreSQL).
		SelectOverdue(hmntsk.LeaseRequest{Now: reference, Duration: time.Minute})

	statuses := make([]string, 0, 3)

	for _, arg := range statement.Args {
		if text, ok := arg.(string); ok {
			statuses = append(statuses, text)
		}
	}

	assert.Equal(t, []string{"READY", "RESERVED", "IN_PROGRESS"}, statuses,
		"a suspended task is never escalated, and a terminal one cannot be")
	assert.NotContains(t, strings.Join(statuses, ","), "SUSPENDED")
	assert.NotContains(t, strings.Join(statuses, ","), "COMPLETED")
}
