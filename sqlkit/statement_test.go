package sqlkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kartaladev/hmntsk/sqlkit"
)

func TestStatementIsZero(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name      string
		statement sqlkit.Statement
		assert    func(t *testing.T, zero bool)
	}

	cases := []testCase{
		{
			name:      "the zero value is zero",
			statement: sqlkit.Statement{},
			assert:    func(t *testing.T, zero bool) { assert.True(t, zero) },
		},
		{
			name:      "arguments without text are still nothing to run",
			statement: sqlkit.Statement{Args: []any{1}},
			assert:    func(t *testing.T, zero bool) { assert.True(t, zero) },
		},
		{
			name:      "text makes a statement",
			statement: sqlkit.Statement{SQL: "SELECT 1"},
			assert:    func(t *testing.T, zero bool) { assert.False(t, zero) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.statement.IsZero())
		})
	}
}

func TestWriter(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		write   func(w *sqlkit.Writer, d sqlkit.Dialect)
		assert  func(t *testing.T, statement sqlkit.Statement)
	}

	selectWithList := func(w *sqlkit.Writer, d sqlkit.Dialect) {
		w.Write("SELECT ", d.Quote("type"), " FROM ", d.Quote("widgets"), " WHERE ", d.Quote("id"), " = ", w.Bind(7))
		w.Write(" AND ", d.Quote("owner"), " IN (", w.BindAll("alice", "bob"), ")")
	}

	cases := []testCase{
		{
			name:    "postgres numbers its bind markers across calls and quotes identifiers",
			dialect: sqlkit.PostgreSQL,
			write:   selectWithList,
			assert: func(t *testing.T, statement sqlkit.Statement) {
				assert.Equal(t, `SELECT "type" FROM "widgets" WHERE "id" = $1 AND "owner" IN ($2, $3)`, statement.SQL)
				assert.Equal(t, []any{7, "alice", "bob"}, statement.Args)
			},
		},
		{
			name:    "mysql uses question marks and backticks",
			dialect: sqlkit.MySQL,
			write:   selectWithList,
			assert: func(t *testing.T, statement sqlkit.Statement) {
				assert.Equal(t, "SELECT `type` FROM `widgets` WHERE `id` = ? AND `owner` IN (?, ?)", statement.SQL)
				assert.Equal(t, []any{7, "alice", "bob"}, statement.Args)
			},
		},
		{
			name:    "sqlite uses question marks and double quotes",
			dialect: sqlkit.SQLite,
			write:   selectWithList,
			assert: func(t *testing.T, statement sqlkit.Statement) {
				assert.Equal(t, `SELECT "type" FROM "widgets" WHERE "id" = ? AND "owner" IN (?, ?)`, statement.SQL)
				assert.Equal(t, []any{7, "alice", "bob"}, statement.Args)
			},
		},
		{
			name:    "binding nothing renders nothing and adds no argument",
			dialect: sqlkit.PostgreSQL,
			write: func(w *sqlkit.Writer, _ sqlkit.Dialect) {
				w.Write("SELECT 1", w.BindAll())
			},
			assert: func(t *testing.T, statement sqlkit.Statement) {
				assert.Equal(t, "SELECT 1", statement.SQL)
				assert.Empty(t, statement.Args)
			},
		},
		{
			name:    "a writer never written to produces the zero statement",
			dialect: sqlkit.SQLite,
			write:   func(*sqlkit.Writer, sqlkit.Dialect) {},
			assert: func(t *testing.T, statement sqlkit.Statement) {
				assert.True(t, statement.IsZero())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := sqlkit.NewWriter(tc.dialect)
			tc.write(w, tc.dialect)
			tc.assert(t, w.Done())
		})
	}
}

func TestTrimSQL(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		sql    string
		assert func(t *testing.T, trimmed string)
	}

	cases := []testCase{
		{
			name: "whitespace runs collapse to one space",
			sql:  "SELECT  a,\n\t b\nFROM   t ",
			assert: func(t *testing.T, trimmed string) {
				assert.Equal(t, "SELECT a, b FROM t", trimmed)
			},
		},
		{
			name:   "empty stays empty",
			sql:    " \n ",
			assert: func(t *testing.T, trimmed string) { assert.Empty(t, trimmed) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlkit.TrimSQL(tc.sql))
		})
	}
}
