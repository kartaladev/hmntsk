package redis_test

import (
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
)

// TestNew covers construction-time validation: a wiring mistake is an error
// from the constructor, before a single event is published.
func TestNew(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		client goredis.UniversalClient
		opts   []hmntskredis.Option
		assert func(t *testing.T, sink *hmntskredis.Sink, err error)
	}

	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:6379"})

	rejected := func(t *testing.T, sink *hmntskredis.Sink, err error) {
		t.Helper()

		require.ErrorIs(t, err, hmntskredis.ErrConfiguration)
		assert.Nil(t, sink)
	}

	accepted := func(t *testing.T, sink *hmntskredis.Sink, err error) {
		t.Helper()

		require.NoError(t, err)
		assert.NotNil(t, sink)
	}

	cases := []testCase{
		{
			name:   "defaults",
			client: client,
			assert: func(t *testing.T, sink *hmntskredis.Sink, err error) {
				require.NoError(t, err)
				require.NotNil(t, sink)
				assert.Equal(t, hmntskredis.DefaultName, sink.Name())
				assert.Equal(t, hmntskredis.DefaultStream, sink.Stream())
			},
		},
		{
			name:   "stream and name overridden",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithStream("acme.tasks"),
				hmntskredis.WithName("acme-bus"),
				hmntskredis.WithTimeout(2 * time.Second),
			},
			assert: func(t *testing.T, sink *hmntskredis.Sink, err error) {
				require.NoError(t, err)
				assert.Equal(t, "acme-bus", sink.Name())
				assert.Equal(t, "acme.tasks", sink.Stream())
			},
		},
		{
			name:   "nil client",
			client: nil,
			assert: rejected,
		},
		{
			name:   "empty stream",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithStream("")},
			assert: rejected,
		},
		{
			name:   "empty name",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithName("")},
			assert: rejected,
		},
		{
			name:   "non-positive timeout",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithTimeout(0)},
			assert: rejected,
		},
		{
			name:   "zero length bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithMaxLen(0)},
			assert: rejected,
		},
		{
			name:   "negative length bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithMaxLen(-1)},
			assert: rejected,
		},
		{
			name:   "zero age bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithMaxAge(0)},
			assert: rejected,
		},
		{
			name:   "negative age bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithMaxAge(-time.Second)},
			assert: rejected,
		},
		{
			// The broker takes one threshold per command, and the client would
			// silently prefer the length: one of them would quietly not apply.
			name:   "length and age bound together",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxLen(1000),
				hmntskredis.WithMaxAge(time.Hour),
			},
			assert: rejected,
		},
		{
			// A mode with nothing to trim is meaningless, and would still break
			// every publish on a broker older than trim modes.
			name:   "trim mode without a bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithTrimMode(hmntskredis.TrimAcked)},
			assert: rejected,
		},
		{
			// Exact trimming with nothing to trim towards is meaningless, like a
			// trim mode with no bound.
			name:   "exact trim without a bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithExactTrim()},
			assert: rejected,
		},
		{
			name:   "length bound trimmed exactly",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxLen(1000),
				hmntskredis.WithExactTrim(),
			},
			assert: accepted,
		},
		{
			name:   "age bound trimmed exactly under a trim mode",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxAge(time.Hour),
				hmntskredis.WithTrimMode(hmntskredis.TrimAcked),
				hmntskredis.WithExactTrim(),
			},
			assert: accepted,
		},
		{
			name:   "undefined trim mode in the wrong case",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxLen(1000),
				hmntskredis.WithTrimMode("acked"),
			},
			assert: rejected,
		},
		{
			name:   "undefined trim mode",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxLen(1000),
				hmntskredis.WithTrimMode("BOGUS"),
			},
			assert: rejected,
		},
		{
			name:   "length bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithMaxLen(1000)},
			assert: accepted,
		},
		{
			name:   "age bound",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithMaxAge(time.Hour)},
			assert: accepted,
		},
		{
			name:   "length bound keeping references",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxLen(1000),
				hmntskredis.WithTrimMode(hmntskredis.TrimKeepRef),
			},
			assert: accepted,
		},
		{
			name:   "length bound deleting references",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxLen(1000),
				hmntskredis.WithTrimMode(hmntskredis.TrimDelRef),
			},
			assert: accepted,
		},
		{
			name:   "age bound trimming only acknowledged entries",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxAge(time.Hour),
				hmntskredis.WithTrimMode(hmntskredis.TrimAcked),
			},
			assert: accepted,
		},
		{
			name:   "nil clock is ignored",
			client: client,
			opts: []hmntskredis.Option{
				hmntskredis.WithMaxAge(time.Hour),
				hmntskredis.WithClock(nil),
			},
			assert: accepted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink, err := hmntskredis.New(tc.client, tc.opts...)
			tc.assert(t, sink, err)
		})
	}
}
