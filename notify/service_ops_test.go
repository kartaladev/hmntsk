package notify_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk/notify"
)

func TestServiceClose(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		req    notify.CloseRequest
		expect func(t *testing.T, store *notify.MockStore, broadcaster *notify.MockBroadcaster)
		assert func(t *testing.T, result notify.CloseResult, err error)
	}

	cases := []testCase{
		{
			name:   "an invalid request closes nothing",
			req:    notify.CloseRequest{Version: -1},
			expect: func(*testing.T, *notify.MockStore, *notify.MockBroadcaster) {},
			assert: func(t *testing.T, _ notify.CloseResult, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name:   "an invalid successor closes nothing",
			req:    notify.CloseRequest{Subject: "task-1", Version: 1, Successor: &notify.Successor{Kind: "taken"}},
			expect: func(*testing.T, *notify.MockStore, *notify.MockBroadcaster) {},
			assert: func(t *testing.T, _ notify.CloseResult, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name: "a close runs at the clock's instant with the service's identifiers, and signals closes and successors",
			req: notify.CloseRequest{
				Subject: "task-1", Kinds: []string{"offer"}, Version: 5, Reason: "taken",
				Successor: &notify.Successor{SourceID: "event-5", Kind: "taken", SubjectVersion: 5},
			},
			expect: func(t *testing.T, store *notify.MockStore, broadcaster *notify.MockBroadcaster) {
				gomock.InOrder(
					store.EXPECT().Close(gomock.Any(), gomock.Any(), serviceAt, gomock.Any()).DoAndReturn(
						func(_ context.Context, req notify.CloseRequest, _ time.Time, ids notify.IDGenerator) (notify.CloseResult, error) {
							assert.Equal(t, "task-1", req.Subject)

							id, err := ids.NewID()
							require.NoError(t, err)
							assert.Equal(t, "id-1", id, "the store stamps successors with the service's generator")

							return notify.CloseResult{
								Closed: 3, Recipients: []string{"alice", "bob", "carol"},
								Successors: []notify.Notification{{Recipient: "alice"}, {Recipient: "bob"}},
							}, nil
						},
					),
					broadcaster.EXPECT().Broadcast(gomock.Any(), []notify.Signal{
						{Recipient: "alice", Change: notify.ChangeClosed, At: serviceAt},
						{Recipient: "bob", Change: notify.ChangeClosed, At: serviceAt},
						{Recipient: "carol", Change: notify.ChangeClosed, At: serviceAt},
						{Recipient: "alice", Change: notify.ChangeCreated, At: serviceAt},
						{Recipient: "bob", Change: notify.ChangeCreated, At: serviceAt},
					}).Return(nil),
				)
			},
			assert: func(t *testing.T, result notify.CloseResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(3), result.Closed)
				assert.Len(t, result.Successors, 2)
			},
		},
		{
			name: "a close that closed nothing signals nobody",
			req:  notify.CloseRequest{Subject: "task-1", Version: 5},
			expect: func(t *testing.T, store *notify.MockStore, _ *notify.MockBroadcaster) {
				store.EXPECT().Close(gomock.Any(), gomock.Any(), serviceAt, gomock.Any()).Return(notify.CloseResult{}, nil)
			},
			assert: func(t *testing.T, _ notify.CloseResult, err error) {
				assert.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, store, broadcaster, _ := mocked(t)
			tc.expect(t, store, broadcaster)

			result, err := svc.Close(t.Context(), tc.req)
			tc.assert(t, result, err)
		})
	}
}

func TestServiceMarkRead(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name      string
		recipient string
		ids       []string
		expect    func(t *testing.T, store *notify.MockStore, broadcaster *notify.MockBroadcaster)
		assert    func(t *testing.T, result notify.MarkResult, err error)
	}

	cases := []testCase{
		{
			name:   "a recipient is required",
			ids:    []string{"n-1"},
			expect: func(*testing.T, *notify.MockStore, *notify.MockBroadcaster) {},
			assert: func(t *testing.T, _ notify.MarkResult, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name:      "at least one identifier is required",
			recipient: "alice",
			expect:    func(*testing.T, *notify.MockStore, *notify.MockBroadcaster) {},
			assert: func(t *testing.T, _ notify.MarkResult, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name:      "marking read signals the recipient",
			recipient: "alice",
			ids:       []string{"n-1", "n-2"},
			expect: func(t *testing.T, store *notify.MockStore, broadcaster *notify.MockBroadcaster) {
				gomock.InOrder(
					store.EXPECT().MarkRead(gomock.Any(), "alice", []string{"n-1", "n-2"}, serviceAt).
						Return(notify.MarkResult{Marked: 2}, nil),
					broadcaster.EXPECT().Broadcast(gomock.Any(), []notify.Signal{
						{Recipient: "alice", Change: notify.ChangeRead, At: serviceAt},
					}).Return(nil),
				)
			},
			assert: func(t *testing.T, result notify.MarkResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(2), result.Marked)
			},
		},
		{
			name:      "marking what was already read signals nobody",
			recipient: "alice",
			ids:       []string{"n-1"},
			expect: func(t *testing.T, store *notify.MockStore, _ *notify.MockBroadcaster) {
				store.EXPECT().MarkRead(gomock.Any(), "alice", []string{"n-1"}, serviceAt).Return(notify.MarkResult{}, nil)
			},
			assert: func(t *testing.T, _ notify.MarkResult, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name:      "not found is returned as it is",
			recipient: "bob",
			ids:       []string{"n-1"},
			expect: func(t *testing.T, store *notify.MockStore, _ *notify.MockBroadcaster) {
				store.EXPECT().MarkRead(gomock.Any(), "bob", []string{"n-1"}, serviceAt).
					Return(notify.MarkResult{}, notify.ErrNotFound)
			},
			assert: func(t *testing.T, _ notify.MarkResult, err error) {
				assert.ErrorIs(t, err, notify.ErrNotFound)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, store, broadcaster, _ := mocked(t)
			tc.expect(t, store, broadcaster)

			result, err := svc.MarkRead(t.Context(), tc.recipient, tc.ids...)
			tc.assert(t, result, err)
		})
	}
}

func TestServiceMarkAllRead(t *testing.T) {
	t.Parallel()

	through := serviceAt.Add(-time.Hour)

	type testCase struct {
		name    string
		through time.Time
		expect  func(t *testing.T, store *notify.MockStore, broadcaster *notify.MockBroadcaster)
		assert  func(t *testing.T, result notify.MarkResult, err error)
	}

	cases := []testCase{
		{
			name:    "marking all read up to an instant signals the recipient",
			through: through,
			expect: func(t *testing.T, store *notify.MockStore, broadcaster *notify.MockBroadcaster) {
				gomock.InOrder(
					store.EXPECT().MarkAllRead(gomock.Any(), "alice", through, serviceAt).Return(notify.MarkResult{Marked: 4}, nil),
					broadcaster.EXPECT().Broadcast(gomock.Any(), []notify.Signal{
						{Recipient: "alice", Change: notify.ChangeRead, At: serviceAt},
					}).Return(nil),
				)
			},
			assert: func(t *testing.T, result notify.MarkResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, int64(4), result.Marked)
			},
		},
		{
			name: "no instant means now",
			expect: func(t *testing.T, store *notify.MockStore, _ *notify.MockBroadcaster) {
				store.EXPECT().MarkAllRead(gomock.Any(), "alice", serviceAt, serviceAt).Return(notify.MarkResult{}, nil)
			},
			assert: func(t *testing.T, _ notify.MarkResult, err error) {
				assert.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, store, broadcaster, _ := mocked(t)
			tc.expect(t, store, broadcaster)

			result, err := svc.MarkAllRead(t.Context(), "alice", tc.through)
			tc.assert(t, result, err)
		})
	}
}

func TestServiceReads(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		call   func(t *testing.T, svc *notify.Service) error
		expect func(t *testing.T, store *notify.MockStore)
	}

	cases := []testCase{
		{
			name: "get passes through",
			expect: func(t *testing.T, store *notify.MockStore) {
				store.EXPECT().Get(gomock.Any(), "alice", "n-1").Return(notify.Notification{ID: "n-1"}, nil)
			},
			call: func(t *testing.T, svc *notify.Service) error {
				n, err := svc.Get(t.Context(), "alice", "n-1")
				assert.Equal(t, "n-1", n.ID)

				return err
			},
		},
		{
			name:   "get needs a recipient and an identifier",
			expect: func(*testing.T, *notify.MockStore) {},
			call: func(t *testing.T, svc *notify.Service) error {
				_, err := svc.Get(t.Context(), "", "n-1")
				assert.ErrorIs(t, err, notify.ErrValidation)

				_, err = svc.Get(t.Context(), "alice", "")
				assert.ErrorIs(t, err, notify.ErrValidation)

				return nil
			},
		},
		{
			name: "list passes a valid query through",
			expect: func(t *testing.T, store *notify.MockStore) {
				store.EXPECT().List(gomock.Any(), notify.ListQuery{Recipient: "alice", Limit: 10}).
					Return(notify.Page{NextCursor: "next"}, nil)
			},
			call: func(t *testing.T, svc *notify.Service) error {
				page, err := svc.List(t.Context(), notify.ListQuery{Recipient: "alice", Limit: 10})
				assert.Equal(t, "next", page.NextCursor)

				return err
			},
		},
		{
			name:   "list refuses an invalid query without asking the store",
			expect: func(*testing.T, *notify.MockStore) {},
			call: func(t *testing.T, svc *notify.Service) error {
				_, err := svc.List(t.Context(), notify.ListQuery{Recipient: "alice", Limit: notify.MaxListLimit + 1})
				assert.ErrorIs(t, err, notify.ErrValidation)

				return nil
			},
		},
		{
			name: "count passes through",
			expect: func(t *testing.T, store *notify.MockStore) {
				store.EXPECT().CountActive(gomock.Any(), "alice").Return(int64(7), nil)
			},
			call: func(t *testing.T, svc *notify.Service) error {
				count, err := svc.CountActive(t.Context(), "alice")
				assert.Equal(t, int64(7), count)

				return err
			},
		},
		{
			name:   "count needs a recipient",
			expect: func(*testing.T, *notify.MockStore) {},
			call: func(t *testing.T, svc *notify.Service) error {
				_, err := svc.CountActive(t.Context(), "")
				assert.ErrorIs(t, err, notify.ErrValidation)

				return nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, store, _, _ := mocked(t)
			tc.expect(t, store)

			require.NoError(t, tc.call(t, svc))
		})
	}
}

// recording is a broadcaster that remembers every signal it is given.
type recording struct {
	mu      sync.Mutex
	signals []notify.Signal
}

// Broadcast implements notify.Broadcaster.
func (r *recording) Broadcast(_ context.Context, signals []notify.Signal) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.signals = append(r.signals, signals...)

	return nil
}

// Listen implements notify.Broadcaster.
func (r *recording) Listen(ctx context.Context, _ func(notify.Signal)) error {
	<-ctx.Done()

	return ctx.Err()
}

// changes returns the recorded changes for a recipient, in order.
func (r *recording) changes(recipient string) []notify.Change {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []notify.Change

	for _, signal := range r.signals {
		if signal.Recipient == recipient {
			out = append(out, signal.Change)
		}
	}

	return out
}

func TestServiceOnTheMemoryStore(t *testing.T) {
	t.Parallel()

	broadcaster := &recording{}

	svc, err := notify.New(notify.NewMemoryStore(), notify.WithBroadcaster(broadcaster))
	require.NoError(t, err)

	ctx := t.Context()

	published, err := svc.Publish(
		ctx,
		notify.Draft{Recipient: "alice", SourceID: "event-1", Subject: "task-1", Kind: "offer", SubjectVersion: 1},
		notify.Draft{Recipient: "bob", SourceID: "event-1", Subject: "task-1", Kind: "offer", SubjectVersion: 1},
	)
	require.NoError(t, err)
	require.Len(t, published.Created, 2)

	count, err := svc.CountActive(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	_, err = svc.MarkRead(ctx, "alice", published.Created[0].ID)
	require.NoError(t, err)

	closed, err := svc.Close(ctx, notify.CloseRequest{
		Subject: "task-1", Kinds: []string{"offer"}, Version: 2, Reason: "taken", SuccessorSkip: []string{"bob"},
		Successor: &notify.Successor{SourceID: "event-2", Kind: "taken", SubjectVersion: 2},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, closed.Recipients)
	require.Len(t, closed.Successors, 1)

	page, err := svc.List(ctx, notify.ListQuery{Recipient: "alice", States: []notify.State{notify.StateActive}})
	require.NoError(t, err)
	require.Len(t, page.Notifications, 1)
	assert.Equal(t, "taken", page.Notifications[0].Kind)

	assert.Equal(t, []notify.Change{notify.ChangeCreated, notify.ChangeRead, notify.ChangeClosed, notify.ChangeCreated},
		broadcaster.changes("alice"))
	assert.Equal(t, []notify.Change{notify.ChangeCreated, notify.ChangeClosed}, broadcaster.changes("bob"))
}
