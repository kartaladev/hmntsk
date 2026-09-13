package sqlkit_test

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/sqlkit"
)

func TestNormalizeTime(t *testing.T) {
	t.Parallel()

	jakarta := time.FixedZone("WIB", 7*60*60)

	type testCase struct {
		name    string
		instant time.Time
		assert  func(t *testing.T, normalized time.Time)
	}

	cases := []testCase{
		{
			name:    "nanoseconds are truncated, not rounded",
			instant: time.Date(2026, 9, 14, 12, 0, 0, 123456999, time.UTC),
			assert: func(t *testing.T, normalized time.Time) {
				assert.Equal(t, time.Date(2026, 9, 14, 12, 0, 0, 123456000, time.UTC), normalized)
			},
		},
		{
			name:    "a zoned instant becomes the same instant in UTC",
			instant: time.Date(2026, 9, 14, 19, 0, 0, 0, jakarta),
			assert: func(t *testing.T, normalized time.Time) {
				assert.Equal(t, time.UTC, normalized.Location())
				assert.Equal(t, time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), normalized)
			},
		},
		{
			name:    "the zero time stays zero",
			instant: time.Time{},
			assert:  func(t *testing.T, normalized time.Time) { assert.True(t, normalized.IsZero()) },
		},
		{
			name:    "an instant before the Unix epoch truncates the same way",
			instant: time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC),
			assert: func(t *testing.T, normalized time.Time) {
				assert.Equal(t, time.Date(1969, 12, 31, 23, 59, 59, 999999000, time.UTC), normalized)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlkit.NormalizeTime(tc.instant))
		})
	}
}

func TestEncodeTime(t *testing.T) {
	t.Parallel()

	instant := time.Date(2026, 9, 14, 19, 30, 1, 987654321, time.FixedZone("WIB", 7*60*60))

	type testCase struct {
		name    string
		dialect sqlkit.Dialect
		instant *time.Time
		assert  func(t *testing.T, encoded any)
	}

	cases := []testCase{
		{
			name: "no instant is NULL", dialect: sqlkit.PostgreSQL, instant: nil,
			assert: func(t *testing.T, encoded any) { assert.Nil(t, encoded) },
		},
		{
			name: "the zero instant is NULL", dialect: sqlkit.SQLite, instant: &time.Time{},
			assert: func(t *testing.T, encoded any) { assert.Nil(t, encoded) },
		},
		{
			name: "a dialect with a native timestamp gets a normalised time", dialect: sqlkit.MySQL, instant: &instant,
			assert: func(t *testing.T, encoded any) {
				assert.Equal(t, time.Date(2026, 9, 14, 12, 30, 1, 987654000, time.UTC), encoded)
			},
		},
		{
			name: "a dialect without one gets the fixed text layout", dialect: sqlkit.SQLite, instant: &instant,
			assert: func(t *testing.T, encoded any) {
				assert.Equal(t, "2026-09-14T12:30:01.987654Z", encoded)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlkit.EncodeTime(tc.dialect, tc.instant))
		})
	}
}

func TestTimestampLayoutOrdersLexically(t *testing.T) {
	t.Parallel()

	instants := []time.Time{
		time.Date(2026, 9, 14, 12, 0, 0, 5000, time.UTC),
		time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 14, 12, 0, 0, 100000, time.UTC),
	}

	encoded := make([]string, 0, len(instants))
	for _, instant := range instants {
		encoded = append(encoded, instant.Format(sqlkit.TimestampLayout))
	}

	sort.Strings(encoded)

	for i := 1; i < len(encoded); i++ {
		earlier, err := sqlkit.DecodeTime(encoded[i-1])
		require.NoError(t, err)

		later, err := sqlkit.DecodeTime(encoded[i])
		require.NoError(t, err)

		assert.Truef(t, earlier.Before(later), "%s must sort before %s", encoded[i-1], encoded[i])
	}
}

func TestDecodeTime(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 9, 14, 12, 30, 1, 987654000, time.UTC)

	type testCase struct {
		name   string
		value  any
		assert func(t *testing.T, decoded time.Time, err error)
	}

	is := func(expected time.Time) func(t *testing.T, decoded time.Time, err error) {
		return func(t *testing.T, decoded time.Time, err error) {
			require.NoError(t, err)
			assert.Equal(t, expected, decoded)
		}
	}

	cases := []testCase{
		{name: "NULL is the zero time", value: nil, assert: is(time.Time{})},
		{name: "empty text is the zero time", value: "  ", assert: is(time.Time{})},
		{
			name:   "a native zoned time is normalised",
			value:  time.Date(2026, 9, 14, 19, 30, 1, 987654999, time.FixedZone("WIB", 7*60*60)),
			assert: is(want),
		},
		{name: "the fixed layout as text", value: "2026-09-14T12:30:01.987654Z", assert: is(want)},
		{name: "the fixed layout as bytes", value: []byte("2026-09-14T12:30:01.987654Z"), assert: is(want)},
		{name: "a driver's space-separated text", value: "2026-09-14 12:30:01.987654", assert: is(want)},
		{name: "text with an offset", value: "2026-09-14T19:30:01.987654+07:00", assert: is(want)},
		{
			name:  "malformed text is an error naming the value",
			value: "yesterday",
			assert: func(t *testing.T, _ time.Time, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "yesterday")
			},
		},
		{
			name:  "an unsupported type is an error",
			value: 42,
			assert: func(t *testing.T, _ time.Time, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "int")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := sqlkit.DecodeTime(tc.value)
			tc.assert(t, decoded, err)
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		value  any
		assert func(t *testing.T, decoded json.RawMessage, err error)
	}

	source := []byte(`{"b": 1,  "a": [1.0]}`)

	cases := []testCase{
		{
			name: "NULL is no payload", value: nil,
			assert: func(t *testing.T, decoded json.RawMessage, err error) {
				require.NoError(t, err)
				assert.Nil(t, decoded)
			},
		},
		{
			name: "empty text is no payload", value: "",
			assert: func(t *testing.T, decoded json.RawMessage, err error) {
				require.NoError(t, err)
				assert.Nil(t, decoded)
			},
		},
		{
			name: "text comes back byte for byte", value: `{"b": 1,  "a": [1.0]}`,
			assert: func(t *testing.T, decoded json.RawMessage, err error) {
				require.NoError(t, err)
				assert.JSONEq(t, `{"a":[1],"b":1}`, string(decoded))
				//nolint:testifylint // byte-for-byte survival is the point; JSONEq would hide a rewrite
				assert.Equal(t, `{"b": 1,  "a": [1.0]}`, string(decoded), "field order and number literals survive")
			},
		},
		{
			name: "bytes are copied, not aliased", value: source,
			assert: func(t *testing.T, decoded json.RawMessage, err error) {
				require.NoError(t, err)
				assert.Equal(t, string(source), string(decoded))

				decoded[0] = '['
				assert.Equal(t, byte('{'), source[0], "a driver may reuse its buffer")
			},
		},
		{
			name: "an unsupported type is an error", value: 3.5,
			assert: func(t *testing.T, _ json.RawMessage, err error) { require.Error(t, err) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := sqlkit.DecodeJSON(tc.value)
			tc.assert(t, decoded, err)
		})
	}
}

func TestDecodeString(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		value  any
		assert func(t *testing.T, decoded string, err error)
	}

	is := func(expected string) func(t *testing.T, decoded string, err error) {
		return func(t *testing.T, decoded string, err error) {
			require.NoError(t, err)
			assert.Equal(t, expected, decoded)
		}
	}

	cases := []testCase{
		{name: "NULL is empty", value: nil, assert: is("")},
		{name: "text", value: "alice", assert: is("alice")},
		{name: "bytes", value: []byte("bob"), assert: is("bob")},
		{
			name: "an unsupported type is an error", value: int64(1),
			assert: func(t *testing.T, _ string, err error) { require.Error(t, err) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := sqlkit.DecodeString(tc.value)
			tc.assert(t, decoded, err)
		})
	}
}

func TestDecodeInt(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		value  any
		assert func(t *testing.T, decoded int64, err error)
	}

	is := func(expected int64) func(t *testing.T, decoded int64, err error) {
		return func(t *testing.T, decoded int64, err error) {
			require.NoError(t, err)
			assert.Equal(t, expected, decoded)
		}
	}

	fails := func(t *testing.T, _ int64, err error) { require.Error(t, err) }

	cases := []testCase{
		{name: "NULL is zero", value: nil, assert: is(0)},
		{name: "int64", value: int64(9), assert: is(9)},
		{name: "int32", value: int32(8), assert: is(8)},
		{name: "int", value: 7, assert: is(7)},
		{name: "float64", value: float64(6), assert: is(6)},
		{name: "decimal text", value: " 42 ", assert: is(42)},
		{name: "decimal bytes", value: []byte("-3"), assert: is(-3)},
		{name: "empty text is zero", value: "", assert: is(0)},
		{name: "text that is not a number", value: "many", assert: fails},
		{name: "an unsupported type", value: true, assert: fails},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := sqlkit.DecodeInt(tc.value)
			tc.assert(t, decoded, err)
		})
	}
}

func TestEncodeRaw(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		payload []byte
		assert  func(t *testing.T, encoded any)
	}

	cases := []testCase{
		{name: "no payload is NULL", payload: nil, assert: func(t *testing.T, encoded any) { assert.Nil(t, encoded) }},
		{
			name: "an empty payload is NULL", payload: []byte{},
			assert: func(t *testing.T, encoded any) { assert.Nil(t, encoded) },
		},
		{
			name: "a payload is passed as text, so no driver takes the column for binary", payload: []byte(`{"a":1}`),
			assert: func(t *testing.T, encoded any) { assert.Equal(t, `{"a":1}`, encoded) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlkit.EncodeRaw(tc.payload))
		})
	}
}
