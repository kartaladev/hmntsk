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
			assert: func(t *testing.T, sink *hmntskredis.Sink, err error) {
				require.ErrorIs(t, err, hmntskredis.ErrConfiguration)
				assert.Nil(t, sink)
			},
		},
		{
			name:   "empty stream",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithStream("")},
			assert: func(t *testing.T, sink *hmntskredis.Sink, err error) {
				require.ErrorIs(t, err, hmntskredis.ErrConfiguration)
				assert.Nil(t, sink)
			},
		},
		{
			name:   "empty name",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithName("")},
			assert: func(t *testing.T, sink *hmntskredis.Sink, err error) {
				require.ErrorIs(t, err, hmntskredis.ErrConfiguration)
				assert.Nil(t, sink)
			},
		},
		{
			name:   "non-positive timeout",
			client: client,
			opts:   []hmntskredis.Option{hmntskredis.WithTimeout(0)},
			assert: func(t *testing.T, sink *hmntskredis.Sink, err error) {
				require.ErrorIs(t, err, hmntskredis.ErrConfiguration)
				assert.Nil(t, sink)
			},
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
