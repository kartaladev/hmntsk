package notify_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// customBroadcaster is a broadcaster the host supplies, told apart from the
// default by its type.
type customBroadcaster struct{ notify.InProcessBroadcaster }

func TestNew(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)
	custom := &customBroadcaster{}

	draft := notify.Draft{Recipient: "alice", SourceID: "event-1", Subject: "task-1", Kind: "offer"}

	type testCase struct {
		name   string
		store  notify.Store
		opts   []notify.Option
		assert func(t *testing.T, svc *notify.Service, err error)
	}

	cases := []testCase{
		{
			name:  "a nil store is a configuration error",
			store: nil,
			assert: func(t *testing.T, svc *notify.Service, err error) {
				require.ErrorIs(t, err, notify.ErrConfiguration)
				assert.Nil(t, svc)
			},
		},
		{
			name:  "with no options it broadcasts in process, reads the system clock and mints UUIDv7 identifiers",
			store: notify.NewMemoryStore(),
			assert: func(t *testing.T, svc *notify.Service, err error) {
				require.NoError(t, err)
				assert.IsType(t, &notify.InProcessBroadcaster{}, svc.Broadcaster())

				result, err := svc.Publish(t.Context(), draft)
				require.NoError(t, err)
				require.Len(t, result.Created, 1)

				created := result.Created[0]
				assert.WithinDuration(t, time.Now(), created.CreatedAt, time.Minute)
				require.Len(t, created.ID, 36)
				assert.Equal(t, byte('7'), created.ID[14])
			},
		},
		{
			name:  "options replace the clock, the identifiers and the broadcaster",
			store: notify.NewMemoryStore(),
			opts: []notify.Option{
				notify.WithClock(notify.ClockFunc(func() time.Time { return fixed })),
				notify.WithIDGenerator(notify.IDGeneratorFunc(func() (string, error) { return "host-id", nil })),
				notify.WithBroadcaster(custom),
			},
			assert: func(t *testing.T, svc *notify.Service, err error) {
				require.NoError(t, err)
				assert.Same(t, custom, svc.Broadcaster())

				result, err := svc.Publish(t.Context(), draft)
				require.NoError(t, err)
				require.Len(t, result.Created, 1)
				assert.Equal(t, "host-id", result.Created[0].ID)
				assert.True(t, fixed.Equal(result.Created[0].CreatedAt))
			},
		},
		{
			name:  "nil options leave the defaults in place",
			store: notify.NewMemoryStore(),
			opts: []notify.Option{
				nil, notify.WithClock(nil), notify.WithIDGenerator(nil), notify.WithBroadcaster(nil),
				notify.WithSignalErrorHandler(nil),
			},
			assert: func(t *testing.T, svc *notify.Service, err error) {
				require.NoError(t, err)
				assert.IsType(t, &notify.InProcessBroadcaster{}, svc.Broadcaster())

				_, err = svc.Publish(context.WithoutCancel(t.Context()), draft)
				assert.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, err := notify.New(tc.store, tc.opts...)
			tc.assert(t, svc, err)
		})
	}
}
