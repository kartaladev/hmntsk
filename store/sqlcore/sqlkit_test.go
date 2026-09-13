package sqlcore_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/sqlkit"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// TestNormalizeTimeAgreesWithTheEngine guards the one function sqlkit and the
// engine each have to keep a copy of.
//
// sqlkit cannot import the engine and the engine must not import SQL support,
// so neither can call the other's. If the two ever disagreed, an instant sqlkit
// stored would not compare equal to the same instant the engine computed, and a
// deadline or a lease would silently shift by up to a microsecond.
func TestNormalizeTimeAgreesWithTheEngine(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		instant time.Time
		assert  func(t *testing.T, engine, kit time.Time)
	}

	agree := func(t *testing.T, engine, kit time.Time) {
		assert.Equal(t, engine, kit)
		assert.Equal(t, engine.Location(), kit.Location())
	}

	cases := []testCase{
		{name: "nanoseconds", instant: time.Date(2026, 9, 14, 12, 0, 0, 123456789, time.UTC), assert: agree},
		{
			name:    "a zoned instant",
			instant: time.Date(2026, 9, 14, 19, 0, 0, 999999999, time.FixedZone("WIB", 7*60*60)),
			assert:  agree,
		},
		{name: "the zero time", instant: time.Time{}, assert: agree},
		{name: "before the Unix epoch", instant: time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC), assert: agree},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.NormalizeTime(tc.instant), sqlkit.NormalizeTime(tc.instant))
		})
	}
}

// TestSchemaMismatchMatchesBothConfigurationErrors pins the error a host sees
// from VerifySchema now that the comparison lives in sqlkit: it is still the
// engine's configuration error, in the engine's words, and it is also sqlkit's.
func TestSchemaMismatchMatchesBothConfigurationErrors(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.MySQL)

	columns := without("tasks", "due_at")(completeSchema(b, sqlcore.MySQL, ""))

	err := b.VerifySchema(t.Context(), newQuerier(b, columns, completeIndexes("")))
	require.Error(t, err)

	assert.ErrorIs(t, err, hmntsk.ErrConfiguration, "a host checking the engine's error keeps working")
	assert.ErrorIs(t, err, sqlkit.ErrConfiguration)
	assert.ErrorIs(t, err, sqlkit.ErrSchemaMismatch)

	var kitErr *sqlkit.SchemaError

	require.ErrorAs(t, err, &kitErr)
	assert.Len(t, kitErr.Issues, 1)
	assert.Equal(t, "mysql", kitErr.Dialect)

	assert.True(t, strings.HasPrefix(err.Error(), "hmntsk: the mysql schema does not match what the engine requires: "),
		"the engine's message is unchanged: %s", err)
	assert.Contains(t, err.Error(), "tasks.due_at: column is missing")
}
