package sqlcore_test

import (
	"encoding/base64"
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
				assert.Contains(t, statement.SQL, `"t"."assignee" = $1`)
				assert.Contains(t, statement.SQL, `"c"."kind" = $2 AND "c"."value" = $3`)
				assert.Contains(t, statement.SQL, `"c"."value" IN ($5, $6)`)
				assert.Equal(t, []any{
					"alice", "user", "alice", "group", "finance-approvers", "managers",
					"excluded", "alice", int64(51),
				}, statement.Args)
			},
		},
		{
			name:  "a candidate with no groups omits the group disjunct entirely",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Candidate: "alice"}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.NotContains(t, statement.SQL, `"c"."kind" = $4`)
				assert.Equal(t, []any{"alice", "user", "alice", "excluded", "alice", int64(51)},
					statement.Args)
			},
		},
		{
			name:  "a group filter is the pool as configured, with no membership resolved",
			query: hmntsk.ResolvedQuery{Query: hmntsk.Query{Group: "finance-approvers"}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL,
					`EXISTS (SELECT 1 FROM "task_candidates" AS "g" WHERE "g"."task_id" = "t"."id"`+
						` AND "g"."kind" = $1 AND "g"."value" = $2)`)
				assert.NotContains(t, statement.SQL, `"t"."assignee" IS NULL`,
					"a team queue lists held work as well as pooled work")
				assert.NotContains(t, statement.SQL, "NOT EXISTS",
					"a team queue has no actor, so no exclusion applies")
				assert.Equal(t, []any{"group", "finance-approvers", int64(51)}, statement.Args)
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

			tc.assert(t, mustStatement(t)(sqlcore.New(sqlcore.PostgreSQL).QueryTasks(tc.query)))
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

					statement := mustStatement(t)(sqlcore.New(dialect).QueryTasks(tc.query))

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

func TestQueryTasksOrderingsAcrossDialects(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		query  hmntsk.Query
		assert func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement)
	}

	// orderedBy asserts the ORDER BY clause. Each term names a column of the
	// tasks alias and a direction; "no-deadline" names the flag expression.
	orderedBy := func(terms ...string) func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement) {
		return func(t *testing.T, dialect sqlcore.Dialect, statement sqlcore.Statement) {
			rendered := make([]string, 0, len(terms))

			for _, term := range terms {
				name, direction, _ := strings.Cut(term, " ")

				expression := dialect.Quote("t") + "." + dialect.Quote(name)
				if name == "no-deadline" {
					expression = "CASE WHEN " + dialect.Quote("t") + "." + dialect.Quote("due_at") +
						" IS NULL THEN 1 ELSE 0 END"
				}

				rendered = append(rendered, expression+" "+direction)
			}

			assert.Contains(t, statement.SQL, " ORDER BY "+strings.Join(rendered, ", ")+" LIMIT ")
		}
	}

	cases := []testCase{
		{
			name:   "creation order is the identifier alone",
			query:  hmntsk.Query{},
			assert: orderedBy("id ASC"),
		},
		{
			name:   "priority, then creation",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderPriority},
			assert: orderedBy("priority ASC", "id ASC"),
		},
		{
			name:   "priority descending reverses the whole key",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderPriority, Descending: true},
			assert: orderedBy("priority DESC", "id DESC"),
		},
		{
			name:   "due date sorts tasks without a deadline last with a flag every dialect orders alike",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderDue},
			assert: orderedBy("no-deadline ASC", "due_at ASC", "id ASC"),
		},
		{
			name:   "due date descending still sorts tasks without a deadline last",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderDue, Descending: true},
			assert: orderedBy("no-deadline ASC", "due_at DESC", "id DESC"),
		},
		{
			name:   "urgency is priority, then due date with no deadline last, then creation",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderUrgency},
			assert: orderedBy("priority ASC", "no-deadline ASC", "due_at ASC", "id ASC"),
		},
		{
			name:   "urgency descending keeps tasks without a deadline last within a priority",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderUrgency, Descending: true},
			assert: orderedBy("priority DESC", "no-deadline ASC", "due_at DESC", "id DESC"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, dialect := range sqlcore.Dialects() {
				t.Run(dialect.Name(), func(t *testing.T) {
					t.Parallel()

					statement := mustStatement(t)(sqlcore.New(dialect).QueryTasks(
						hmntsk.ResolvedQuery{Query: tc.query}))
					tc.assert(t, dialect, statement)
				})
			}
		})
	}
}

func TestQueryTasksContinuesWithAnExpandedKeysetPredicate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name  string
		query hmntsk.Query
		// last is the task the previous page ended with.
		last   hmntsk.Task
		assert func(t *testing.T, statement sqlcore.Statement)
	}

	// continuesWith asserts the whole WHERE clause and the bindings that are
	// not instants; instants are compared separately, by value.
	continuesWith := func(where string, args ...any) func(t *testing.T, statement sqlcore.Statement) {
		return func(t *testing.T, statement sqlcore.Statement) {
			assert.Contains(t, statement.SQL, " WHERE "+where+" ORDER BY ")
			require.Len(t, statement.Args, len(args))

			for i, arg := range args {
				if arg == nil {
					instant, ok := statement.Args[i].(time.Time)
					require.Truef(t, ok, "argument %d is %T, not an instant", i, statement.Args[i])
					assert.True(t, reference.Equal(instant))

					continue
				}

				assert.Equalf(t, arg, statement.Args[i], "argument %d", i)
			}
		}
	}

	cases := []testCase{
		{
			name:   "creation order continues beyond the last identifier",
			query:  hmntsk.Query{},
			last:   hmntsk.Task{ID: "task-7", Priority: 1, DueAt: &reference},
			assert: continuesWith(`("t"."id" > $1)`, "task-7", int64(51)),
		},
		{
			name:   "descending creation order walks the other way",
			query:  hmntsk.Query{Descending: true},
			last:   hmntsk.Task{ID: "task-7", Priority: 1, DueAt: &reference},
			assert: continuesWith(`("t"."id" < $1)`, "task-7", int64(51)),
		},
		{
			name:  "priority breaks a tie by identifier",
			query: hmntsk.Query{OrderBy: hmntsk.OrderPriority},
			last:  hmntsk.Task{ID: "task-7", Priority: 1},
			assert: continuesWith(
				`(("t"."priority" > $1) OR ("t"."priority" = $2 AND "t"."id" > $3))`,
				int64(1), int64(1), "task-7", int64(51)),
		},
		{
			name:  "urgency compares the whole key, and a deadline comes before none",
			query: hmntsk.Query{OrderBy: hmntsk.OrderUrgency},
			last:  hmntsk.Task{ID: "task-7", Priority: 1, DueAt: &reference},
			assert: continuesWith(
				`(("t"."priority" > $1)`+
					` OR ("t"."priority" = $2 AND "t"."due_at" IS NULL)`+
					` OR ("t"."priority" = $3 AND "t"."due_at" IS NOT NULL AND "t"."due_at" > $4)`+
					` OR ("t"."priority" = $5 AND "t"."due_at" IS NOT NULL AND "t"."due_at" = $6`+
					` AND "t"."id" > $7))`,
				int64(1), int64(1), int64(1), nil, int64(1), nil, "task-7", int64(51)),
		},
		{
			name:  "urgency after a task with no deadline has no deadline to compare",
			query: hmntsk.Query{OrderBy: hmntsk.OrderUrgency},
			last:  hmntsk.Task{ID: "task-7", Priority: 1},
			assert: continuesWith(
				`(("t"."priority" > $1) OR ("t"."priority" = $2 AND "t"."due_at" IS NULL AND "t"."id" > $3))`,
				int64(1), int64(1), "task-7", int64(51)),
		},
		{
			name:  "descending due date reverses the comparisons but not where no deadline sorts",
			query: hmntsk.Query{OrderBy: hmntsk.OrderDue, Descending: true},
			last:  hmntsk.Task{ID: "task-7", DueAt: &reference},
			assert: continuesWith(
				`(("t"."due_at" IS NULL)`+
					` OR ("t"."due_at" IS NOT NULL AND "t"."due_at" < $1)`+
					` OR ("t"."due_at" IS NOT NULL AND "t"."due_at" = $2 AND "t"."id" < $3))`,
				nil, nil, "task-7", int64(51)),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := sqlcore.New(sqlcore.PostgreSQL)

			query := tc.query
			query.Cursor = b.NextCursor(tc.query, tc.last)

			statement := mustStatement(t)(b.QueryTasks(hmntsk.ResolvedQuery{Query: query}))

			assert.NotRegexp(t, `\) [<>] \(`, statement.SQL,
				"a row-value comparison is not used reliably with mixed directions on MySQL and SQLite")
			tc.assert(t, statement)
		})
	}
}

func TestQueryTasksRefusesACursorItCannotContinue(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		query  hmntsk.Query
		assert func(t *testing.T, statement sqlcore.Statement, err error)
	}

	b := sqlcore.New(sqlcore.PostgreSQL)
	created := b.NextCursor(hmntsk.Query{}, hmntsk.Task{ID: "task-7", Priority: 1, DueAt: &reference})

	refused := func(t *testing.T, statement sqlcore.Statement, err error) {
		require.ErrorIs(t, err, hmntsk.ErrValidation)
		assert.True(t, statement.IsZero(), "nothing is rendered for a query that cannot run")
	}

	cases := []testCase{
		{
			name:  "a cursor continues the ordering and direction that issued it",
			query: hmntsk.Query{Cursor: created},
			assert: func(t *testing.T, statement sqlcore.Statement, err error) {
				require.NoError(t, err)
				assert.False(t, statement.IsZero())
			},
		},
		{
			name:   "a cursor from another ordering is refused",
			query:  hmntsk.Query{OrderBy: hmntsk.OrderPriority, Cursor: created},
			assert: refused,
		},
		{
			name:   "a cursor from the other direction is refused",
			query:  hmntsk.Query{Descending: true, Cursor: created},
			assert: refused,
		},
		{
			name:   "a bare identifier is not a cursor",
			query:  hmntsk.Query{Cursor: "task-7"},
			assert: refused,
		},
		{
			name:   "well-formed JSON that is not a cursor is refused",
			query:  hmntsk.Query{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"x":1}`))},
			assert: refused,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := b.QueryTasks(hmntsk.ResolvedQuery{Query: tc.query})
			tc.assert(t, statement, err)
		})
	}
}

func TestCountTasks(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name  string
		query hmntsk.ResolvedQuery
		// assert receives the count and the page statement for the same query
		// without its cursor, so the two can be compared.
		assert func(t *testing.T, count, page sqlcore.Statement)
	}

	b := sqlcore.New(sqlcore.PostgreSQL)
	urgent := hmntsk.Query{OrderBy: hmntsk.OrderUrgency, Limit: 5}
	urgent.Cursor = b.NextCursor(urgent, hmntsk.Task{ID: "task-7", Priority: 1, DueAt: &reference})

	cases := []testCase{
		{
			name:  "a count reads the tasks table alone, unordered and unpaged, whatever the query asks for",
			query: hmntsk.ResolvedQuery{Query: urgent},
			assert: func(t *testing.T, count, _ sqlcore.Statement) {
				assert.True(t, strings.HasPrefix(count.SQL,
					`SELECT COUNT(*) FROM "tasks" AS "t"`), count.SQL)
				assert.NotContains(t, count.SQL, "JOIN",
					"filters are EXISTS subqueries, so no task can be counted twice")
				assert.NotContains(t, count.SQL, "ORDER BY")
				assert.NotContains(t, count.SQL, "LIMIT")
				assert.NotContains(t, count.SQL, "WHERE", "the cursor is not a filter")
				assert.Empty(t, count.Args)
			},
		},
		{
			name: "a count applies exactly the filters the query applies",
			query: hmntsk.ResolvedQuery{
				Query: hmntsk.Query{
					Assignee: "alice", Candidate: "alice", Group: "finance-approvers",
					Statuses:  []hmntsk.Status{hmntsk.StatusReady},
					Types:     []string{"approval"},
					OwnerType: "process", OwnerRef: "p-1", ActivityKey: "approve",
					DueBefore: &reference,
				},
				CandidateGroups: []string{"finance-approvers", "managers"},
			},
			assert: func(t *testing.T, count, page sqlcore.Statement) {
				_, countWhere, found := strings.Cut(count.SQL, " WHERE ")
				require.Truef(t, found, "the count filters nothing: %s", count.SQL)

				_, pageWhere, found := strings.Cut(page.SQL, " WHERE ")
				require.True(t, found)

				pageWhere, _, _ = strings.Cut(pageWhere, " ORDER BY ")

				assert.Equal(t, pageWhere, countWhere)
				assert.Equal(t, page.Args[:len(page.Args)-1], count.Args,
					"the same bindings as the query, less its page size")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			uncursored := tc.query
			uncursored.Cursor = ""

			tc.assert(t, b.CountTasks(tc.query), mustStatement(t)(b.QueryTasks(uncursored)))
		})
	}
}

// mustStatement unwraps a statement the test expects to render without error.
func mustStatement(t *testing.T) func(statement sqlcore.Statement, err error) sqlcore.Statement {
	t.Helper()

	return func(statement sqlcore.Statement, err error) sqlcore.Statement {
		t.Helper()
		require.NoError(t, err)

		return statement
	}
}
