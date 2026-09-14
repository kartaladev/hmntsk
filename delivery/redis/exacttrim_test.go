package redis_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kartaladev/hmntsk"
	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
)

// TestExactTrim runs the bounds against a broker that keeps Redis's default of
// a hundred entries a stream node, unlike [TestRetention]. At that size
// approximate trimming leaves a small stream untouched, which is exactly the
// surprise exact trimming exists to remove.
//
// The node byte limit is lifted. Redis also closes a node at
// stream-node-max-bytes (4096 by default), and an event body is large enough
// that a node closes after a few entries, which would let approximate trimming
// look exact by accident. Without the byte limit the node size is the entry
// count, which is what the approximate case must be seen to fall short of.
func TestExactTrim(t *testing.T) {
	t.Parallel()

	client := hmntskredis.RunTestRedis(t, hmntskredis.WithTestServerConfig("stream-node-max-bytes", "0"))

	// These cases mean something only with nodes of a hundred entries.
	requireServerConfig(t, client, map[string]string{
		"stream-node-max-entries": "100",
		"stream-node-max-bytes":   "0",
	})

	t.Run("length bound", func(t *testing.T) {
		t.Parallel()

		const published = 10

		type testCase struct {
			name   string
			opts   []hmntskredis.Option
			assert func(t *testing.T, ids []string)
		}

		cases := []testCase{
			{
				name: "approximate trimming leaves a small stream alone",
				opts: []hmntskredis.Option{hmntskredis.WithMaxLen(2)},
				assert: func(t *testing.T, ids []string) {
					assert.Greater(t, len(ids), 2)
				},
			},
			{
				name: "exact trimming holds exactly the bound",
				opts: []hmntskredis.Option{hmntskredis.WithMaxLen(2), hmntskredis.WithExactTrim()},
				assert: func(t *testing.T, ids []string) {
					assert.Equal(t, []string{"evt-09", "evt-10"}, ids)
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				ctx := t.Context()
				sink, stream := newStreamSink(t, client, tc.opts...)

				for i := range published {
					publishEvent(t, ctx, sink, fmt.Sprintf("evt-%02d", i+1))
				}

				tc.assert(t, eventIDs(t, ctx, client, stream))
			})
		}
	})

	t.Run("age bound removes every entry older than the cutoff", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		now := time.Now()

		sink, stream := newStreamSink(t, client,
			hmntskredis.WithMaxAge(time.Hour),
			hmntskredis.WithExactTrim(),
			hmntskredis.WithClock(hmntsk.ClockFunc(func() time.Time { return now })),
		)

		for _, seeded := range []struct {
			age     time.Duration
			eventID string
		}{
			{3 * time.Hour, "evt-3h"},
			{90 * time.Minute, "evt-90m"},
			{30 * time.Minute, "evt-30m"},
		} {
			seedEntry(t, ctx, client, stream, streamID(now.Add(-seeded.age)), seeded.eventID)
		}

		publishEvent(t, ctx, sink, "evt-new")

		assert.Equal(t, []string{"evt-30m", "evt-new"}, eventIDs(t, ctx, client, stream))
	})
}
