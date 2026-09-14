package redis

import (
	"context"
	"fmt"
	"slices"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kartaladev/hmntsk/notify"
)

// DefaultChannel is the pub/sub channel signals travel on unless [WithChannel]
// replaces it. It shares nothing with the stream delivery/redis appends durable
// events to.
const DefaultChannel = "notify.signals"

// DefaultPublishTimeout bounds one publish unless [WithPublishTimeout] replaces
// it. A broker that has not answered by then is reported as unreachable; the
// notification that produced the signal is already stored either way.
const DefaultPublishTimeout = 5 * time.Second

// MaxSignalsPerMessage is the most signals one message carries. A broadcast of
// more, such as escalating to a large group, is split into several messages, so
// that a message stays small whatever the fan-out.
const MaxSignalsPerMessage = 500

// Broadcaster carries notification change signals between instances over Redis
// publish/subscribe. It is a [notify.Broadcaster]: pass it to
// [notify.WithBroadcaster] on every instance that shares a channel.
//
// A Broadcaster is safe for concurrent use.
type Broadcaster struct {
	client  goredis.UniversalClient
	channel string
	timeout time.Duration
	onError func(ctx context.Context, err error)
}

var _ notify.Broadcaster = (*Broadcaster)(nil)

// Option configures a [Broadcaster].
type Option func(*config)

// config is what the options set. The timeout is a pointer because an explicit
// zero is a wiring mistake to report, while an absent value is the default.
type config struct {
	channel string
	timeout *time.Duration
	onError func(ctx context.Context, err error)
}

// WithChannel replaces [DefaultChannel]. Instances see each other's signals
// only when they share a channel. An empty channel is a [ConfigurationError].
func WithChannel(channel string) Option {
	return func(c *config) { c.channel = channel }
}

// WithPublishTimeout replaces [DefaultPublishTimeout]. It must be positive.
func WithPublishTimeout(timeout time.Duration) Option {
	return func(c *config) { c.timeout = &timeout }
}

// WithDecodeErrorHandler receives messages on the channel that could not be
// read, such as one in a signal format version this library does not know
// (matching [notify.ErrUnknownSignalFormat]). No signal is delivered for such a
// message, and receiving continues. The default handler does nothing, which is
// safe but silent; a host should supply one that logs. A nil handler keeps the
// default.
func WithDecodeErrorHandler(handler func(ctx context.Context, err error)) Option {
	return func(c *config) { c.onError = handler }
}

// NewBroadcaster builds a broadcaster over a Redis client, which the host owns
// and closes. It does not dial.
//
// With no options it publishes on [DefaultChannel] within
// [DefaultPublishTimeout]. A nil client, an empty channel or a timeout that is
// not positive is a [ConfigurationError].
func NewBroadcaster(client goredis.UniversalClient, opts ...Option) (*Broadcaster, error) {
	cfg := config{channel: DefaultChannel}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	switch {
	case client == nil:
		return nil, &ConfigurationError{Detail: "a Redis client is required"}
	case cfg.channel == "":
		return nil, &ConfigurationError{
			Detail: "the channel must not be empty; omit WithChannel to keep " + DefaultChannel,
		}
	case cfg.timeout != nil && *cfg.timeout <= 0:
		return nil, &ConfigurationError{
			Detail: "the publish timeout must be positive; omit WithPublishTimeout to keep the default",
		}
	}

	b := &Broadcaster{
		client:  client,
		channel: cfg.channel,
		timeout: DefaultPublishTimeout,
		onError: func(context.Context, error) {},
	}

	if cfg.timeout != nil {
		b.timeout = *cfg.timeout
	}

	if cfg.onError != nil {
		b.onError = cfg.onError
	}

	return b, nil
}

// Channel is the pub/sub channel the broadcaster publishes and listens on.
func (b *Broadcaster) Channel() string { return b.channel }

// PublishTimeout is how long one publish may take.
func (b *Broadcaster) PublishTimeout() time.Duration { return b.timeout }

// Broadcast implements [notify.Broadcaster]. It publishes the signals in the
// notify signal format, one message per [MaxSignalsPerMessage] signals, each
// within the publish timeout. A message the broker does not take is a
// [*PublishError] matching [ErrPublish]; the messages before it were published.
func (b *Broadcaster) Broadcast(ctx context.Context, signals []notify.Signal) error {
	for chunk := range slices.Chunk(signals, MaxSignalsPerMessage) {
		payload, err := notify.EncodeSignals(chunk)
		if err != nil {
			return err
		}

		if err := b.publish(ctx, payload); err != nil {
			return &PublishError{Channel: b.channel, Signals: len(chunk), Cause: err}
		}
	}

	return nil
}

// publish runs one PUBLISH within the publish timeout.
func (b *Broadcaster) publish(ctx context.Context, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	return b.client.Publish(ctx, b.channel, payload).Err()
}

// listenBuffer is how many messages a listener holds before the client's reader
// waits. deliver is the hub's non-blocking send, so it drains quickly.
const listenBuffer = 1000

// Listen implements [notify.Broadcaster]. It subscribes to the channel, waits
// for the broker to confirm the subscription, calls ready once, and then calls
// deliver with every signal of every message, until ctx is done, when it
// unsubscribes and returns ctx's error.
//
// ready is called only after the broker has confirmed the subscription, so a
// signal broadcast from then on, by this instance or another, is delivered. A
// nil deliver or ready is a [ConfigurationError], returned before
// subscribing: it breaks the notify contract rather than this broadcaster's
// configuration.
//
// A message that cannot be read is reported to the decode error handler and
// delivers nothing; receiving continues. After a dropped connection the client
// resubscribes on its own, and signals published while it was away are not
// replayed; ready is not called again. A subscription the client could not make,
// or one that ends while ctx is still live, is returned as an error, and ready is
// not called for a subscription that was never confirmed.
func (b *Broadcaster) Listen(ctx context.Context, deliver func(notify.Signal), ready func()) error {
	switch {
	case deliver == nil:
		return &ConfigurationError{Detail: "Listen needs a deliver function"}
	case ready == nil:
		return &ConfigurationError{Detail: "Listen needs a ready function"}
	}

	pubsub := b.client.Subscribe(ctx, b.channel)
	defer func() { _ = pubsub.Close() }()

	reply, err := pubsub.Receive(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return fmt.Errorf("redis: subscribe to channel %q: %w", b.channel, err)
	}

	if _, confirmed := reply.(*goredis.Subscription); !confirmed {
		return fmt.Errorf("redis: subscribe to channel %q: the broker answered %T, not a subscription confirmation",
			b.channel, reply)
	}

	ready()

	messages := pubsub.Channel(goredis.WithChannelSize(listenBuffer))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-messages:
			if !ok {
				return fmt.Errorf("redis: the subscription to channel %q ended", b.channel)
			}

			signals, err := notify.DecodeSignals([]byte(msg.Payload))
			if err != nil {
				b.onError(ctx, fmt.Errorf("redis: a message on channel %q: %w", b.channel, err))

				continue
			}

			for _, signal := range signals {
				deliver(signal)
			}
		}
	}
}
