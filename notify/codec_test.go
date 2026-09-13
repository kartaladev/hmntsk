package notify_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// goldenSignals is what testdata/signals.v1.json encodes. The file is written by
// hand, not generated from the code: it is the wire contract Redis and NATS
// broadcasters share, and a change to the encoding must change it on purpose.
var goldenSignals = []notify.Signal{
	{Recipient: "alice", Change: notify.ChangeCreated, At: time.Date(2026, 9, 14, 8, 30, 0, 123456000, time.UTC)},
	{Recipient: "bob", Change: notify.ChangeRead, At: time.Date(2026, 9, 14, 8, 30, 1, 0, time.UTC)},
}

func TestSignalCodec(t *testing.T) {
	t.Parallel()

	golden, err := os.ReadFile(filepath.Join("testdata", "signals.v1.json"))
	require.NoError(t, err)

	golden = bytes.TrimSpace(golden)

	type testCase struct {
		name   string
		assert func(t *testing.T)
	}

	cases := []testCase{
		{
			name: "signals encode to the version 1 wire format",
			assert: func(t *testing.T) {
				encoded, err := notify.EncodeSignals(goldenSignals)
				require.NoError(t, err)
				assert.Equal(t, string(golden), string(encoded))
				assert.Equal(t, 1, notify.SignalFormatVersion)
			},
		},
		{
			name: "the version 1 wire format decodes to the signals",
			assert: func(t *testing.T) {
				decoded, err := notify.DecodeSignals(golden)
				require.NoError(t, err)
				assert.Equal(t, goldenSignals, decoded)
			},
		},
		{
			name: "a round trip keeps the recipient and change, and the instant at microsecond precision in UTC",
			assert: func(t *testing.T) {
				zone := time.FixedZone("WIB", 7*60*60)
				original := notify.Signal{
					Recipient: "carol", Change: notify.ChangePruned,
					At: time.Date(2026, 9, 14, 15, 30, 0, 123456789, zone),
				}

				encoded, err := notify.EncodeSignals([]notify.Signal{original})
				require.NoError(t, err)

				decoded, err := notify.DecodeSignals(encoded)
				require.NoError(t, err)
				require.Len(t, decoded, 1)

				assert.Equal(t, "carol", decoded[0].Recipient)
				assert.Equal(t, notify.ChangePruned, decoded[0].Change)
				assert.Equal(t, time.UTC, decoded[0].At.Location())
				assert.True(t, original.At.Truncate(time.Microsecond).Equal(decoded[0].At))
				assert.Equal(t, 123456000, decoded[0].At.Nanosecond())
			},
		},
		{
			name: "another format version is reported as unknown",
			assert: func(t *testing.T) {
				_, err := notify.DecodeSignals([]byte(`{"v":2,"signals":[]}`))
				assert.ErrorIs(t, err, notify.ErrUnknownSignalFormat)
			},
		},
		{
			name: "a message with no version is reported as unknown",
			assert: func(t *testing.T) {
				_, err := notify.DecodeSignals([]byte(`{"signals":[]}`))
				assert.ErrorIs(t, err, notify.ErrUnknownSignalFormat)
			},
		},
		{
			name: "malformed JSON is an error, and not an unknown format",
			assert: func(t *testing.T) {
				_, err := notify.DecodeSignals([]byte(`{"v":1,"signals":[`))
				require.Error(t, err)
				assert.NotErrorIs(t, err, notify.ErrUnknownSignalFormat)
			},
		},
		{
			name: "the encoding carries no notification content",
			assert: func(t *testing.T) {
				encoded, err := notify.EncodeSignals(goldenSignals)
				require.NoError(t, err)

				var message map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(encoded, &message))
				assert.ElementsMatch(t, []string{"v", "signals"}, keys(message))

				var signals []map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(message["signals"], &signals))

				for _, signal := range signals {
					assert.ElementsMatch(t, []string{"recipient", "change", "at"}, keys(signal))
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t)
		})
	}
}

// keys lists a JSON object's keys.
func keys(object map[string]json.RawMessage) []string {
	out := make([]string, 0, len(object))
	for key := range object {
		out = append(out, key)
	}

	return out
}
