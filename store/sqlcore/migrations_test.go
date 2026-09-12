package sqlcore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// recordingExecer captures the statements a migration run would execute.
type recordingExecer struct{ statements []string }

func (e *recordingExecer) ExecStatement(_ context.Context, sql string, _ ...any) error {
	e.statements = append(e.statements, sql)

	return nil
}

func TestMigrationsAreCompletePerDialect(t *testing.T) {
	t.Parallel()

	for _, dialect := range sqlcore.Dialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			t.Parallel()

			statements, err := sqlcore.Migrations(dialect)
			require.NoError(t, err)
			require.NotEmpty(t, statements)

			joined := strings.Join(statements, "\n")

			for _, table := range []string{
				sqlcore.TasksTable, sqlcore.CandidatesTable,
				sqlcore.HistoryTable, sqlcore.OutboxTable, sqlcore.TypesTable,
			} {
				assert.Containsf(t, joined, "CREATE TABLE IF NOT EXISTS "+dialect.Quote(table),
					"the published schema must create %s", table)
			}

			for _, statement := range statements {
				assert.NotContains(t, statement, "{{PREFIX}}",
					"every prefix token must be substituted, even when the prefix is empty")
				assert.NotContains(t, statement, "--", "comments are stripped from executable statements")
				assert.NotEmpty(t, strings.TrimSpace(statement))
			}

			assert.Contains(t, joined, dialect.IdentifierCollation(),
				"identifier columns must pin the collation that keeps comparison case-sensitive")
		})
	}
}

func TestMigrationsCoverEveryColumnTheStatementsUse(t *testing.T) {
	t.Parallel()

	for _, dialect := range sqlcore.Dialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			t.Parallel()

			b := sqlcore.New(dialect)

			statements, err := b.Migrations()
			require.NoError(t, err)

			joined := strings.Join(statements, "\n")

			for _, column := range b.TaskColumns() {
				assert.Containsf(t, joined, dialect.Quote(column),
					"the tasks table must declare %s, which every read and write names", column)
			}

			for _, column := range b.HistoryColumns() {
				assert.Containsf(t, joined, dialect.Quote(column), "history column %s", column)
			}
		})
	}
}

func TestMigrationsApplyTheTablePrefix(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		prefix string
		assert func(t *testing.T, statements []string)
	}

	cases := []testCase{
		{
			name:   "no prefix leaves the names bare",
			prefix: "",
			assert: func(t *testing.T, statements []string) {
				assert.Contains(t, strings.Join(statements, "\n"), `CREATE TABLE IF NOT EXISTS "tasks"`)
			},
		},
		{
			name:   "a prefix reaches tables, indexes and foreign keys alike",
			prefix: "hmntsk_",
			assert: func(t *testing.T, statements []string) {
				joined := strings.Join(statements, "\n")

				assert.Contains(t, joined, `CREATE TABLE IF NOT EXISTS "hmntsk_tasks"`)
				assert.Contains(t, joined, `CREATE TABLE IF NOT EXISTS "hmntsk_task_candidates"`)
				assert.Contains(t, joined, `REFERENCES "hmntsk_tasks"`)
				assert.Contains(t, joined, `"hmntsk_task_candidates_lookup_idx"`)
				assert.NotContains(t, joined, `"tasks"`,
					"an unprefixed name left behind would read the wrong table")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statements, err := sqlcore.New(sqlcore.SQLite, sqlcore.WithTablePrefix(tc.prefix)).Migrations()
			require.NoError(t, err)

			tc.assert(t, statements)
		})
	}
}

func TestTablePrefixReachesEveryStatement(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.PostgreSQL, sqlcore.WithTablePrefix("hmntsk_"))

	task := sampleTask()

	statements := map[string]sqlcore.Statement{
		"InsertTask":       b.InsertTask(task),
		"UpdateTask":       b.UpdateTask(task, 3),
		"SelectTask":       b.SelectTask(task.ID),
		"DeleteTask":       b.DeleteTask(task.ID),
		"InsertCandidates": b.InsertCandidates(task.ID, task.Candidates),
		"DeleteCandidates": b.DeleteCandidates(task.ID),
		"SelectCandidates": b.SelectCandidates(task.ID),
		"SelectHistory":    b.SelectHistory(task.ID),
		"SelectOutbox":     b.SelectOutbox(10),
		"SelectTypes":      b.SelectTypes(),
	}

	for name, statement := range statements {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Contains(t, statement.SQL, `"hmntsk_`)
			assert.NotContains(t, statement.SQL, ` "tasks"`)
			assert.NotContains(t, statement.SQL, ` "task_candidates"`)
		})
	}

	assert.Equal(t, "hmntsk_", b.Prefix())
	assert.Equal(t, []string{
		"hmntsk_tasks", "hmntsk_task_candidates", "hmntsk_task_history",
		"hmntsk_task_outbox", "hmntsk_task_types",
	}, b.Tables())
}

func TestMigrateRunsEveryStatementInOrder(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.SQLite, sqlcore.WithTablePrefix("t_"))

	execer := &recordingExecer{}
	require.NoError(t, b.Migrate(t.Context(), execer))

	expected, err := b.Migrations()
	require.NoError(t, err)

	assert.Equal(t, expected, execer.statements)

	// The tasks table must exist before anything references it.
	tasksAt := -1
	candidatesAt := -1

	for i, statement := range execer.statements {
		if strings.Contains(statement, `CREATE TABLE IF NOT EXISTS "t_tasks"`) {
			tasksAt = i
		}

		if strings.Contains(statement, `CREATE TABLE IF NOT EXISTS "t_task_candidates"`) {
			candidatesAt = i
		}
	}

	require.NotEqual(t, -1, tasksAt)
	require.NotEqual(t, -1, candidatesAt)
	assert.Less(t, tasksAt, candidatesAt, "a foreign key cannot precede the table it references")
}

func TestMigrationsSourceIsOneDocument(t *testing.T) {
	t.Parallel()

	source, err := sqlcore.New(sqlcore.MySQL, sqlcore.WithTablePrefix("t_")).MigrationsSource()
	require.NoError(t, err)

	assert.Contains(t, source, "-- MySQL schema for the hmntsk engine",
		"the document a host pastes into a migration file keeps its explanation")
	assert.Contains(t, source, "`t_tasks`")
	assert.NotContains(t, source, "{{PREFIX}}")
}

func TestDropRemovesTablesInReverseOrder(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.PostgreSQL)

	execer := &recordingExecer{}
	require.NoError(t, b.Drop(t.Context(), execer))

	require.Len(t, execer.statements, 5)
	assert.Contains(t, execer.statements[0], `"task_types"`)
	assert.Contains(t, execer.statements[4], `"tasks"`,
		"the referenced table goes last, after everything that points at it")
}
