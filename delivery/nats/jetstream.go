package nats

import (
	"context"
	"slices"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kartaladev/hmntsk/relay"
)

// jetStreamConfig is the JetStream sink's settings: the shared ones, and those
// that mean something only to JetStream.
type jetStreamConfig struct {
	config

	expectStream string
	// expectStreamSet tells WithExpectStream("") apart from no option at all.
	expectStreamSet bool
}

// validate reports the first setting a JetStream sink could not publish with.
func (c jetStreamConfig) validate() error {
	if err := c.config.validate(); err != nil {
		return err
	}

	if c.expectStreamSet && c.expectStream == "" {
		return &ConfigurationError{Detail: "the expected stream must not be empty; omit WithExpectStream to publish without the check"}
	}

	return nil
}

// JetStreamOption varies how a [JetStreamSink] publishes. Every [Option] is one;
// a JetStream-only option is not an [Option], so passing it to [NewSink] does
// not compile.
type JetStreamOption interface {
	applyJetStream(c *jetStreamConfig)
}

// jetStreamOptionFunc is an option only a [JetStreamSink] accepts.
type jetStreamOptionFunc func(*jetStreamConfig)

func (f jetStreamOptionFunc) applyJetStream(c *jetStreamConfig) { f(c) }

// WithExpectStream names the stream every publication must land in. By
// default no stream is expected, and a publication is stored by whichever
// stream captures its subject.
//
// It is for hosts whose streams have overlapping subjects. The server refuses a
// publication that would be stored in any other stream, before storing it, and
// the attempt is retryable: reconfiguring the stream fixes it without
// redeploying. An empty name is a configuration error; omit the option instead.
func WithExpectStream(stream string) JetStreamOption {
	return jetStreamOptionFunc(func(c *jetStreamConfig) {
		c.expectStream = stream
		c.expectStreamSet = true
	})
}

// JetStreamSink publishes hmntsk events to JetStream.
//
// It is safe for concurrent use: it holds no mutable state, and the underlying
// JetStream context is itself concurrency-safe.
type JetStreamSink struct {
	js jetstream.JetStream
	config

	// publishOpts are the options every publication carries. The message ID,
	// the one that varies, is added per event.
	publishOpts []jetstream.PublishOpt
}

// JetStreamSink is a relay sink. Asserted here so that a change to the interface
// is a compile error in this module rather than a runtime surprise in a host's.
var _ relay.Sink = (*JetStreamSink)(nil)

// NewJetStreamSink returns a sink publishing through js, named
// [DefaultJetStreamName].
//
// Delivered means a stream has acknowledged storing the message. Every
// publication carries the event identifier as its message ID, so the stream
// discards a redelivery inside its duplicate window, and the acknowledgement of
// a discarded duplicate is reported delivered.
//
// The sink never creates, updates or deletes a stream: subjects, retention,
// replicas, storage and the duplicate window are the host's. Create a stream
// capturing the subject prefix before starting the relay. With none, every
// attempt fails with [jetstream.ErrNoStreamResponse] and is retryable, and the
// relay dead-letters the event once its attempts run out. See docs/delivery.md,
// under "Publishing to NATS".
//
// js carries the host's connection, domain and API prefix, and this package
// neither dials nor closes the connection under it.
//
// A wiring mistake is reported as an error matching [ErrConfiguration], not a
// panic: it is found at construction, where a host can still refuse to start.
func NewJetStreamSink(js jetstream.JetStream, opts ...JetStreamOption) (*JetStreamSink, error) {
	cfg := jetStreamConfig{config: defaultConfig(DefaultJetStreamName)}
	for _, opt := range opts {
		opt.applyJetStream(&cfg)
	}

	if js == nil {
		return nil, &ConfigurationError{Detail: "a JetStream context is required; pass jetstream.New on the host's connection"}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	publishOpts := []jetstream.PublishOpt{
		// Not tuning: without it the client answers "no responders" with two
		// more publications 250ms apart, a retry inside the attempt that the
		// relay's attempt budget cannot see.
		jetstream.WithRetryAttempts(0),
	}

	if cfg.expectStreamSet {
		publishOpts = append(publishOpts, jetstream.WithExpectStream(cfg.expectStream))
	}

	return &JetStreamSink{js: js, config: cfg.config, publishOpts: publishOpts}, nil
}

// Name implements [relay.Sink].
func (s *JetStreamSink) Name() string { return s.name }

// SubjectPrefix returns the prefix events are published under.
func (s *JetStreamSink) SubjectPrefix() string { return s.subjectPrefix }

// Deliver makes one publication, waits for a stream to acknowledge storing it,
// and classifies the result, all within the sink's timeout.
//
// It never retries within the attempt; retrying is the relay's decision. The
// permanent failures are those of [Sink.Deliver]: an event with no identifier or
// one that will not marshal, a message larger than the server's maximum
// payload, and a subject the client refuses. Everything else is retryable,
// including no stream capturing the subject ([jetstream.ErrNoStreamResponse]),
// which a stream electing a new leader answers briefly too, and a publication
// bound for a stream other than the expected one. Every publication failure is
// a [PublishError].
func (s *JetStreamSink) Deliver(ctx context.Context, attempt relay.Attempt) relay.Outcome {
	return deliver(ctx, s.config, attempt, s.publish)
}

// publish makes exactly one publication and returns once a stream has
// acknowledged storing it.
//
// An acknowledgement marked as a duplicate returns nil like any other: the
// stream already holds the event, which is what delivered means here.
func (s *JetStreamSink) publish(ctx context.Context, msg *natsgo.Msg) error {
	// The event, not the attempt: a redelivery is the same event, and the
	// stream should discard it. Clipped, so appending never writes into the
	// shared slice from concurrent deliveries.
	opts := append(slices.Clip(s.publishOpts), jetstream.WithMsgID(msg.Header.Get(HeaderEventID)))

	_, err := s.js.PublishMsg(ctx, msg, opts...)

	return err
}
