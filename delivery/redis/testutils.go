package redis

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RedisImage is the Redis image the sink is tested against.
//
// It is pinned. A moving tag would let a remote image update change what these
// tests mean overnight, on a Tuesday, for no reason anybody could find.
const RedisImage = "redis:8.2.9-alpine"

// testConfig is the provisioning a caller can vary.
type testConfig struct {
	image   string
	startup time.Duration
}

// TestOption varies how a test broker is provisioned.
type TestOption func(*testConfig)

// WithTestImage overrides the pinned image, for testing against another
// version.
func WithTestImage(image string) TestOption {
	return func(c *testConfig) {
		if image != "" {
			c.image = image
		}
	}
}

// WithTestStartupTimeout overrides how long the helper waits for readiness.
func WithTestStartupTimeout(timeout time.Duration) TestOption {
	return func(c *testConfig) {
		if timeout > 0 {
			c.startup = timeout
		}
	}
}

// RunTestRedis starts a Redis container and returns a client connected to it.
//
// A client rather than an address: it is the highest-level thing a caller
// wants, and nobody testing a sink should have to know that a container was
// involved. The address is still reachable through the client's own options
// when a test needs to put something between itself and the broker.
//
// The container lives as long as the test. Termination is registered with
// t.Cleanup the instant the container starts, before anything that could fail,
// so a failing connection cannot leak it.
func RunTestRedis(t *testing.T, opts ...TestOption) *goredis.Client {
	t.Helper()

	cfg := &testConfig{
		image:   RedisImage,
		startup: 2 * time.Minute,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	container, err := tcredis.Run(t.Context(), cfg.image,
		testcontainers.WithWaitStrategy(
			// The log line says the server has finished loading; the listening
			// port says the mapped port is actually reachable from here. A
			// container that is running is not the same as a broker that will
			// answer, and either check alone has let one through.
			wait.ForAll(
				wait.ForLog("Ready to accept connections").
					WithStartupTimeout(cfg.startup),
				wait.ForListeningPort("6379/tcp").
					WithStartupTimeout(cfg.startup),
			),
		),
	)
	require.NoError(t, err, "start the Redis test container")

	t.Cleanup(func() {
		// Not t.Context(): it is already cancelled by the time cleanup runs,
		// and Terminate would quietly do nothing.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminate the Redis test container: %s", err)
		}
	})

	uri, err := container.ConnectionString(t.Context())
	require.NoError(t, err, "read the Redis connection string")

	options, err := goredis.ParseURL(uri)
	require.NoError(t, err, "parse the Redis connection string")

	client := goredis.NewClient(options)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close the Redis test client: %s", err)
		}
	})

	require.NoError(t, client.Ping(t.Context()).Err(), "ping the Redis test container")

	return client
}
