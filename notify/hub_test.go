package notify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk/notify"
)

// hubWait bounds every wait in the hub tests. A hub answers far sooner; the
// bound only turns a hang into a failure.
const hubWait = 2 * time.Second

// startedRun is one hub.Run on its own goroutine.
type startedRun struct {
	cancel context.CancelFunc

	// exited is closed once Run has returned, after err is set.
	exited chan struct{}
	err    error

	// ready is the ready function a gated broadcaster handed over, for
	// startGated's runs.
	ready func()
}

// startRun runs a hub on its own goroutine and stops it at cleanup.
func startRun(t *testing.T, hub *notify.Hub) *startedRun {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	run := &startedRun{cancel: cancel, exited: make(chan struct{})}

	go func() {
		run.err = hub.Run(ctx)
		close(run.exited)
	}()

	t.Cleanup(func() { _ = run.stop(t) })

	return run
}

// stop cancels the run and returns Run's error. It is safe to call more than
// once.
func (r *startedRun) stop(t *testing.T) error {
	t.Helper()

	r.cancel()

	select {
	case <-r.exited:
		return r.err
	case <-time.After(hubWait):
		t.Error("Run did not return after cancellation")

		return nil
	}
}

// runHub runs a hub until the test ends, and waits until the hub is ready.
func runHub(t *testing.T, hub *notify.Hub) (stop func() error) {
	t.Helper()

	run := startRun(t, hub)

	select {
	case <-hub.Ready():
	case <-run.exited:
		t.Fatalf("the hub stopped before it was ready: %v", run.err)
	case <-time.After(hubWait):
		t.Fatal("the hub never became ready")
	}

	return func() error { return run.stop(t) }
}

// ready reports whether a subscription has a pending signal, without waiting.
func ready(subscription *notify.Subscription) bool {
	return closed(subscription.Ready())
}

// awaitSubscribed broadcasts a probe to a recipient, waits for the subscription
// to see it, and drains it. The hub is ready, so the first broadcast arrives.
func awaitSubscribed(t *testing.T, broadcaster notify.Broadcaster, subscription *notify.Subscription, recipient string) {
	t.Helper()

	probe := notify.Signal{Recipient: recipient, Change: notify.ChangeCreated, At: serviceAt}
	require.NoError(t, broadcaster.Broadcast(t.Context(), []notify.Signal{probe}))

	select {
	case <-subscription.Ready():
		_, ok := subscription.Take()
		require.True(t, ok)
	case <-time.After(hubWait):
		t.Fatalf("the subscription for %s never received a signal", recipient)
	}
}

func TestNewHub(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name        string
		broadcaster notify.Broadcaster
		opts        []notify.HubOption
		assert      func(t *testing.T, hub *notify.Hub, err error)
	}

	refused := func(t *testing.T, hub *notify.Hub, err error) {
		t.Helper()

		require.ErrorIs(t, err, notify.ErrConfiguration)
		assert.Nil(t, hub)
	}

	cases := []testCase{
		{name: "a nil broadcaster is a configuration error", assert: refused},
		{
			name:        "with no options it heartbeats every 25s, times writes out after 10s and caps 8 streams",
			broadcaster: notify.NewInProcessBroadcaster(),
			assert: func(t *testing.T, hub *notify.Hub, err error) {
				require.NoError(t, err)
				assert.Equal(t, 25*time.Second, hub.Heartbeat())
				assert.Equal(t, 10*time.Second, hub.WriteTimeout())
				assert.Equal(t, 25*time.Second, notify.DefaultHeartbeat)
				assert.Equal(t, 10*time.Second, notify.DefaultWriteTimeout)

				runHub(t, hub)

				for range notify.DefaultMaxStreamsPerRecipient {
					_, err := hub.Subscribe("alice")
					require.NoError(t, err)
				}

				_, err = hub.Subscribe("alice")
				assert.ErrorIs(t, err, notify.ErrTooManyStreams)
				assert.Equal(t, 8, notify.DefaultMaxStreamsPerRecipient)
			},
		},
		{
			name:        "options replace the defaults",
			broadcaster: notify.NewInProcessBroadcaster(),
			opts: []notify.HubOption{
				notify.WithHeartbeat(time.Second), notify.WithWriteTimeout(2 * time.Second), notify.WithMaxStreamsPerRecipient(1),
			},
			assert: func(t *testing.T, hub *notify.Hub, err error) {
				require.NoError(t, err)
				assert.Equal(t, time.Second, hub.Heartbeat())
				assert.Equal(t, 2*time.Second, hub.WriteTimeout())

				runHub(t, hub)

				_, err = hub.Subscribe("alice")
				require.NoError(t, err)

				_, err = hub.Subscribe("alice")
				assert.ErrorIs(t, err, notify.ErrTooManyStreams)
			},
		},
		{
			name: "a non-positive heartbeat is a configuration error", broadcaster: notify.NewInProcessBroadcaster(),
			opts: []notify.HubOption{notify.WithHeartbeat(0)}, assert: refused,
		},
		{
			name: "a non-positive write timeout is a configuration error", broadcaster: notify.NewInProcessBroadcaster(),
			opts: []notify.HubOption{notify.WithWriteTimeout(-time.Second)}, assert: refused,
		},
		{
			name: "a stream cap below one is a configuration error", broadcaster: notify.NewInProcessBroadcaster(),
			opts: []notify.HubOption{notify.WithMaxStreamsPerRecipient(0)}, assert: refused,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hub, err := notify.NewHub(tc.broadcaster, tc.opts...)
			tc.assert(t, hub, err)
		})
	}
}

func TestHubSubscribe(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		assert func(t *testing.T, hub *notify.Hub)
	}

	cases := []testCase{
		{
			name: "a hub that is not running refuses a subscription as unavailable",
			assert: func(t *testing.T, hub *notify.Hub) {
				subscription, err := hub.Subscribe("alice")
				require.ErrorIs(t, err, notify.ErrUnavailable)
				assert.Nil(t, subscription)
			},
		},
		{
			name: "a subscription over the cap is refused, and another recipient is unaffected",
			assert: func(t *testing.T, hub *notify.Hub) {
				runHub(t, hub)

				for range 2 {
					_, err := hub.Subscribe("alice")
					require.NoError(t, err)
				}

				_, err := hub.Subscribe("alice")
				require.ErrorIs(t, err, notify.ErrTooManyStreams)

				_, err = hub.Subscribe("bob")
				assert.NoError(t, err)
			},
		},
		{
			name: "closing a subscription releases its slot once, however often it is closed",
			assert: func(t *testing.T, hub *notify.Hub) {
				runHub(t, hub)

				first, err := hub.Subscribe("alice")
				require.NoError(t, err)

				_, err = hub.Subscribe("alice")
				require.NoError(t, err)

				first.Close()
				first.Close()

				_, err = hub.Subscribe("alice")
				require.NoError(t, err, "the closed slot is free again")

				_, err = hub.Subscribe("alice")
				assert.ErrorIs(t, err, notify.ErrTooManyStreams, "closing twice freed only one slot")
			},
		},
		{
			name: "a hub that has stopped refuses a subscription as unavailable",
			assert: func(t *testing.T, hub *notify.Hub) {
				stop := runHub(t, hub)

				assert.ErrorIs(t, stop(), context.Canceled)
				assert.False(t, hub.Running())

				_, err := hub.Subscribe("alice")
				assert.ErrorIs(t, err, notify.ErrUnavailable)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hub, err := notify.NewHub(notify.NewInProcessBroadcaster(), notify.WithMaxStreamsPerRecipient(2))
			require.NoError(t, err)

			tc.assert(t, hub)
		})
	}
}

func TestHubDelivery(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		assert func(t *testing.T, broadcaster *notify.InProcessBroadcaster, hub *notify.Hub)
	}

	cases := []testCase{
		{
			name: "a signal reaches every subscription of its recipient and no one else's",
			assert: func(t *testing.T, broadcaster *notify.InProcessBroadcaster, hub *notify.Hub) {
				one, err := hub.Subscribe("alice")
				require.NoError(t, err)

				two, err := hub.Subscribe("alice")
				require.NoError(t, err)

				bobs, err := hub.Subscribe("bob")
				require.NoError(t, err)

				awaitSubscribed(t, broadcaster, one, "alice")
				_, _ = two.Take()

				signal := notify.Signal{Recipient: "alice", Change: notify.ChangeRead, At: serviceAt.Add(time.Minute)}
				require.NoError(t, broadcaster.Broadcast(t.Context(), []notify.Signal{signal}))

				for _, subscription := range []*notify.Subscription{one, two} {
					select {
					case <-subscription.Ready():
						got, ok := subscription.Take()
						require.True(t, ok)
						assert.Equal(t, signal, got)
					case <-time.After(hubWait):
						t.Fatal("an alice subscription did not receive the signal")
					}
				}

				assert.False(t, ready(bobs), "bob's subscription received nothing")
			},
		},
		{
			name: "a thousand signals to a subscription nobody reads never block and leave the latest pending",
			assert: func(t *testing.T, broadcaster *notify.InProcessBroadcaster, hub *notify.Hub) {
				subscription, err := hub.Subscribe("alice")
				require.NoError(t, err)

				awaitSubscribed(t, broadcaster, subscription, "alice")

				var last notify.Signal

				published := make(chan struct{})

				go func() {
					defer close(published)

					for i := range 1000 {
						last = notify.Signal{Recipient: "alice", Change: notify.ChangeCreated, At: serviceAt.Add(time.Duration(i) * time.Millisecond)}
						_ = broadcaster.Broadcast(context.Background(), []notify.Signal{last})
					}
				}()

				select {
				case <-published:
				case <-time.After(5 * time.Second):
					t.Fatal("broadcasting to a subscription nobody reads blocked")
				}

				require.True(t, ready(subscription), "one signal is pending")

				got, ok := subscription.Take()
				require.True(t, ok)
				assert.Equal(t, last, got, "the pending signal is the latest")

				_, ok = subscription.Take()
				assert.False(t, ok, "taking cleared it")
				assert.False(t, ready(subscription))
			},
		},
		{
			name: "taking with nothing pending reports false",
			assert: func(t *testing.T, _ *notify.InProcessBroadcaster, hub *notify.Hub) {
				subscription, err := hub.Subscribe("alice")
				require.NoError(t, err)

				_, ok := subscription.Take()
				assert.False(t, ok)
			},
		},
		{
			name: "a closed subscription receives nothing more",
			assert: func(t *testing.T, broadcaster *notify.InProcessBroadcaster, hub *notify.Hub) {
				closed, err := hub.Subscribe("alice")
				require.NoError(t, err)

				open, err := hub.Subscribe("alice")
				require.NoError(t, err)

				awaitSubscribed(t, broadcaster, open, "alice")
				_, _ = closed.Take()

				closed.Close()
				awaitSubscribed(t, broadcaster, open, "alice")

				_, ok := closed.Take()
				assert.False(t, ok)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			broadcaster := notify.NewInProcessBroadcaster()

			hub, err := notify.NewHub(broadcaster)
			require.NoError(t, err)

			runHub(t, hub)

			tc.assert(t, broadcaster, hub)
		})
	}
}

// startGated starts Run over a gated broadcaster, whose Listen hands the test
// its ready function instead of calling it, and waits for that hand-over.
func startGated(t *testing.T, hub *notify.Hub, readies <-chan func()) *startedRun {
	t.Helper()

	run := startRun(t, hub)

	select {
	case run.ready = <-readies:
	case <-run.exited:
		t.Fatalf("Run returned before Listen was called: %v", run.err)
	case <-time.After(hubWait):
		t.Fatal("Listen was never called")
	}

	return run
}

// closed reports whether a channel is closed, without waiting.
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// eventuallyClosed waits briefly for a channel to close.
func eventuallyClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(hubWait):
		return false
	}
}

func TestHubReadiness(t *testing.T) {
	t.Parallel()

	errListen := errors.New("the broker refused the subscription")

	type testCase struct {
		name string
		// listenErr, when set, is what the first Listen returns at once.
		listenErr error
		assert    func(t *testing.T, hub *notify.Hub, readies <-chan func())
	}

	cases := []testCase{
		{
			name: "before ready the hub is not running, refuses streams and is not ready",
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				readyCh := hub.Ready()
				startGated(t, hub, readies)

				assert.False(t, hub.Running())
				_, err := hub.Subscribe("alice")
				require.ErrorIs(t, err, notify.ErrUnavailable)
				assert.False(t, closed(readyCh))
			},
		},
		{
			name: "after ready the hub is running, accepts streams and Ready taken before Run is closed",
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				readyCh := hub.Ready()
				run := startGated(t, hub, readies)

				run.ready()

				assert.True(t, hub.Running())
				_, err := hub.Subscribe("alice")
				require.NoError(t, err)
				assert.True(t, closed(readyCh))
				assert.True(t, closed(hub.Ready()))
			},
		},
		{
			name: "after Listen returns the hub stops running and Ready waits for the next run",
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				first := startGated(t, hub, readies)
				first.ready()
				require.ErrorIs(t, first.stop(t), context.Canceled)

				assert.False(t, hub.Running())

				next := hub.Ready()
				assert.False(t, closed(next), "Ready after a run ends is open again")

				second := startGated(t, hub, readies)
				assert.False(t, closed(next))

				second.ready()
				assert.True(t, eventuallyClosed(next), "the next run's ready closes it")
				assert.True(t, hub.Running())
			},
		},
		{
			name:      "a Listen that fails before ready returns its error and never closes Ready",
			listenErr: errListen,
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				readyCh := hub.Ready()

				require.ErrorIs(t, hub.Run(t.Context()), errListen)

				assert.False(t, hub.Running())
				assert.False(t, closed(readyCh))
			},
		},
		{
			name: "ready called twice keeps one run receiving",
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				run := startGated(t, hub, readies)

				run.ready()
				run.ready()

				assert.True(t, hub.Running())
			},
		},
		{
			name: "a late ready after its run ended does not mark the hub running",
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				readyCh := hub.Ready()
				first := startGated(t, hub, readies)
				require.ErrorIs(t, first.stop(t), context.Canceled)

				first.ready()

				assert.False(t, hub.Running())
				assert.False(t, closed(readyCh))
			},
		},
		{
			name: "a late ready from an earlier run does not mark the next run ready",
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				first := startGated(t, hub, readies)
				require.ErrorIs(t, first.stop(t), context.Canceled)

				next := hub.Ready()
				startGated(t, hub, readies)

				first.ready()

				assert.False(t, hub.Running())
				assert.False(t, closed(next))
			},
		},
		{
			name: "a second Run while the first is starting is refused",
			assert: func(t *testing.T, hub *notify.Hub, readies <-chan func()) {
				startGated(t, hub, readies)

				assert.ErrorIs(t, hub.Run(t.Context()), notify.ErrConfiguration)
				assert.False(t, hub.Running(), "the first Run is still starting")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			broadcaster := notify.NewMockBroadcaster(ctrl)
			readies := make(chan func(), 1)

			if tc.listenErr != nil {
				broadcaster.EXPECT().Listen(gomock.Any(), gomock.Any(), gomock.Any()).Return(tc.listenErr)
			} else {
				broadcaster.EXPECT().Listen(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ func(notify.Signal), ready func()) error {
						readies <- ready
						<-ctx.Done()

						return ctx.Err()
					}).AnyTimes()
			}

			hub, err := notify.NewHub(broadcaster)
			require.NoError(t, err)

			tc.assert(t, hub, readies)
		})
	}
}

func TestHubRun(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		assert func(t *testing.T, hub *notify.Hub)
	}

	cases := []testCase{
		{
			name: "Run returns the context's error when cancelled and the hub stops running",
			assert: func(t *testing.T, hub *notify.Hub) {
				stop := runHub(t, hub)
				require.True(t, hub.Running())

				assert.ErrorIs(t, stop(), context.Canceled)
				assert.False(t, hub.Running())
			},
		},
		{
			name: "a second Run while the first is running is refused",
			assert: func(t *testing.T, hub *notify.Hub) {
				runHub(t, hub)

				assert.ErrorIs(t, hub.Run(t.Context()), notify.ErrConfiguration)
				assert.True(t, hub.Running(), "the first Run is unaffected")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hub, err := notify.NewHub(notify.NewInProcessBroadcaster())
			require.NoError(t, err)

			tc.assert(t, hub)
		})
	}
}
