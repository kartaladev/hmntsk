package redis_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/redis"
)

// instance is one application instance: its own client, store, service and
// hub, sharing a broker with the others.
type instance struct {
	svc          *notify.Service
	url          string
	signalErrors chan error
	published    atomic.Int64
}

// startInstance starts an instance on its own connection to the broker, with
// its hub receiving signals on channel.
func startInstance(t *testing.T, broker *goredis.Client, channel string, opts ...redis.Option) *instance {
	t.Helper()

	in := &instance{signalErrors: make(chan error, 64)}

	var err error

	in.svc, err = notify.New(notify.NewMemoryStore(),
		notify.WithBroadcaster(newBroadcaster(t, broker, channel, opts...)),
		notify.WithSignalErrorHandler(func(_ context.Context, err error) { in.signalErrors <- err }),
	)
	require.NoError(t, err)

	hub, err := notify.NewHub(in.svc.Broadcaster())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	var runErr error

	go func() {
		defer close(done)

		runErr = hub.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	handler, err := notify.NewHandler(in.svc, hub,
		notify.WithActor(func(r *http.Request) (string, error) { return r.Header.Get("X-Actor"), nil }))
	require.NoError(t, err)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	in.url = server.URL

	select {
	case <-hub.Ready():
	case <-done:
		require.FailNowf(t, "the hub stopped before it became ready", "%v", runErr)
	case <-time.After(testWait):
		require.FailNow(t, "the hub did not become ready")
	}

	return in
}

// publish stores a new notification for a recipient on the instance.
func (in *instance) publish(t *testing.T, recipient string) error {
	t.Helper()

	_, err := in.svc.Publish(t.Context(), notify.Draft{
		Recipient: recipient,
		SourceID:  fmt.Sprintf("event-%d", in.published.Add(1)),
		Subject:   "task-42",
		Kind:      "offer",
		Title:     "Approve invoice INV-42",
	})

	return err
}

// stream is an open server-sent event stream, with the names of the events it
// received collected on a channel.
type stream struct{ events chan string }

// openStream opens a recipient's stream on the instance until the test ends.
func (in *instance) openStream(t *testing.T, recipient string) *stream {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.url+"/v1/notifications/stream", http.NoBody)
	require.NoError(t, err)

	req.Header.Set("X-Actor", recipient)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	s := &stream{events: make(chan string, 64)}

	var wg sync.WaitGroup

	wg.Go(func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if name, ok := strings.CutPrefix(scanner.Text(), "event: "); ok {
				s.events <- name
			}
		}
	})

	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
		wg.Wait()
	})

	return s
}

// awaitEvent publishes on from until the stream receives an event, which also
// proves the receiving instance's subscription is live.
func awaitEvent(t *testing.T, from *instance, s *stream, recipient string) {
	t.Helper()

	require.Eventually(t, func() bool {
		if from.publish(t, recipient) != nil {
			return false
		}

		select {
		case <-s.events:
			return true
		case <-time.After(testTick):
			return false
		}
	}, testWait, testTick, "a notification published on one instance reaches a stream on another")

	for {
		select {
		case <-s.events:
		case <-time.After(3 * testTick):
			return
		}
	}
}

func TestSignalsCrossInstances(t *testing.T) {
	t.Parallel()

	broker := redis.RunTestRedis(t)

	type testCase struct {
		name   string
		act    func(t *testing.T) (publisher *instance, s *stream)
		assert func(t *testing.T, publisher *instance, s *stream)
	}

	cases := []testCase{
		{
			name: "a publish on A reaches a stream on B",
			act: func(t *testing.T) (*instance, *stream) {
				a, b := startInstance(t, broker, "shared.a-to-b"), startInstance(t, broker, "shared.a-to-b")

				return a, b.openStream(t, "alice")
			},
			assert: func(t *testing.T, publisher *instance, s *stream) {
				awaitEvent(t, publisher, s, "alice")
			},
		},
		{
			name: "a publish on B reaches a stream on A",
			act: func(t *testing.T) (*instance, *stream) {
				a, b := startInstance(t, broker, "shared.b-to-a"), startInstance(t, broker, "shared.b-to-a")

				return b, a.openStream(t, "alice")
			},
			assert: func(t *testing.T, publisher *instance, s *stream) {
				awaitEvent(t, publisher, s, "alice")
			},
		},
		{
			name: "instances on different channels do not see each other",
			act: func(t *testing.T) (*instance, *stream) {
				a, b := startInstance(t, broker, "app-one.signals"), startInstance(t, broker, "app-two.signals")

				s := b.openStream(t, "alice")
				// B's own publish proves its listener is live before A's is
				// expected to go unseen.
				awaitEvent(t, b, s, "alice")

				return a, s
			},
			assert: func(t *testing.T, publisher *instance, s *stream) {
				for range 5 {
					require.NoError(t, publisher.publish(t, "alice"))
				}

				select {
				case name := <-s.events:
					assert.Failf(t, "B received a signal from A", "event %q", name)
				case <-time.After(10 * testTick):
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			publisher, s := tc.act(t)
			tc.assert(t, publisher, s)
		})
	}
}

// TestPublishingSurvivesABrokerOutage stops the broker and proves a publish
// still stores the notification and succeeds, with the broadcast failure handed
// to the service's signal error handler.
func TestPublishingSurvivesABrokerOutage(t *testing.T) {
	t.Parallel()

	var container testcontainers.Container

	broker := redis.RunTestRedis(t, redis.WithTestContainer(func(c testcontainers.Container) { container = c }))
	in := startInstance(t, broker, "outage.signals", redis.WithPublishTimeout(500*time.Millisecond))

	stop := 5 * time.Second
	require.NoError(t, container.Stop(t.Context(), &stop))

	require.NoError(t, in.publish(t, "alice"), "the publish succeeds while the broker is down")

	count, err := in.svc.CountActive(t.Context(), "alice")
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "the notification is stored")

	select {
	case signalErr := <-in.signalErrors:
		require.ErrorIs(t, signalErr, redis.ErrPublish)
	case <-time.After(testWait):
		require.FailNow(t, "the broadcast failure was not reported to the signal error handler")
	}
}
