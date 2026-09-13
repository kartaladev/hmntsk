package redis_test

import (
	"fmt"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
	"github.com/kartaladev/hmntsk/relay"
)

// redis7Image is a broker from before stream trim modes existed (Redis 8.2),
// pinned for the same reason as [hmntskredis.RedisImage]. It is a test
// constant rather than part of the package: nothing outside these tests needs
// an old broker.
const redis7Image = "redis:7.4.7-alpine"

// redis7TrimModeError is what a broker older than Redis 8.2 answers to an XADD
// carrying any trim mode. It names neither trimming nor modes, which is why
// docs/delivery.md quotes it: an operator searching for it has to find the
// explanation. The docs test asserts the document quotes this same string, and
// TestATrimModeFailsRetryably asserts a real broker still produces it.
const redis7TrimModeError = "ERR Invalid stream ID specified as stream command argument"

// TestRetentionOnRedis7 runs the sink against a broker from before trim modes,
// proving on a real server what still works there and how what does not fails.
func TestRetentionOnRedis7(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(redis7Suite))
}

// redis7Suite shares one pre-8.2 broker across its cases, each on its own
// stream. One entry per stream node, for the same reason as retentionSuite.
type redis7Suite struct {
	suite.Suite

	client *goredis.Client
}

// SetupSuite starts the older broker.
func (s *redis7Suite) SetupSuite() {
	s.client = hmntskredis.RunTestRedis(s.T(),
		hmntskredis.WithTestImage(redis7Image),
		hmntskredis.WithTestServerConfig("stream-node-max-entries", "1"),
	)
}

// TestPublishesWithoutATrimMode covers what an older broker must keep doing: a
// sink with no retention publishes, and a bound with no mode publishes and
// trims.
func (s *redis7Suite) TestPublishesWithoutATrimMode() {
	const published = 6

	type testCase struct {
		name   string
		opts   []hmntskredis.Option
		assert func(t *testing.T, length int64)
	}

	cases := []testCase{
		{
			name: "no retention",
			assert: func(t *testing.T, length int64) {
				assert.EqualValues(t, published, length)
			},
		},
		{
			name: "a length bound without a trim mode",
			opts: []hmntskredis.Option{hmntskredis.WithMaxLen(3)},
			assert: func(t *testing.T, length int64) {
				assert.EqualValues(t, 3, length)
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()
			ctx := t.Context()

			sink, stream := newStreamSink(t, s.client, tc.opts...)

			for i := range published {
				publishEvent(t, ctx, sink, fmt.Sprintf("evt-%d", i))
			}

			length, err := s.client.XLen(ctx, stream).Result()
			require.NoError(t, err)
			tc.assert(t, length)
		})
	}
}

// TestATrimModeFailsRetryably pins the operational constraint the docs warn
// about: on a broker older than Redis 8.2, every mode — including KEEPREF, 8.2's
// own default — makes every publish fail. The failure stays retryable, as every
// broker rejection is, so the relay retries until the attempt limit.
func (s *redis7Suite) TestATrimModeFailsRetryably() {
	type testCase struct {
		name   string
		mode   hmntskredis.TrimMode
		assert func(t *testing.T, outcome relay.Outcome, published int64)
	}

	failsRetryably := func(t *testing.T, outcome relay.Outcome, published int64) {
		t.Helper()

		require.Equal(t, relay.OutcomeRetryable, outcome.Status)
		require.ErrorIs(t, outcome.Err, hmntskredis.ErrPublish)
		assert.ErrorContains(t, outcome.Err, redis7TrimModeError)
		assert.Zero(t, published, "a rejected publish must not reach the stream")
	}

	cases := []testCase{
		{name: "keeping references", mode: hmntskredis.TrimKeepRef, assert: failsRetryably},
		{name: "deleting references", mode: hmntskredis.TrimDelRef, assert: failsRetryably},
		{name: "acknowledged only", mode: hmntskredis.TrimAcked, assert: failsRetryably},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()
			ctx := t.Context()

			sink, stream := newStreamSink(t, s.client,
				hmntskredis.WithMaxLen(3),
				hmntskredis.WithTrimMode(tc.mode),
			)

			outcome := sink.Deliver(ctx, attempt(testEvent()))

			published, err := s.client.Exists(ctx, stream).Result()
			require.NoError(t, err)
			tc.assert(t, outcome, published)
		})
	}
}
