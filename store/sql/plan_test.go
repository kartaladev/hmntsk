package sqlstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
	"github.com/kartaladev/hmntsk/store/sqlcore"
	"github.com/kartaladev/hmntsk/storetest"
)

// TestQueryPlansAreServedByAnIndex checks that the queries the inbox and the
// escalation sweep run reach their rows through an index rather than by reading
// the table.
//
// The assertion is "an index is used", not "this particular index is used", and
// that is deliberate. Which index a planner picks is a function of statistics
// and of the dialect's own preferences — SQLite will happily serve the
// eligibility subquery from the candidates primary key, whose leading column is
// the task, while PostgreSQL prefers the lookup index, whose leading columns are
// the kind and the value. Both are correct and both are indexed. Asserting the
// name would pin an implementation detail of three planners; asserting that
// none of them falls back to a full scan of the candidates table pins the thing
// that would actually hurt.
func TestQueryPlansAreServedByAnIndex(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		driver  string
		dialect sqlcore.Dialect
		dsn     func(t *testing.T) string
	}

	cases := []testCase{
		{
			name: "sqlite", driver: "sqlite", dialect: sqlcore.SQLite,
			dsn: func(t *testing.T) string { return storetest.RunTestSQLite(t) },
		},
		{
			name: "postgres", driver: "postgres", dialect: sqlcore.PostgreSQL,
			dsn: func(t *testing.T) string { return storetest.RunTestPostgres(t) },
		},
		{
			name: "mysql", driver: "mysql", dialect: sqlcore.MySQL,
			dsn: func(t *testing.T) string { return storetest.RunTestMySQL(t) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := open(t, tc.driver, tc.dsn(t))

			prefix := storetest.NextTablePrefix()
			store := sqlstore.New(db, tc.dialect, sqlstore.WithTablePrefix(prefix))

			require.NoError(t, store.Migrate(t.Context()))

			seedForPlanning(t, store)
			analyze(t, db, tc.dialect, prefix)

			inbox := store.Builder().QueryTasks(hmntsk.ResolvedQuery{
				Query:           hmntsk.Query{Candidate: "alice", Limit: 25},
				CandidateGroups: []string{"finance-approvers", "managers"},
			})

			sweep := store.Builder().SelectOverdue(hmntsk.LeaseRequest{
				Now: storetest.Reference, Owner: "sweeper-1", Duration: time.Minute, Limit: 25,
			})

			t.Run("the eligibility subquery is served by an index", func(t *testing.T) {
				plan := explain(t, db, tc.dialect, inbox)
				t.Logf("inbox plan:\n%s", plan)

				assertIndexed(t, tc.dialect, plan,
					prefix+sqlcore.CandidatesTable, []string{"c", "x"})
			})

			t.Run("the sweep is served by an index", func(t *testing.T) {
				plan := explain(t, db, tc.dialect, sweep)
				t.Logf("sweep plan:\n%s", plan)

				assertIndexed(t, tc.dialect, plan, prefix+sqlcore.TasksTable, nil)
			})
		})
	}
}

// seedForPlanning inserts enough rows that a planner has something to plan
// against. A table of four rows is read whole by every engine, whatever indexes
// it carries, so a plan taken over one proves nothing.
func seedForPlanning(t *testing.T, store *sqlstore.Store) {
	t.Helper()

	const tasks = 400

	overdue := storetest.Reference.Add(-time.Hour)

	require.NoError(t, store.Do(t.Context(), func(ctx context.Context) error {
		for i := range tasks {
			id := hmntsk.TaskID(fmt.Sprintf("019243af-9f1c-7000-8000-%012d", i))

			task := storetest.NewTask(id, func(task *hmntsk.Task) {
				task.Candidates = hmntsk.CandidatePool{
					Users:  []string{fmt.Sprintf("user-%d", i%97)},
					Groups: []string{fmt.Sprintf("group-%d", i%13)},
				}

				if i%5 == 0 {
					task.DueAt = &overdue
				}
			})

			if err := store.Create(ctx, task); err != nil {
				return err
			}
		}

		return nil
	}))
}

// analyze refreshes the statistics a planner works from.
func analyze(t *testing.T, db *sql.DB, dialect sqlcore.Dialect, prefix string) {
	t.Helper()

	statements := []string{}

	switch dialect.Name() {
	case "postgres":
		statements = append(statements,
			`ANALYZE "`+prefix+`tasks"`, `ANALYZE "`+prefix+`task_candidates"`)
	case "mysql":
		statements = append(statements,
			"ANALYZE TABLE `"+prefix+"tasks`", "ANALYZE TABLE `"+prefix+"task_candidates`")
	case "sqlite":
		statements = append(statements, "ANALYZE")
	}

	for _, statement := range statements {
		_, err := db.ExecContext(t.Context(), statement)
		require.NoErrorf(t, err, "analyze: %s", statement)
	}
}

// explain renders a statement's plan, in whatever form the dialect offers.
//
// On PostgreSQL the plan is taken with sequential scans disabled for the
// statement. That does not make the assertion vacuous: disabling them lets the
// planner reach for an index but does not invent one, so a query with no usable
// index still comes back as a scan — PostgreSQL falls back rather than failing.
func explain(t *testing.T, db *sql.DB, dialect sqlcore.Dialect, statement sqlcore.Statement) string {
	t.Helper()

	prefixes := map[string]string{
		"postgres": "EXPLAIN ",
		"mysql":    "EXPLAIN ",
		"sqlite":   "EXPLAIN QUERY PLAN ",
	}

	query := prefixes[dialect.Name()] + statement.SQL

	if dialect.Name() != "postgres" {
		return readPlan(t, db, query, statement.Args)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(t.Context(), "SET LOCAL enable_seqscan = off")
	require.NoError(t, err)

	rows, err := tx.QueryContext(t.Context(), query, statement.Args...)
	require.NoError(t, err)

	return collectPlan(t, rows)
}

// readPlan runs an EXPLAIN and returns its output as text.
func readPlan(t *testing.T, db *sql.DB, query string, args []any) string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), query, args...)
	require.NoError(t, err)

	return collectPlan(t, rows)
}

// collectPlan flattens an EXPLAIN result set into one string, whatever shape it
// has: the three dialects return different column counts and different types.
func collectPlan(t *testing.T, rows *sql.Rows) string {
	t.Helper()

	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	require.NoError(t, err)

	var out strings.Builder

	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))

		for i := range values {
			dest[i] = &values[i]
		}

		require.NoError(t, rows.Scan(dest...))

		parts := make([]string, 0, len(values))

		for i, value := range values {
			text, decodeErr := sqlcore.DecodeString(value)
			if decodeErr != nil {
				text = fmt.Sprint(value)
			}

			parts = append(parts, columns[i]+"="+text)
		}

		out.WriteString(strings.Join(parts, " "))
		out.WriteString("\n")
	}

	require.NoError(t, rows.Err())

	return out.String()
}

// assertIndexed fails if the plan reaches a table by reading all of it.
//
// The three dialects say so three different ways, and two of them name the
// query's alias rather than the table, so both are accepted.
func assertIndexed(t *testing.T, dialect sqlcore.Dialect, plan, table string, aliases []string) {
	t.Helper()

	names := append([]string{table}, aliases...)

	switch dialect.Name() {
	case "postgres":
		assert.NotContainsf(t, plan, "Seq Scan on "+table,
			"the plan reads every row of %s:\n%s", table, plan)
	case "sqlite":
		// SQLite writes "SCAN <name>" for a full scan and "SEARCH <name> USING
		// ..." for an indexed one.
		for _, name := range names {
			assert.NotContainsf(t, plan, "SCAN "+name+" ",
				"the plan reads every row of %s:\n%s", name, plan)
			assert.NotContainsf(t, plan, "SCAN "+name+"\n",
				"the plan reads every row of %s:\n%s", name, plan)
		}
	case "mysql":
		for line := range strings.Lines(plan) {
			matched := false

			for _, name := range names {
				if strings.Contains(line, "table="+name+" ") {
					matched = true

					break
				}
			}

			if !matched {
				continue
			}

			assert.NotContainsf(t, line, "type=ALL", "the plan reads every row of %s: %s", table, line)
			assert.NotContainsf(t, line, "key=NULL", "the plan reaches %s without an index: %s", table, line)
		}
	}
}
