package notify_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk/notify"
)

func TestNewPruner(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		nilSvc  bool
		opts    []notify.PruneOption
		request *notify.PruneRequest
		assert  func(t *testing.T, pruner *notify.Pruner, err error)
	}

	refused := func(t *testing.T, pruner *notify.Pruner, err error) {
		t.Helper()

		require.ErrorIs(t, err, notify.ErrConfiguration)
		assert.Nil(t, pruner)
	}

	built := func(t *testing.T, pruner *notify.Pruner, err error) {
		t.Helper()

		require.NoError(t, err)
		require.NotNil(t, pruner)

		_, err = pruner.Prune(t.Context())
		assert.NoError(t, err)
	}

	cases := []testCase{
		{name: "a nil service is a configuration error", nilSvc: true, assert: refused},
		{
			name: "with no options the documented defaults apply",
			request: &notify.PruneRequest{
				Now: serviceAt, MaxPerRecipient: 500, MaxAge: 90 * 24 * time.Hour, Strategy: notify.EvictOldestActive,
				WatermarkRetention: 7 * 24 * time.Hour, Batch: 1000,
			},
			assert: built,
		},
		{
			name: "options replace every default",
			opts: []notify.PruneOption{
				notify.WithMaxPerRecipient(100), notify.WithoutMaxAge(), notify.WithRetentionStrategy(notify.RetainActive),
				notify.WithWatermarkRetention(24 * time.Hour), notify.WithPruneBatch(50),
			},
			request: &notify.PruneRequest{
				Now: serviceAt, MaxPerRecipient: 100, Strategy: notify.RetainActive,
				WatermarkRetention: 24 * time.Hour, Batch: 50,
			},
			assert: built,
		},
		{
			name: "the count bound can be removed while the age bound stays",
			opts: []notify.PruneOption{notify.WithoutMaxPerRecipient(), notify.WithMaxAge(30 * 24 * time.Hour)},
			request: &notify.PruneRequest{
				Now: serviceAt, MaxAge: 30 * 24 * time.Hour, Strategy: notify.EvictOldestActive,
				WatermarkRetention: 7 * 24 * time.Hour, Batch: 1000,
			},
			assert: built,
		},
		{name: "a count bound of zero is refused", opts: []notify.PruneOption{notify.WithMaxPerRecipient(0)}, assert: refused},
		{name: "a negative count bound is refused", opts: []notify.PruneOption{notify.WithMaxPerRecipient(-1)}, assert: refused},
		{name: "an age bound of zero is refused", opts: []notify.PruneOption{notify.WithMaxAge(0)}, assert: refused},
		{name: "an unknown strategy is refused", opts: []notify.PruneOption{notify.WithRetentionStrategy("SOMETIMES")}, assert: refused},
		{
			name:   "a count bound both set and removed is refused",
			opts:   []notify.PruneOption{notify.WithMaxPerRecipient(5), notify.WithoutMaxPerRecipient()},
			assert: refused,
		},
		{
			name:   "an age bound both set and removed is refused",
			opts:   []notify.PruneOption{notify.WithMaxAge(time.Hour), notify.WithoutMaxAge()},
			assert: refused,
		},
		{
			name:   "a pruner with both bounds removed is refused",
			opts:   []notify.PruneOption{notify.WithoutMaxPerRecipient(), notify.WithoutMaxAge()},
			assert: refused,
		},
		{name: "a watermark retention of zero is refused", opts: []notify.PruneOption{notify.WithWatermarkRetention(0)}, assert: refused},
		{name: "a batch of zero is refused", opts: []notify.PruneOption{notify.WithPruneBatch(0)}, assert: refused},
		{name: "a nil option is ignored", opts: []notify.PruneOption{nil}, request: &notify.PruneRequest{
			Now: serviceAt, MaxPerRecipient: 500, MaxAge: 90 * 24 * time.Hour, Strategy: notify.EvictOldestActive,
			WatermarkRetention: 7 * 24 * time.Hour, Batch: 1000,
		}, assert: built},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, store, _, _ := mocked(t)
			if tc.request != nil {
				store.EXPECT().Prune(gomock.Any(), *tc.request).Return(notify.PruneResult{}, nil)
			}

			if tc.nilSvc {
				svc = nil
			}

			pruner, err := notify.NewPruner(svc, tc.opts...)
			tc.assert(t, pruner, err)
		})
	}
}

func TestPrunerPrune(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		expect func(store *notify.MockStore, broadcaster *notify.MockBroadcaster)
		assert func(t *testing.T, result notify.PruneResult, err error)
	}

	cases := []testCase{
		{
			name: "evicted active notifications signal their recipients",
			expect: func(store *notify.MockStore, broadcaster *notify.MockBroadcaster) {
				gomock.InOrder(
					store.EXPECT().Prune(gomock.Any(), gomock.Any()).Return(notify.PruneResult{
						DeletedForAge: 7, DeletedForCount: 3, EvictedActive: 2, Recipients: []string{"bob", "alice"},
					}, nil),
					broadcaster.EXPECT().Broadcast(gomock.Any(), []notify.Signal{
						{Recipient: "alice", Change: notify.ChangePruned, At: serviceAt},
						{Recipient: "bob", Change: notify.ChangePruned, At: serviceAt},
					}).Return(nil),
				)
			},
			assert: func(t *testing.T, result notify.PruneResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(7), result.DeletedForAge)
				assert.Equal(t, int64(3), result.DeletedForCount)
				assert.Equal(t, int64(2), result.EvictedActive)
			},
		},
		{
			name: "a pass that evicted nothing active signals nobody",
			expect: func(store *notify.MockStore, _ *notify.MockBroadcaster) {
				store.EXPECT().Prune(gomock.Any(), gomock.Any()).Return(notify.PruneResult{DeletedForAge: 4}, nil)
			},
			assert: func(t *testing.T, result notify.PruneResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(4), result.DeletedForAge)
			},
		},
		{
			name: "a store failure is returned",
			expect: func(store *notify.MockStore, _ *notify.MockBroadcaster) {
				store.EXPECT().Prune(gomock.Any(), gomock.Any()).Return(notify.PruneResult{}, errors.New("disk full"))
			},
			assert: func(t *testing.T, _ notify.PruneResult, err error) {
				assert.ErrorContains(t, err, "disk full")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, store, broadcaster, _ := mocked(t)
			tc.expect(store, broadcaster)

			pruner, err := notify.NewPruner(svc)
			require.NoError(t, err)

			result, err := pruner.Prune(t.Context())
			tc.assert(t, result, err)
		})
	}
}

func TestPrunerRun(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		run    func(t *testing.T, store *notify.MockStore, svc *notify.Service) error
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "a non-positive interval is a configuration error",
			run: func(t *testing.T, _ *notify.MockStore, svc *notify.Service) error {
				pruner, err := notify.NewPruner(svc)
				require.NoError(t, err)

				return pruner.Run(t.Context(), 0)
			},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, notify.ErrConfiguration)
			},
		},
		{
			name: "it prunes every interval, reports failed passes and stops when cancelled",
			run: func(t *testing.T, store *notify.MockStore, svc *notify.Service) error {
				var passes atomic.Int64

				failures := make(chan error, 16)

				store.EXPECT().Prune(gomock.Any(), gomock.Any()).DoAndReturn(
					func(context.Context, notify.PruneRequest) (notify.PruneResult, error) {
						passes.Add(1)

						return notify.PruneResult{}, errors.New("database is restarting")
					},
				).MinTimes(2)

				pruner, err := notify.NewPruner(svc, notify.WithPruneErrorHandler(func(_ context.Context, err error) {
					select {
					case failures <- err:
					default:
					}
				}))
				require.NoError(t, err)

				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)

				go func() { done <- pruner.Run(ctx, 5*time.Millisecond) }()

				for range 2 {
					select {
					case err := <-failures:
						assert.ErrorContains(t, err, "database is restarting")
					case <-time.After(5 * time.Second):
						t.Fatal("no pass reported its failure")
					}
				}

				cancel()

				select {
				case err := <-done:
					assert.GreaterOrEqual(t, passes.Load(), int64(2))

					return err
				case <-time.After(5 * time.Second):
					t.Fatal("Run did not return after cancellation")

					return nil
				}
			},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, context.Canceled)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, store, _, _ := mocked(t)

			tc.assert(t, tc.run(t, store, svc))
		})
	}
}
