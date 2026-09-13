package notify_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// runHub runs a hub on its own goroutine until the test ends, and waits until
// the hub reports that it is running.
func runHub(t *testing.T, hub *notify.Hub) (stop func() error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- hub.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for !hub.Running() {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the hub never reported running")
		}

		time.Sleep(time.Millisecond)
	}

	var once sync.Once

	var result error

	stop = func() error {
		once.Do(func() {
			cancel()

			select {
			case result = <-done:
			case <-time.After(2 * time.Second):
				t.Error("Run did not return after cancellation")
			}
		})

		return result
	}

	t.Cleanup(func() { _ = stop() })

	return stop
}

// ready reports whether a subscription has a pending signal, without waiting.
func ready(subscription *notify.Subscription) bool {
	select {
	case <-subscription.Ready():
		return true
	default:
		return false
	}
}

// awaitSubscribed broadcasts probes to a recipient until the subscription sees
// one, because the hub's listener registers asynchronously, and then drains it.
func awaitSubscribed(t *testing.T, broadcaster notify.Broadcaster, subscription *notify.Subscription, recipient string) {
	t.Helper()

	probe := notify.Signal{Recipient: recipient, Change: notify.ChangeCreated, At: serviceAt}
	deadline := time.Now().Add(2 * time.Second)

	for {
		require.NoError(t, broadcaster.Broadcast(t.Context(), []notify.Signal{probe}))

		select {
		case <-subscription.Ready():
			_, ok := subscription.Take()
			require.True(t, ok)

			return
		case <-time.After(5 * time.Millisecond):
		}

		if time.Now().After(deadline) {
			t.Fatalf("the subscription for %s never received a signal", recipient)
		}
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
					case <-time.After(2 * time.Second):
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
