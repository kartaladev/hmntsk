package redis_test

import (
	"context"
	"fmt"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
)

// TestTrimModes covers what trimming does about consumer groups, under a bound
// of three on a stream seeded with six entries, 1-0 to 6-0.
//
// The sink creates no group; each case creates the one it needs, standing in
// for a consumer the host runs.
func (s *retentionSuite) TestTrimModes() {
	const group = "consumers"

	type testCase struct {
		name string
		mode hmntskredis.TrimMode
		// publishes is how many events the sink publishes after seeding.
		publishes int
		prepare   func(t *testing.T, ctx context.Context, stream string)
		assert    func(t *testing.T, ctx context.Context, sink *hmntskredis.Sink, stream string)
	}

	// readGroup creates the group, has it read the count oldest entries, and
	// acknowledges the given IDs among them.
	readGroup := func(count int64, acknowledge ...string) func(t *testing.T, ctx context.Context, stream string) {
		return func(t *testing.T, ctx context.Context, stream string) {
			t.Helper()

			require.NoError(t, s.client.XGroupCreate(ctx, stream, group, "0").Err())

			if count > 0 {
				require.NoError(t, s.client.XReadGroup(ctx, &goredis.XReadGroupArgs{
					Group:    group,
					Consumer: "worker",
					Streams:  []string{stream, ">"},
					Count:    count,
				}).Err())
			}

			if len(acknowledge) > 0 {
				require.NoError(t, s.client.XAck(ctx, stream, group, acknowledge...).Err())
			}
		}
	}

	pending := func(t *testing.T, ctx context.Context, stream string) int64 {
		t.Helper()

		summary, err := s.client.XPending(ctx, stream, group).Result()
		require.NoError(t, err)

		return summary.Count
	}

	cases := []testCase{
		{
			name:      "keeping references leaves the trimmed entries pending",
			mode:      hmntskredis.TrimKeepRef,
			publishes: 1,
			prepare:   readGroup(4),
			assert: func(t *testing.T, ctx context.Context, _ *hmntskredis.Sink, stream string) {
				assert.Equal(t, []string{"evt-5", "evt-6", "evt-new-1"}, eventIDs(t, ctx, s.client, stream),
					"unacknowledged entries are trimmed all the same")
				assert.EqualValues(t, 4, pending(t, ctx, stream),
					"their identifiers stay pending, with nothing left to read")
			},
		},
		{
			name:      "deleting references clears the trimmed entries from pending",
			mode:      hmntskredis.TrimDelRef,
			publishes: 1,
			prepare:   readGroup(4),
			assert: func(t *testing.T, ctx context.Context, _ *hmntskredis.Sink, stream string) {
				assert.Equal(t, []string{"evt-5", "evt-6", "evt-new-1"}, eventIDs(t, ctx, s.client, stream))
				assert.Zero(t, pending(t, ctx, stream))
			},
		},
		{
			name:      "acknowledged-only stops at the first unacknowledged entry",
			mode:      hmntskredis.TrimAcked,
			publishes: 1,
			prepare:   readGroup(2, "1-0", "2-0"),
			assert: func(t *testing.T, ctx context.Context, _ *hmntskredis.Sink, stream string) {
				assert.Equal(t,
					[]string{"evt-3", "evt-4", "evt-5", "evt-6", "evt-new-1"},
					eventIDs(t, ctx, s.client, stream),
					"only the two acknowledged entries may go, however far over the bound the stream is")
			},
		},
		{
			// The remedy is part of the claim: the bound is not broken, it is
			// held back by the group, and deleting the group releases it.
			name:      "a group that never reads stops acknowledged-only trimming",
			mode:      hmntskredis.TrimAcked,
			publishes: 5,
			prepare:   readGroup(0),
			assert: func(t *testing.T, ctx context.Context, sink *hmntskredis.Sink, stream string) {
				length, err := s.client.XLen(ctx, stream).Result()
				require.NoError(t, err)
				require.EqualValues(t, 11, length, "nothing is trimmed while the group holds every entry")

				require.NoError(t, s.client.XGroupDestroy(ctx, stream, group).Err())
				publishEvent(t, ctx, sink, "evt-after-destroy")

				assert.Equal(t,
					[]string{"evt-new-4", "evt-new-5", "evt-after-destroy"},
					eventIDs(t, ctx, s.client, stream),
					"with the stale group gone the bound applies again")
			},
		},
		{
			name:      "acknowledged-only with no groups trims to the bound",
			mode:      hmntskredis.TrimAcked,
			publishes: 1,
			assert: func(t *testing.T, ctx context.Context, _ *hmntskredis.Sink, stream string) {
				assert.Equal(t, []string{"evt-5", "evt-6", "evt-new-1"}, eventIDs(t, ctx, s.client, stream))

				// Trimming by acknowledgement reads group state; it must not
				// create any.
				groups, err := s.client.XInfoGroups(ctx, stream).Result()
				require.NoError(t, err)
				assert.Empty(t, groups, "the sink must create no consumer group")
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()
			ctx := t.Context()

			sink, stream := newStreamSink(t, s.client,
				hmntskredis.WithMaxLen(3),
				hmntskredis.WithTrimMode(tc.mode),
			)

			for i := 1; i <= 6; i++ {
				s.seed(ctx, stream, fmt.Sprintf("%d-0", i), fmt.Sprintf("evt-%d", i))
			}

			if tc.prepare != nil {
				tc.prepare(t, ctx, stream)
			}

			for i := 1; i <= tc.publishes; i++ {
				publishEvent(t, ctx, sink, fmt.Sprintf("evt-new-%d", i))
			}

			tc.assert(t, ctx, sink, stream)
		})
	}
}
