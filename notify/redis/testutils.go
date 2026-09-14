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

// RedisImage is the Redis image the broadcaster is tested against. It is the
// image delivery/redis pins, copied rather than imported: this module moves to
// its own repository with notify and may import nothing that stays behind.
//
// It is pinned. A moving tag would let a remote image update change what these
// tests mean overnight.
const RedisImage = "redis:8.2.9-alpine"

// testConfig is the provisioning a caller can vary.
type testConfig struct {
	image     string
	startup   time.Duration
	container func(testcontainers.Container)
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

// WithTestContainer hands the started container to fn, for a test whose meaning
// depends on the broker going away, such as stopping it to prove that
// publishing survives an outage. The container is still terminated by the
// helper.
func WithTestContainer(fn func(testcontainers.Container)) TestOption {
	return func(c *testConfig) { c.container = fn }
}

// RunTestRedis starts a Redis container and returns a client connected to it.
//
// A client rather than an address: it is the highest-level thing a caller
// wants. The address is still reachable through the client's options, for a
// test that needs a second client on the same broker.
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
			// port says the mapped port is reachable from here. Either check
			// alone has let a broker through that would not answer.
			wait.ForAll(
				wait.ForLog("Ready to accept connections").WithStartupTimeout(cfg.startup),
				wait.ForListeningPort("6379/tcp").WithStartupTimeout(cfg.startup),
			),
		),
	)
	if container != nil {
		t.Cleanup(func() {
			// Not t.Context(): it is already cancelled by the time cleanup runs,
			// and Terminate would quietly do nothing.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := container.Terminate(ctx); err != nil {
				t.Errorf("terminate the Redis test container: %s", err)
			}
		})
	}

	require.NoError(t, err, "start the Redis test container")

	if cfg.container != nil {
		cfg.container(container)
	}

	uri, err := container.ConnectionString(t.Context())
	require.NoError(t, err, "read the Redis connection string")

	options, err := goredis.ParseURL(uri)
	require.NoError(t, err, "parse the Redis connection string")

	client := goredis.NewClient(options)
	t.Cleanup(func() {
		// A test may close the client itself; closing twice is not a failure.
		_ = client.Close()
	})

	require.NoError(t, client.Ping(t.Context()).Err(), "ping the Redis test container")

	return client
}
