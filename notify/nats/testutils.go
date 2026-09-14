package nats

import (
	"context"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NATSImage is the NATS server image the broadcaster is tested against. It is
// the image delivery/nats pins, copied rather than imported: this module moves
// to its own repository with notify and may import nothing that stays behind.
//
// It is pinned. A moving tag would let a remote image update change what these
// tests mean overnight.
const NATSImage = "nats:2.12.7-alpine"

// testConfig is the provisioning a caller can vary.
type testConfig struct {
	image     string
	startup   time.Duration
	connect   []natsgo.Option
	container func(testcontainers.Container)
	url       *string
}

// TestOption varies how a test server is provisioned.
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

// WithTestConnectOptions adds options to the connection the helper returns,
// such as a reconnect policy for a test that takes the server away.
func WithTestConnectOptions(opts ...natsgo.Option) TestOption {
	return func(c *testConfig) { c.connect = append(c.connect, opts...) }
}

// WithTestContainer hands the started container to fn, for a test whose meaning
// depends on the server going away. The container is still terminated by the
// helper.
func WithTestContainer(fn func(testcontainers.Container)) TestOption {
	return func(c *testConfig) { c.container = fn }
}

// WithTestURL stores the server's URL in dst, for a test that opens a second
// connection to the same server.
func WithTestURL(dst *string) TestOption {
	return func(c *testConfig) { c.url = dst }
}

// RunTestNATS starts a NATS server and returns a connection to it.
//
// A connection rather than an address: it is the highest-level thing a caller
// wants. [WithTestURL] hands out the address for a test that needs a second
// connection.
//
// The container lives as long as the test. Termination is registered with
// t.Cleanup the instant the container exists, before anything that could fail,
// so a failed start or connection cannot leak it.
func RunTestNATS(t *testing.T, opts ...TestOption) *natsgo.Conn {
	t.Helper()

	cfg := &testConfig{
		image:   NATSImage,
		startup: 2 * time.Minute,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	container, err := tcnats.Run(t.Context(), cfg.image,
		// The module starts the server with -DV, debug and trace logging, which
		// buries a failing test's output under a line per protocol message. Core
		// subjects need nothing else.
		testcontainers.WithCmd(),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForLog("Server is ready").WithStartupTimeout(cfg.startup),
				// The log line already proves the server listens inside the
				// container, so only the host side is checked, without an exec.
				wait.ForListeningPort("4222/tcp").SkipInternalCheck().WithStartupTimeout(cfg.startup),
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
				t.Errorf("terminate the NATS test container: %s", err)
			}
		})
	}

	require.NoError(t, err, "start the NATS test container")

	if cfg.container != nil {
		cfg.container(container)
	}

	uri, err := container.ConnectionString(t.Context())
	require.NoError(t, err, "read the NATS connection string")

	if cfg.url != nil {
		*cfg.url = uri
	}

	conn, err := natsgo.Connect(uri, append([]natsgo.Option{natsgo.Name("notify-test")}, cfg.connect...)...)
	require.NoError(t, err, "connect to the NATS test container")

	t.Cleanup(conn.Close)

	return conn
}
