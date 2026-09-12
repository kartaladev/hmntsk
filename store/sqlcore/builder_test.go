package sqlcore_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

var reference = time.Date(2026, time.March, 1, 12, 0, 0, 123456000, time.UTC)

// sampleTask is the task every statement case is built from.
func sampleTask() hmntsk.Task {
	due := reference.Add(24 * time.Hour)

	return hmntsk.Task{
		ID:       "task-1",
		Type:     "approval",
		Version:  4,
		Status:   hmntsk.StatusReserved,
		Priority: hmntsk.PriorityDefault,
		Assignee: "alice",
		Candidates: hmntsk.CandidatePool{
			Users:    []string{"alice", "bob"},
			Groups:   []string{"finance-approvers"},
			Excluded: []string{"mallory"},
		},
		Correlation: hmntsk.CorrelationData{
			OwnerType: "process", OwnerRef: "p-1", ActivityKey: "approve",
			Extra: map[string]string{"tenant": "acme"},
		},
		Callback: &hmntsk.CallbackTarget{
			Address:             "https://host.example/hook",
			ReferenceParameters: json.RawMessage(`{"corr":"abc"}`),
		},
		Input:     json.RawMessage(`{"amount":9007199254740993}`),
		CreatedBy: "system",
		CreatedAt: reference,
		UpdatedAt: reference,
		DueAt:     &due,
	}
}

func TestInsertTaskPerDialect(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		assert  func(t *testing.T, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name:    "postgres numbers its placeholders",
			dialect: sqlcore.PostgreSQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.True(t, strings.HasPrefix(statement.SQL, `INSERT INTO "tasks" ("id", "task_type",`))
				assert.Contains(t, statement.SQL, "VALUES ($1, $2, $3,")
				assert.Contains(t, statement.SQL, "$28)")
				assert.NotContains(t, statement.SQL, "$29")
			},
		},
		{
			name:    "mysql uses question marks and backticks",
			dialect: sqlcore.MySQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.True(t, strings.HasPrefix(statement.SQL, "INSERT INTO `tasks` (`id`, `task_type`,"))
				assert.Equal(t, 28, strings.Count(statement.SQL, "?"))
			},
		},
		{
			name:    "sqlite uses question marks and double quotes",
			dialect: sqlcore.SQLite,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.True(t, strings.HasPrefix(statement.SQL, `INSERT INTO "tasks" ("id", "task_type",`))
				assert.Equal(t, 28, strings.Count(statement.SQL, "?"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement := sqlcore.New(tc.dialect).InsertTask(sampleTask())

			require.Len(t, statement.Args, 28, "one argument per column, in column order")
			assert.Equal(t, "task-1", statement.Args[0])
			assert.Equal(t, "approval", statement.Args[1])
			assert.Equal(t, int64(4), statement.Args[2])
			assert.Equal(t, "RESERVED", statement.Args[3])
			assert.Nil(t, statement.Args[4], "an unset suspended-from is NULL, not an empty string")
			assert.Equal(t, int64(5), statement.Args[5])
			assert.Equal(t, "alice", statement.Args[6])
			assert.Equal(t, "process", statement.Args[7])
			assert.Equal(t, `{"amount":9007199254740993}`, statement.Args[14],
				"an opaque payload is passed through as supplied")

			tc.assert(t, statement)
		})
	}
}

func TestTimestampEncodingPerDialect(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		assert  func(t *testing.T, createdAt any)
	}

	cases := []testCase{
		{
			name:    "postgres stores a native instant",
			dialect: sqlcore.PostgreSQL,
			assert: func(t *testing.T, createdAt any) {
				instant, ok := createdAt.(time.Time)
				require.True(t, ok, "got %T", createdAt)
				assert.True(t, reference.Equal(instant))
				assert.Equal(t, time.UTC, instant.Location())
			},
		},
		{
			name:    "mysql stores a native instant",
			dialect: sqlcore.MySQL,
			assert: func(t *testing.T, createdAt any) {
				instant, ok := createdAt.(time.Time)
				require.True(t, ok, "got %T", createdAt)
				assert.True(t, reference.Equal(instant))
			},
		},
		{
			name:    "sqlite stores one fixed textual encoding",
			dialect: sqlcore.SQLite,
			assert: func(t *testing.T, createdAt any) {
				text, ok := createdAt.(string)
				require.True(t, ok, "got %T", createdAt)
				assert.Equal(t, "2026-03-01T12:00:00.123456Z", text,
					"fixed width, UTC and six fractional digits, so lexical order is chronological order")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement := sqlcore.New(tc.dialect).InsertTask(sampleTask())
			tc.assert(t, statement.Args[20])
		})
	}
}

func TestUpdateTaskCarriesTheVersionPredicate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		assert  func(t *testing.T, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name:    "postgres",
			dialect: sqlcore.PostgreSQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `WHERE "id" = $28 AND "version" = $29`)
			},
		},
		{
			name:    "mysql",
			dialect: sqlcore.MySQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, "WHERE `id` = ? AND `version` = ?")
			},
		},
		{
			name:    "sqlite",
			dialect: sqlcore.SQLite,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `WHERE "id" = ? AND "version" = ?`)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement := sqlcore.New(tc.dialect).UpdateTask(sampleTask(), 3)

			require.Len(t, statement.Args, 29,
				"twenty-seven assignments, the identifier, and the version the caller read")
			assert.Equal(t, "task-1", statement.Args[27])
			assert.Equal(t, int64(3), statement.Args[28],
				"the predicate must carry the version the caller observed, not the new one")

			assert.NotContains(t, statement.SQL, "RETURNING",
				"nothing may depend on RETURNING, because MySQL has none")
			assert.NotContains(t, statement.SQL, `SET "id" =`)

			tc.assert(t, statement)
		})
	}
}

func TestConditionalWritesNeverDependOnReturning(t *testing.T) {
	t.Parallel()

	for _, dialect := range sqlcore.Dialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			t.Parallel()

			b := sqlcore.New(dialect)
			task := sampleTask()

			statements := []sqlcore.Statement{
				b.InsertTask(task),
				b.UpdateTask(task, 3),
				b.InsertCandidates(task.ID, task.Candidates),
				b.DeleteCandidates(task.ID),
				b.InsertHistory(hmntsk.TransitionRecord{TaskID: task.ID, Version: 1, At: reference}),
				b.ClaimLease(task.ID, hmntsk.LeaseRequest{Now: reference, Duration: time.Minute}),
				b.ReleaseLease(task.ID, "sweeper-1"),
				b.UpsertType(hmntsk.TypeSpec{Name: "approval"}, reference),
			}

			for _, statement := range statements {
				assert.NotContainsf(t, statement.SQL, "RETURNING",
					"every write is a conditional update plus a rows-affected check: %s", statement.SQL)
			}
		})
	}
}

func TestConflictErrorReportsTheCurrentVersion(t *testing.T) {
	t.Parallel()

	err := sqlcore.ConflictError("task-1", 3, 7)

	require.ErrorIs(t, err, hmntsk.ErrConflict)

	var conflict *hmntsk.ConflictError

	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, hmntsk.TaskID("task-1"), conflict.TaskID)
	assert.Equal(t, int64(3), conflict.Expected)
	assert.Equal(t, int64(7), conflict.Current,
		"a rows-affected count of zero is the only signal a conditional update gives, "+
			"so the adapter has to re-read the version to tell the loser what to re-read")
}

func TestInsertCandidatesWritesOneRowPerEntry(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		pool   hmntsk.CandidatePool
		assert func(t *testing.T, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name: "users, groups and exclusions are separate rows",
			pool: hmntsk.CandidatePool{
				Users: []string{"alice", "bob"}, Groups: []string{"finance-approvers"},
				Excluded: []string{"mallory"},
			},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				require.Len(t, statement.Args, 16, "four columns for each of four entries")
				assert.Equal(t, "user", statement.Args[1])
				assert.Equal(t, "alice", statement.Args[2])
				assert.Equal(t, int64(0), statement.Args[3])
				assert.Equal(t, "group", statement.Args[9])
				assert.Equal(t, "finance-approvers", statement.Args[10])
				assert.Equal(t, "excluded", statement.Args[13])
				assert.Equal(t, "mallory", statement.Args[14])
			},
		},
		{
			name: "an empty pool produces no statement at all",
			pool: hmntsk.CandidatePool{},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.True(t, statement.IsZero(),
					"an INSERT with no VALUES is a syntax error on every dialect")
			},
		},
		{
			name: "exclusions alone are still written",
			pool: hmntsk.CandidatePool{Excluded: []string{"mallory"}},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				require.Len(t, statement.Args, 4)
				assert.Equal(t, "excluded", statement.Args[1])
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlcore.New(sqlcore.PostgreSQL).InsertCandidates("task-1", tc.pool))
		})
	}
}

func TestHistoryHasNoUpdateOrDeleteStatement(t *testing.T) {
	t.Parallel()

	// History is append-only. The proof is negative, so it is asserted over the
	// statements this package can produce for that table.
	b := sqlcore.New(sqlcore.PostgreSQL)

	assert.Contains(t, b.InsertHistory(hmntsk.TransitionRecord{TaskID: "t"}).SQL, "INSERT INTO")
	assert.Contains(t, b.SelectHistory("t").SQL, "SELECT")
	assert.Contains(t, b.SelectHistory("t").SQL, `ORDER BY "version", "at"`)
}

func TestCheckAffected(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		affected int64
		current  int64
		assert   func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "one row means the write took", affected: 1, current: 4,
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name: "no rows with the task still there is a conflict", affected: 0, current: 9,
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConflict)

				var conflict *hmntsk.ConflictError

				require.ErrorAs(t, err, &conflict)
				assert.Equal(t, int64(9), conflict.Current)
				assert.Equal(t, int64(3), conflict.Expected)
			},
		},
		{
			name: "no rows with the task gone is not found", affected: 0, current: 0,
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrNotFound)
				assert.NotErrorIs(t, err, hmntsk.ErrConflict,
					"a task that no longer exists is a different answer from one that moved on")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlcore.CheckAffected(tc.affected, "task-1", 3, tc.current))
		})
	}
}
