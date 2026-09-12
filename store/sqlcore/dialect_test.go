package sqlcore_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/store/sqlcore"
)

func TestDialectFragments(t *testing.T) {
	t.Parallel()

	type expectation struct {
		placeholders   []string
		quoted         string
		quotedWithMark string
		returning      bool
		skipLocked     bool
		collation      string
		timestamp      string
		jsonColumn     string
		upsert         string
	}

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		expect  expectation
	}

	cases := []testCase{
		{
			name:    "postgres",
			dialect: sqlcore.PostgreSQL,
			expect: expectation{
				placeholders:   []string{"$1", "$2", "$10"},
				quoted:         `"task_type"`,
				quotedWithMark: `"we""ird"`,
				returning:      true,
				skipLocked:     true,
				collation:      "C",
				timestamp:      "timestamptz(6)",
				jsonColumn:     "text",
				upsert:         ` ON CONFLICT ("name") DO UPDATE SET "title" = EXCLUDED."title"`,
			},
		},
		{
			name:    "mysql",
			dialect: sqlcore.MySQL,
			expect: expectation{
				placeholders:   []string{"?", "?", "?"},
				quoted:         "`task_type`",
				quotedWithMark: "`we``ird`",
				returning:      false,
				skipLocked:     true,
				collation:      "utf8mb4_0900_as_cs",
				timestamp:      "DATETIME(6)",
				jsonColumn:     "LONGTEXT",
				upsert:         " ON DUPLICATE KEY UPDATE `title` = VALUES(`title`)",
			},
		},
		{
			name:    "sqlite",
			dialect: sqlcore.SQLite,
			expect: expectation{
				placeholders:   []string{"?", "?", "?"},
				quoted:         `"task_type"`,
				quotedWithMark: `"we""ird"`,
				returning:      true,
				skipLocked:     false,
				collation:      "BINARY",
				timestamp:      "TEXT",
				jsonColumn:     "TEXT",
				upsert:         ` ON CONFLICT ("name") DO UPDATE SET "title" = EXCLUDED."title"`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.name, tc.dialect.Name())

			for i, want := range tc.expect.placeholders {
				position := []int{1, 2, 10}[i]
				assert.Equalf(t, want, tc.dialect.Placeholder(position),
					"placeholder %d", position)
			}

			assert.Equal(t, tc.expect.quoted, tc.dialect.Quote("task_type"))
			assert.Equal(t, tc.expect.quotedWithMark, tc.dialect.Quote(`we`+string(tc.expect.quotedWithMark[0])+`ird`),
				"a quote character inside an identifier must be doubled, not left to close the quoting")
			assert.Equal(t, tc.expect.returning, tc.dialect.SupportsReturning())
			assert.Equal(t, tc.expect.skipLocked, tc.dialect.SupportsSkipLocked())
			assert.Equal(t, tc.expect.collation, tc.dialect.IdentifierCollation())
			assert.Equal(t, tc.expect.timestamp, tc.dialect.TimestampColumnType())
			assert.Equal(t, tc.expect.jsonColumn, tc.dialect.JSONColumnType())
			assert.Equal(t, tc.expect.upsert,
				tc.dialect.UpsertSuffix([]string{"name"}, []string{"title"}))
		})
	}
}

func TestOnlyMySQLLacksReturning(t *testing.T) {
	t.Parallel()

	// Nothing in this package may depend on RETURNING, and this is the reason:
	// one dialect does not have it. The check is here so that adding a dialect
	// forces a decision rather than an assumption.
	assert.False(t, sqlcore.MySQL.SupportsReturning())

	for _, dialect := range sqlcore.Dialects() {
		if dialect.Name() == "mysql" {
			continue
		}

		assert.Truef(t, dialect.SupportsReturning(), "%s", dialect.Name())
	}
}

func TestOnlySQLiteLacksRowLocking(t *testing.T) {
	t.Parallel()

	// SQLite's absence of row-level locking is why exclusive claiming is a
	// conditional update on lease columns rather than SELECT ... FOR UPDATE
	// SKIP LOCKED.
	assert.False(t, sqlcore.SQLite.SupportsSkipLocked())

	for _, dialect := range sqlcore.Dialects() {
		if dialect.Name() == "sqlite" {
			continue
		}

		assert.Truef(t, dialect.SupportsSkipLocked(), "%s", dialect.Name())
	}
}

func TestEveryDialectPinsAnIdentifierCollation(t *testing.T) {
	t.Parallel()

	for _, dialect := range sqlcore.Dialects() {
		assert.NotEmptyf(t, dialect.IdentifierCollation(),
			"%s must pin a collation, or who may claim a task depends on the dialect",
			dialect.Name())
	}
}

func TestDialectByName(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		lookup string
		assert func(t *testing.T, dialect sqlcore.Dialect, found bool)
	}

	cases := []testCase{
		{
			name: "a supported dialect is found", lookup: "postgres",
			assert: func(t *testing.T, dialect sqlcore.Dialect, found bool) {
				require.True(t, found)
				assert.Equal(t, "postgres", dialect.Name())
			},
		},
		{
			name: "names are lower-case", lookup: "PostgreSQL",
			assert: func(t *testing.T, _ sqlcore.Dialect, found bool) { assert.False(t, found) },
		},
		{
			name: "an unsupported dialect is not found", lookup: "mariadb",
			assert: func(t *testing.T, _ sqlcore.Dialect, found bool) { assert.False(t, found) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dialect, found := sqlcore.DialectByName(tc.lookup)
			tc.assert(t, dialect, found)
		})
	}
}
