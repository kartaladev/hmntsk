package tasknotify

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
)

// reports records the failures a projector reports to its error handler.
type reports struct {
	mu   sync.Mutex
	errs []error
}

func (r *reports) handle(_ context.Context, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.errs = append(r.errs, err)
}

func (r *reports) all() []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]error(nil), r.errs...)
}

// wiring is what a delivery test builds a projector from.
type wiring struct {
	// resolver, when set, is the engine's directory; nil wires none.
	resolver func(ctrl *gomock.Controller) hmntsk.GroupResolver
	// store, when set, replaces the in-memory notification store.
	store func(ctrl *gomock.Controller) notify.Store
	opts  []Option
}

// build wires a projector, returning it with the recorder of its reports.
func (w wiring) build(t *testing.T) (*Projector, *reports) {
	t.Helper()

	ctrl := gomock.NewController(t)

	var engineOpts []hmntsk.Option
	if w.resolver != nil {
		engineOpts = append(engineOpts, hmntsk.WithGroupResolver(w.resolver(ctrl)))
	}

	engine, err := hmntsk.New(memstore.New(), engineOpts...)
	require.NoError(t, err)

	var store notify.Store = notify.NewMemoryStore()
	if w.store != nil {
		store = w.store(ctrl)
	}

	notifier, err := notify.New(store)
	require.NoError(t, err)

	recorded := &reports{}

	projector, err := New(engine, notifier, append([]Option{WithErrorHandler(recorded.handle)}, w.opts...)...)
	require.NoError(t, err)

	return projector, recorded
}

var errStoreDown = errors.New("connection refused")

func TestDeliverClassifiesOutcomes(t *testing.T) {
	t.Parallel()

	static := func(*gomock.Controller) hmntsk.GroupResolver {
		return hmntsk.NewStaticAssignment(map[string][]string{"approvers": {"bob", "carol"}})
	}

	pooled := event(hmntsk.EventTypeCreated, hmntsk.StatusReady, "admin", func(e *hmntsk.Event) {
		e.Candidates = hmntsk.CandidatePool{Users: []string{"alice"}, Groups: []string{"approvers"}}
	})

	claimed := event(hmntsk.EventTypeClaimed, hmntsk.StatusReserved, "bob", func(e *hmntsk.Event) {
		e.Assignee = "bob"
	})

	type testCase struct {
		name   string
		wiring wiring
		event  hmntsk.Event
		// ctx derives the context Deliver runs with.
		ctx    func(t *testing.T) context.Context
		assert func(t *testing.T, outcome relay.Outcome, reported []error)
	}

	delivered := func(t *testing.T, outcome relay.Outcome, reported []error) {
		t.Helper()

		assert.Equal(t, relay.OutcomeDelivered, outcome.Status)
		assert.NoError(t, outcome.Err)
		assert.Empty(t, reported)
	}

	retryable := func(target error) func(t *testing.T, outcome relay.Outcome, reported []error) {
		return func(t *testing.T, outcome relay.Outcome, reported []error) {
			t.Helper()

			assert.Equal(t, relay.OutcomeRetryable, outcome.Status)
			require.ErrorIs(t, outcome.Err, target)
			assert.Empty(t, reported, "a retryable failure is the relay's to record, not the handler's")
		}
	}

	reportedAs := func(target error) func(t *testing.T, outcome relay.Outcome, reported []error) {
		return func(t *testing.T, outcome relay.Outcome, reported []error) {
			t.Helper()

			assert.Equal(t, relay.OutcomeDelivered, outcome.Status,
				"a failure no retry can change must not dead-letter the event for every other sink")
			require.Len(t, reported, 1)
			assert.ErrorIs(t, reported[0], target)
		}
	}

	cases := []testCase{
		{
			name:   "a projected event is delivered",
			wiring: wiring{resolver: static},
			event:  pooled,
			assert: delivered,
		},
		{
			name:   "an event the rules ignore is delivered",
			event:  event(hmntsk.EventTypeStarted, hmntsk.StatusInProgress, "bob", nil),
			assert: delivered,
		},
		{
			name: "an unavailable store is retried",
			wiring: wiring{store: func(ctrl *gomock.Controller) notify.Store {
				store := NewMockStore(ctrl)
				store.EXPECT().Close(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(notify.CloseResult{}, notify.ErrUnavailable)

				return store
			}},
			event:  claimed,
			assert: retryable(notify.ErrUnavailable),
		},
		{
			name: "a driver error is retried",
			wiring: wiring{store: func(ctrl *gomock.Controller) notify.Store {
				store := NewMockStore(ctrl)
				store.EXPECT().Close(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(notify.CloseResult{}, errStoreDown)

				return store
			}},
			event:  claimed,
			assert: retryable(errStoreDown),
		},
		{
			name:  "a passed deadline is retried",
			event: claimed,
			wiring: wiring{store: func(ctrl *gomock.Controller) notify.Store {
				store := NewMockStore(ctrl)
				store.EXPECT().Close(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ notify.CloseRequest, _ time.Time, _ notify.IDGenerator) (notify.CloseResult, error) {
						return notify.CloseResult{}, ctx.Err()
					})

				return store
			}},
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(t.Context(), 0)
				t.Cleanup(cancel)

				return ctx
			},
			assert: retryable(context.DeadlineExceeded),
		},
		{
			name: "a directory failure is retried",
			wiring: wiring{resolver: func(ctrl *gomock.Controller) hmntsk.GroupResolver {
				resolver := NewMockGroupResolver(ctrl)
				resolver.EXPECT().MembersOf(gomock.Any(), "approvers").Return(nil, errStoreDown)

				return resolver
			}},
			event:  pooled,
			assert: retryable(hmntsk.ErrGroupResolution),
		},
		{
			name:   "a pool with groups and no directory is reported, not retried",
			event:  pooled,
			assert: reportedAs(hmntsk.ErrConfiguration),
		},
		{
			name: "invalid data from the host is reported, not retried",
			wiring: wiring{resolver: static, opts: []Option{
				WithData(func(context.Context, DraftInput) (json.RawMessage, error) { return json.RawMessage("not json"), nil }),
			}},
			event:  pooled,
			assert: reportedAs(notify.ErrValidation),
		},
		{
			name: "a plan with a draft on another task is reported, not retried",
			wiring: wiring{opts: []Option{WithRules(RulesFunc(func(ctx context.Context, in Input) (Plan, error) {
				draft, err := in.Draft(ctx, "alice", KindOffer)
				if err != nil {
					return Plan{}, err
				}

				draft.Subject = "task-other"

				return Plan{Steps: []Step{{Publish: []notify.Draft{draft}}}}, nil
			}))}},
			event:  pooled,
			assert: reportedAs(ErrInvalidPlan),
		},
		{
			name: "a plan with a close on another task is reported, not retried",
			wiring: wiring{opts: []Option{WithRules(RulesFunc(func(context.Context, Input) (Plan, error) {
				return Plan{Steps: []Step{{Close: &notify.CloseRequest{Subject: "task-other", Version: 5}}}}, nil
			}))}},
			event:  pooled,
			assert: reportedAs(ErrInvalidPlan),
		},
		{
			name: "a step that is neither a close nor a publish is reported, not retried",
			wiring: wiring{opts: []Option{WithRules(RulesFunc(func(context.Context, Input) (Plan, error) {
				return Plan{Steps: []Step{{}}}, nil
			}))}},
			event:  pooled,
			assert: reportedAs(ErrInvalidPlan),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			projector, recorded := tc.wiring.build(t)

			ctx := t.Context()
			if tc.ctx != nil {
				ctx = tc.ctx(t)
			}

			outcome := projector.Deliver(ctx, relay.Attempt{Event: tc.event, DeliveryID: "delivery-1", Number: 1})

			assert.NotEqual(t, relay.OutcomePermanent, outcome.Status, "a projector never dead-letters an event")
			tc.assert(t, outcome, recorded.all())
		})
	}
}

func TestDeliverBatchesLargeFanOut(t *testing.T) {
	t.Parallel()

	members := []string{"a1", "a2", "a3", "a4", "a5"}

	var (
		mu      sync.Mutex
		batches []int
	)

	projector, recorded := wiring{
		resolver: func(*gomock.Controller) hmntsk.GroupResolver {
			return hmntsk.NewStaticAssignment(map[string][]string{"everyone": members})
		},
		store: func(ctrl *gomock.Controller) notify.Store {
			store := NewMockStore(ctrl)
			store.EXPECT().Insert(gomock.Any(), "task-1", gomock.Any()).Times(3).
				DoAndReturn(func(_ context.Context, _ string, insertions []notify.Insertion) (notify.InsertResult, error) {
					mu.Lock()
					defer mu.Unlock()

					batches = append(batches, len(insertions))

					return notify.InsertResult{}, nil
				})

			return store
		},
		opts: []Option{WithPublishBatch(2)},
	}.build(t)

	outcome := projector.Deliver(t.Context(), relay.Attempt{
		Event: event(hmntsk.EventTypeCreated, hmntsk.StatusReady, "admin", func(e *hmntsk.Event) {
			e.Candidates = hmntsk.CandidatePool{Groups: []string{"everyone"}}
		}),
		DeliveryID: "delivery-1", Number: 1,
	})

	assert.Equal(t, relay.OutcomeDelivered, outcome.Status)
	assert.Empty(t, recorded.all())
	assert.Equal(t, []int{2, 2, 1}, batches, "five drafts in batches of two are three publishes, in order")
}
