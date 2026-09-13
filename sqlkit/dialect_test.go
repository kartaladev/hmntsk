package sqlkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/sqlkit"
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
		booleanTrue    string
		upsert         string
	}

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		expect  expectation
	}

	cases := []testCase{
		{
			name:    "postgres",
			dialect: sqlkit.PostgreSQL,
			expect: expectation{
				placeholders:   []string{"$1", "$2", "$10"},
				quoted:         `"type"`,
				quotedWithMark: `"we""ird"`,
				returning:      true,
				skipLocked:     true,
				collation:      "C",
				timestamp:      "timestamptz(6)",
				jsonColumn:     "text",
				booleanTrue:    "TRUE",
				upsert:         ` ON CONFLICT ("name") DO UPDATE SET "title" = EXCLUDED."title"`,
			},
		},
		{
			name:    "mysql",
			dialect: sqlkit.MySQL,
			expect: expectation{
				placeholders:   []string{"?", "?", "?"},
				quoted:         "`type`",
				quotedWithMark: "`we``ird`",
				returning:      false,
				skipLocked:     true,
				collation:      "utf8mb4_0900_as_cs",
				timestamp:      "DATETIME(6)",
				jsonColumn:     "LONGTEXT",
				booleanTrue:    "1",
				upsert:         " ON DUPLICATE KEY UPDATE `title` = VALUES(`title`)",
			},
		},
		{
			name:    "sqlite",
			dialect: sqlkit.SQLite,
			expect: expectation{
				placeholders:   []string{"?", "?", "?"},
				quoted:         `"type"`,
				quotedWithMark: `"we""ird"`,
				returning:      true,
				skipLocked:     false,
				collation:      "BINARY",
				timestamp:      "TEXT",
				jsonColumn:     "TEXT",
				booleanTrue:    "1",
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
				assert.Equalf(t, want, tc.dialect.Placeholder(position), "placeholder %d", position)
			}

			assert.Equal(t, tc.expect.quoted, tc.dialect.Quote("type"))
			assert.Equal(t, tc.expect.quotedWithMark, tc.dialect.Quote(`we`+string(tc.expect.quotedWithMark[0])+`ird`),
				"a quote character inside an identifier must be doubled, not left to close the quoting")
			assert.Equal(t, tc.expect.returning, tc.dialect.SupportsReturning())
			assert.Equal(t, tc.expect.skipLocked, tc.dialect.SupportsSkipLocked())
			assert.Equal(t, tc.expect.collation, tc.dialect.IdentifierCollation())
			assert.Equal(t, tc.expect.timestamp, tc.dialect.TimestampColumnType())
			assert.Equal(t, tc.expect.jsonColumn, tc.dialect.JSONColumnType())
			assert.Equal(t, tc.expect.booleanTrue, tc.dialect.BooleanTrue())
			assert.Equal(t, tc.expect.upsert, tc.dialect.UpsertSuffix([]string{"name"}, []string{"title"}))
		})
	}
}

func TestDialectsAreStableAndDistinct(t *testing.T) {
	t.Parallel()

	names := make([]string, 0, len(sqlkit.Dialects()))
	for _, dialect := range sqlkit.Dialects() {
		names = append(names, dialect.Name())
	}

	assert.Equal(t, []string{"postgres", "mysql", "sqlite"}, names)

	for _, dialect := range sqlkit.Dialects() {
		assert.NotEmptyf(t, dialect.IdentifierCollation(),
			"%s must pin a collation, or identifier comparison depends on the dialect", dialect.Name())
	}
}

func TestDialectByName(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		lookup string
		assert func(t *testing.T, dialect sqlkit.Dialect, found bool)
	}

	cases := []testCase{
		{
			name: "a supported dialect is found", lookup: "mysql",
			assert: func(t *testing.T, dialect sqlkit.Dialect, found bool) {
				require.True(t, found)
				assert.Equal(t, sqlkit.MySQL, dialect)
			},
		},
		{
			name: "names are lower-case", lookup: "PostgreSQL",
			assert: func(t *testing.T, dialect sqlkit.Dialect, found bool) {
				assert.False(t, found)
				assert.Nil(t, dialect)
			},
		},
		{
			name: "an unknown dialect is not found", lookup: "mariadb",
			assert: func(t *testing.T, dialect sqlkit.Dialect, found bool) {
				assert.False(t, found)
				assert.Nil(t, dialect)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dialect, found := sqlkit.DialectByName(tc.lookup)
			tc.assert(t, dialect, found)
		})
	}
}
