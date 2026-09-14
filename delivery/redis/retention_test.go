package redis_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/kartaladev/hmntsk"
	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
)

// TestRetention runs the stream bounds against a real broker.
//
// The broker is started with one entry per stream node. Trimming is always
// approximate, and Redis only trims whole nodes, so with its default of a
// hundred entries a node a small test stream is never trimmed at all. One entry
// a node makes approximate trimming exact, which is what lets these cases assert
// which entries remain rather than merely that fewer do.
func TestRetention(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(retentionSuite))
}

// retentionSuite shares one broker across its cases, each on its own stream.
type retentionSuite struct {
	suite.Suite

	client *goredis.Client
}

// SetupSuite starts the broker every case trims, and confirms it has the node
// size the cases depend on: a helper that silently dropped the setting would
// leave every exact assertion here asserting against the wrong broker.
func (s *retentionSuite) SetupSuite() {
	s.client = hmntskredis.RunTestRedis(s.T(),
		hmntskredis.WithTestServerConfig("stream-node-max-entries", "1"),
	)

	requireServerConfig(s.T(), s.client, map[string]string{"stream-node-max-entries": "1"})
}

// seed appends an entry with an explicit stream ID; see [seedEntry].
func (s *retentionSuite) seed(ctx context.Context, stream, id, eventID string) {
	s.T().Helper()

	seedEntry(s.T(), ctx, s.client, stream, id, eventID)
}

// requireServerConfig fails the test unless the broker reports every parameter
// with the wanted value. A helper that silently dropped a setting would leave
// every assertion depending on it asserting against the wrong broker.
func requireServerConfig(t *testing.T, client *goredis.Client, want map[string]string) {
	t.Helper()

	for parameter, value := range want {
		config, err := client.ConfigGet(t.Context(), parameter).Result()
		require.NoError(t, err)
		require.Equal(t, map[string]string{parameter: value}, config)
	}
}

// seedEntry appends an entry with an explicit stream ID, standing in for an
// event published earlier. It carries only the event identifier, which is all
// the retention cases read back.
func seedEntry(t *testing.T, ctx context.Context, client *goredis.Client, stream, id, eventID string) {
	t.Helper()

	require.NoError(t, client.XAdd(ctx, &goredis.XAddArgs{
		Stream: stream,
		ID:     id,
		Values: []any{hmntskredis.FieldEventID, eventID},
	}).Err(), "seed %s", id)
}

// TestLengthBound covers the length bound: without one nothing is removed, and
// with one the oldest entries go and the stream never falls below the bound.
func (s *retentionSuite) TestLengthBound() {
	const published = 12

	type testCase struct {
		name string
		opts []hmntskredis.Option
		// assert receives the stream length after each publish, and the event
		// identifiers left at the end.
		assert func(t *testing.T, lengths []int64, ids []string)
	}

	cases := []testCase{
		{
			name: "no bound keeps every entry",
			assert: func(t *testing.T, lengths []int64, ids []string) {
				for i, length := range lengths {
					assert.EqualValues(t, i+1, length, "after publish %d", i+1)
				}

				assert.Len(t, ids, published)
			},
		},
		{
			name: "a bound keeps the newest entries and never falls below the bound",
			opts: []hmntskredis.Option{hmntskredis.WithMaxLen(5)},
			assert: func(t *testing.T, lengths []int64, ids []string) {
				for i, length := range lengths {
					assert.EqualValues(t, min(i+1, 5), length, "after publish %d", i+1)
				}

				assert.Equal(t, []string{"evt-08", "evt-09", "evt-10", "evt-11", "evt-12"}, ids)
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()
			ctx := t.Context()

			sink, stream := newStreamSink(t, s.client, tc.opts...)

			lengths := make([]int64, 0, published)
			for i := range published {
				publishEvent(t, ctx, sink, fmt.Sprintf("evt-%02d", i+1))

				length, err := s.client.XLen(ctx, stream).Result()
				require.NoError(t, err)
				lengths = append(lengths, length)
			}

			tc.assert(t, lengths, eventIDs(t, ctx, s.client, stream))
		})
	}
}

// TestAgeBound covers the age bound. Entries are seeded at stream IDs three
// hours, ninety minutes and thirty minutes old; the publish itself lands at the
// broker's current time, after all of them.
func (s *retentionSuite) TestAgeBound() {
	type testCase struct {
		name string
		// offset shifts the sink's clock away from the real time.
		offset time.Duration
		assert func(t *testing.T, ids []string)
	}

	cases := []testCase{
		{
			name: "entries older than the cutoff are trimmed",
			assert: func(t *testing.T, ids []string) {
				assert.Equal(t, []string{"evt-30m", "evt-new"}, ids)
			},
		},
		{
			// An hour behind, the cutoff is two hours ago: the ninety-minute
			// entry survives, which it would not if the wall time were used.
			name:   "the cutoff follows the sink's clock",
			offset: -time.Hour,
			assert: func(t *testing.T, ids []string) {
				assert.Equal(t, []string{"evt-90m", "evt-30m", "evt-new"}, ids)
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()
			ctx := t.Context()

			now := time.Now()
			clock := hmntsk.ClockFunc(func() time.Time { return now.Add(tc.offset) })

			sink, stream := newStreamSink(t, s.client,
				hmntskredis.WithMaxAge(time.Hour),
				hmntskredis.WithClock(clock),
			)

			for _, seeded := range []struct {
				age     time.Duration
				eventID string
			}{
				{3 * time.Hour, "evt-3h"},
				{90 * time.Minute, "evt-90m"},
				{30 * time.Minute, "evt-30m"},
			} {
				s.seed(ctx, stream, streamID(now.Add(-seeded.age)), seeded.eventID)
			}

			publishEvent(t, ctx, sink, "evt-new")

			tc.assert(t, eventIDs(t, ctx, s.client, stream))
		})
	}
}

// streamID is the stream entry ID a broker would give an entry added at t.
func streamID(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10) + "-0"
}
