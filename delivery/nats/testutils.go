package nats

import (
	"context"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NATSImage is the NATS server image the sinks are tested against.
//
// It is pinned. A moving tag would let a remote image update change what these
// tests mean overnight, for no reason anybody could find.
const NATSImage = "nats:2.12.7-alpine"

// testConfig is the provisioning a caller can vary.
type testConfig struct {
	image        string
	startup      time.Duration
	serverConfig string
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

// WithTestServerConfig starts the server with a configuration file holding
// config, for a test whose meaning depends on a server setting —
// "max_payload: 1024", for instance, makes an ordinary event oversized.
//
// A file rather than a command-line argument: most server settings, max_payload
// among them, have no flag, and the server refuses to start when given one. The
// file is added to the command line after -js, so it cannot switch JetStream
// off. A second call replaces the first.
func WithTestServerConfig(config string) TestOption {
	return func(c *testConfig) { c.serverConfig = config }
}

// RunTestNATS starts a NATS server with JetStream enabled and returns a
// connection to it.
//
// A connection rather than an address: it is the highest-level thing a caller
// wants, and nobody testing a sink should have to know that a container was
// involved. The address is still reachable through the connection when a test
// needs to put something between itself and the server.
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

	customizers := []testcontainers.ContainerCustomizer{
		// The module starts the server with -DV -js. JetStream is what the tests
		// need; -DV is debug and trace logging, which buries a failing test's
		// output under a line for every protocol message.
		testcontainers.WithCmd("-js"),
		testcontainers.WithWaitStrategy(
			// The log line says the server has finished starting JetStream; the
			// listening port says the mapped port is reachable from here.
			wait.ForAll(
				wait.ForLog("Server is ready").WithStartupTimeout(cfg.startup),
				// The log line already proves the server listens inside the
				// container, so only the host side is checked, without an exec.
				wait.ForListeningPort("4222/tcp").SkipInternalCheck().WithStartupTimeout(cfg.startup),
			),
		),
	}

	if cfg.serverConfig != "" {
		customizers = append(customizers, tcnats.WithConfigFile(strings.NewReader(cfg.serverConfig)))
	}

	container, err := tcnats.Run(t.Context(), cfg.image, customizers...)
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

	uri, err := container.ConnectionString(t.Context())
	require.NoError(t, err, "read the NATS connection string")

	conn, err := natsgo.Connect(uri, natsgo.Name("hmntsk-test"))
	require.NoError(t, err, "connect to the NATS test container")

	t.Cleanup(conn.Close)

	return conn
}
