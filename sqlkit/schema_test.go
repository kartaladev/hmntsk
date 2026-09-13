package sqlkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/sqlkit"
)

// errExec is the failure the recording execer injects.
var errExec = errors.New("sqlkit_test: injected exec failure")

// recordingExecer captures the statements a runner executes, failing on one of
// them when failOn is set.
type recordingExecer struct {
	statements []string
	failOn     int
}

func (e *recordingExecer) ExecStatement(_ context.Context, sql string, _ ...any) error {
	e.statements = append(e.statements, sql)

	if e.failOn > 0 && len(e.statements) == e.failOn {
		return errExec
	}

	return nil
}

func TestSplitStatements(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		document string
		assert   func(t *testing.T, statements []string)
	}

	cases := []testCase{
		{
			name:     "a statement ends at a semicolon on the end of a line",
			document: "CREATE TABLE a (\n    id INT\n);\nCREATE INDEX a_idx ON a (id);\n",
			assert: func(t *testing.T, statements []string) {
				assert.Equal(t, []string{"CREATE TABLE a (\nid INT\n)", "CREATE INDEX a_idx ON a (id)"}, statements)
			},
		},
		{
			name:     "comments and blank lines are dropped",
			document: "-- a heading\n\n  -- an indented note\nCREATE TABLE a (id INT);\n\n",
			assert: func(t *testing.T, statements []string) {
				assert.Equal(t, []string{"CREATE TABLE a (id INT)"}, statements)
			},
		},
		{
			name:     "a trailing statement without a semicolon is kept",
			document: "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT)",
			assert: func(t *testing.T, statements []string) {
				assert.Equal(t, []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"}, statements)
			},
		},
		{
			name:     "a document of comments only has no statements",
			document: "-- nothing\n\n",
			assert:   func(t *testing.T, statements []string) { assert.Empty(t, statements) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlkit.SplitStatements(tc.document))
		})
	}
}

func TestRenderSchema(t *testing.T) {
	t.Parallel()

	document := "-- widgets\nCREATE TABLE IF NOT EXISTS \"" + sqlkit.PrefixToken + "widgets\" (\n    id TEXT\n);\n" +
		"CREATE INDEX IF NOT EXISTS \"" + sqlkit.PrefixToken + "widgets_idx\" ON \"" + sqlkit.PrefixToken +
		"widgets\" (id);\n"

	type testCase struct {
		name   string
		prefix string
		assert func(t *testing.T, statements []string)
	}

	cases := []testCase{
		{
			name:   "no prefix leaves the names bare and no token behind",
			prefix: "",
			assert: func(t *testing.T, statements []string) {
				require.Len(t, statements, 2)
				assert.Equal(t, "CREATE TABLE IF NOT EXISTS \"widgets\" (\nid TEXT\n)", statements[0])
				assert.NotContains(t, strings.Join(statements, "\n"), sqlkit.PrefixToken)
			},
		},
		{
			name:   "a prefix reaches every occurrence of the token",
			prefix: "app_",
			assert: func(t *testing.T, statements []string) {
				require.Len(t, statements, 2)
				assert.Equal(t, `CREATE INDEX IF NOT EXISTS "app_widgets_idx" ON "app_widgets" (id)`, statements[1])
				assert.NotContains(t, strings.Join(statements, "\n"), `"widgets`,
					"an unprefixed name left behind would address the wrong table")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlkit.RenderSchema(document, tc.prefix))
		})
	}
}

func TestApplySchema(t *testing.T) {
	t.Parallel()

	statements := []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)", "CREATE TABLE c (id INT)"}

	type testCase struct {
		name   string
		failOn int
		ctx    func(ctx context.Context) context.Context
		assert func(t *testing.T, executed []string, err error)
	}

	cases := []testCase{
		{
			name: "every statement runs, in order",
			assert: func(t *testing.T, executed []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, statements, executed)
			},
		},
		{
			name:   "a failing statement stops the run and is named",
			failOn: 2,
			assert: func(t *testing.T, executed []string, err error) {
				require.ErrorIs(t, err, errExec)
				assert.Equal(t, statements[:2], executed, "nothing after the failure may run")
				assert.Contains(t, err.Error(), "CREATE TABLE b (id INT)")
				assert.Contains(t, err.Error(), "sqlite")
			},
		},
		{
			name: "a cancelled context runs nothing",
			ctx: func(ctx context.Context) context.Context {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				return cancelled
			},
			assert: func(t *testing.T, executed []string, err error) {
				require.ErrorIs(t, err, context.Canceled)
				assert.Empty(t, executed)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}

			execer := &recordingExecer{failOn: tc.failOn}
			err := sqlkit.ApplySchema(ctx, execer, sqlkit.SQLite, statements)
			tc.assert(t, execer.statements, err)
		})
	}
}

func TestDropTables(t *testing.T) {
	t.Parallel()

	tables := []string{"widgets", "parts"}

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		failOn  int
		assert  func(t *testing.T, executed []string, err error)
	}

	cases := []testCase{
		{
			name:    "postgres drops in reverse order and cascades",
			dialect: sqlkit.PostgreSQL,
			assert: func(t *testing.T, executed []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{
					`DROP TABLE IF EXISTS "parts" CASCADE`,
					`DROP TABLE IF EXISTS "widgets" CASCADE`,
				}, executed, "whatever references a table goes before it")
			},
		},
		{
			name:    "mysql drops in reverse order without cascading",
			dialect: sqlkit.MySQL,
			assert: func(t *testing.T, executed []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"DROP TABLE IF EXISTS `parts`", "DROP TABLE IF EXISTS `widgets`"}, executed)
			},
		},
		{
			name:    "a failure names the table and stops",
			dialect: sqlkit.SQLite,
			failOn:  1,
			assert: func(t *testing.T, executed []string, err error) {
				require.ErrorIs(t, err, errExec)
				assert.Len(t, executed, 1)
				assert.Contains(t, err.Error(), "parts")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			execer := &recordingExecer{failOn: tc.failOn}
			err := sqlkit.DropTables(t.Context(), execer, tc.dialect, tables)
			tc.assert(t, execer.statements, err)
		})
	}
}
