package nats

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	natsgo "github.com/nats-io/nats.go"

	"github.com/kartaladev/hmntsk/notify"
)

// DefaultSubject is the subject signals travel on unless [WithSubject] replaces
// it. It shares nothing with the subjects delivery/nats publishes durable
// events under.
const DefaultSubject = "notify.signals"

// DefaultSubscribeTimeout bounds how long [Broadcaster.Listen] waits for the
// server to confirm its subscription unless [WithSubscribeTimeout] replaces it.
const DefaultSubscribeTimeout = 5 * time.Second

// MaxSignalsPerMessage is the most signals one message carries. A broadcast of
// more, such as escalating to a large group, is split into several messages, so
// that a message stays far below the server's default maximum payload whatever
// the fan-out.
const MaxSignalsPerMessage = 500

// Broadcaster carries notification change signals between instances over a
// NATS core subject. It is a [notify.Broadcaster]: pass it to
// [notify.WithBroadcaster] on every instance that shares a subject.
//
// A Broadcaster is safe for concurrent use.
type Broadcaster struct {
	conn             *natsgo.Conn
	subject          string
	subscribeTimeout time.Duration
	onError          func(ctx context.Context, err error)
}

var _ notify.Broadcaster = (*Broadcaster)(nil)

// Option configures a [Broadcaster].
type Option func(*config)

// config is what the options set. The subscribe timeout is a pointer because an
// explicit zero is a wiring mistake to report, while an absent value is the
// default.
type config struct {
	subject          string
	subscribeTimeout *time.Duration
	onError          func(ctx context.Context, err error)
}

// WithSubject replaces [DefaultSubject]. Instances see each other's signals
// only when they share a subject. A subject that is empty, contains whitespace,
// a wildcard ('*' or '>') or an empty token is a [ConfigurationError]: a message
// cannot be published to it.
func WithSubject(subject string) Option {
	return func(c *config) { c.subject = subject }
}

// WithSubscribeTimeout replaces [DefaultSubscribeTimeout], how long
// [Broadcaster.Listen] waits for the server to confirm its subscription. It must
// be positive.
func WithSubscribeTimeout(timeout time.Duration) Option {
	return func(c *config) { c.subscribeTimeout = &timeout }
}

// WithDecodeErrorHandler receives messages on the subject that could not be
// read, such as one in a signal format version this library does not know
// (matching [notify.ErrUnknownSignalFormat]). No signal is delivered for such a
// message, and receiving continues. The default handler does nothing, which is
// safe but silent; a host should supply one that logs. A nil handler keeps the
// default.
func WithDecodeErrorHandler(handler func(ctx context.Context, err error)) Option {
	return func(c *config) { c.onError = handler }
}

// NewBroadcaster builds a broadcaster over a NATS connection, which the host
// owns, configures (reconnection, TLS, credentials, its async error handler for
// slow consumers) and closes.
//
// With no options it publishes on [DefaultSubject] and waits
// [DefaultSubscribeTimeout] for a subscription to be confirmed. A nil
// connection, an invalid subject or a subscribe timeout that is not positive is
// a [ConfigurationError].
func NewBroadcaster(conn *natsgo.Conn, opts ...Option) (*Broadcaster, error) {
	cfg := config{subject: DefaultSubject}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if conn == nil {
		return nil, &ConfigurationError{Detail: "a NATS connection is required"}
	}

	if err := validateSubject(cfg.subject); err != nil {
		return nil, err
	}

	if cfg.subscribeTimeout != nil && *cfg.subscribeTimeout <= 0 {
		return nil, &ConfigurationError{
			Detail: "the subscribe timeout must be positive; omit WithSubscribeTimeout to keep the default",
		}
	}

	b := &Broadcaster{
		conn:             conn,
		subject:          cfg.subject,
		subscribeTimeout: DefaultSubscribeTimeout,
		onError:          func(context.Context, error) {},
	}

	if cfg.subscribeTimeout != nil {
		b.subscribeTimeout = *cfg.subscribeTimeout
	}

	if cfg.onError != nil {
		b.onError = cfg.onError
	}

	return b, nil
}

// Subject is the subject the broadcaster publishes and listens on.
func (b *Broadcaster) Subject() string { return b.subject }

// SubscribeTimeout is how long Listen waits for the server to confirm its
// subscription.
func (b *Broadcaster) SubscribeTimeout() time.Duration { return b.subscribeTimeout }

// Broadcast implements [notify.Broadcaster]. It publishes the signals in the
// notify signal format, one message per [MaxSignalsPerMessage] signals.
//
// It does not flush: while the server is away the connection buffers messages
// up to its reconnect buffer, and a flush per broadcast would buy a round trip
// without a guarantee. A message the connection does not take, because it is
// closed or its buffer is full, is a [*PublishError] matching [ErrPublish]; the
// messages before it were published.
func (b *Broadcaster) Broadcast(_ context.Context, signals []notify.Signal) error {
	for chunk := range slices.Chunk(signals, MaxSignalsPerMessage) {
		payload, err := notify.EncodeSignals(chunk)
		if err != nil {
			return err
		}

		if err := b.conn.Publish(b.subject, payload); err != nil {
			return &PublishError{Subject: b.subject, Signals: len(chunk), Cause: err}
		}
	}

	return nil
}

// listenBuffer is how many messages a listener holds before the connection
// counts it as a slow consumer and drops messages, which it reports to the
// connection's async error handler. deliver is the hub's non-blocking send, so
// it drains quickly.
const listenBuffer = 1000

// Listen implements [notify.Broadcaster]. It subscribes to the subject, with no
// queue group so that every instance receives every signal, waits for the
// server to confirm the subscription, calls ready, and then calls deliver with
// every signal of every message until ctx is done, when it unsubscribes and
// returns ctx's error.
//
// A subscription registered on the connection is not yet known to the server,
// and a signal published before the server knows it is not delivered. Listen
// therefore confirms it with a round trip to the server, bounded by the
// subscribe timeout ([DefaultSubscribeTimeout] unless [WithSubscribeTimeout]
// replaces it). A subscription the server does not confirm in time, such as
// while the connection is away, is returned as an error without calling ready;
// the host decides whether to run Listen again. A ctx cancelled while waiting
// returns ctx's error.
//
// deliver is called only from the goroutine running Listen, so nothing is
// delivered after Listen returns. A message that cannot be read is reported to
// the decode error handler and delivers nothing; receiving continues. After a
// reconnect the connection resubscribes on its own, and signals published while
// it was away are not replayed. A nil deliver or ready is a
// [ConfigurationError].
func (b *Broadcaster) Listen(ctx context.Context, deliver func(notify.Signal), ready func()) error {
	switch {
	case deliver == nil:
		return &ConfigurationError{Detail: "Listen needs a deliver function"}
	case ready == nil:
		return &ConfigurationError{Detail: "Listen needs a ready function"}
	}

	messages := make(chan *natsgo.Msg, listenBuffer)

	sub, err := b.conn.ChanSubscribe(b.subject, messages)
	if err != nil {
		return fmt.Errorf("nats: subscribe to subject %q: %w", b.subject, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := b.confirm(ctx); err != nil {
		return err
	}

	ready()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-messages:
			signals, err := notify.DecodeSignals(msg.Data)
			if err != nil {
				b.onError(ctx, fmt.Errorf("nats: a message on subject %q: %w", b.subject, err))

				continue
			}

			for _, signal := range signals {
				deliver(signal)
			}
		}
	}
}

// confirm waits, within the subscribe timeout, for the server to process
// everything the connection has sent so far, the subscription included. A
// cancelled ctx wins over the timeout and is returned as itself.
func (b *Broadcaster) confirm(ctx context.Context) error {
	flushCtx, cancel := context.WithTimeout(ctx, b.subscribeTimeout)
	defer cancel()

	if err := b.conn.FlushWithContext(flushCtx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return fmt.Errorf("nats: confirm subscription to subject %q: %w", b.subject, err)
	}

	return nil
}

// validateSubject applies the rules NATS applies to a publish subject, so that
// a subject no signal could be published on fails at wiring rather than on every
// broadcast. It is the rule delivery/nats applies to its prefix, copied rather
// than imported.
func validateSubject(subject string) error {
	if subject == "" {
		return &ConfigurationError{
			Detail: fmt.Sprintf("the subject must not be empty; omit WithSubject to keep %q", DefaultSubject),
		}
	}

	if strings.IndexFunc(subject, unicode.IsSpace) >= 0 {
		return &ConfigurationError{
			Detail: fmt.Sprintf("the subject %q contains whitespace, which a NATS subject cannot", subject),
		}
	}

	for token := range strings.SplitSeq(subject, ".") {
		switch token {
		case "":
			return &ConfigurationError{
				Detail: fmt.Sprintf("the subject %q has an empty token; remove the leading, trailing or doubled dot", subject),
			}
		case "*", ">":
			return &ConfigurationError{
				Detail: fmt.Sprintf("the subject %q contains the wildcard %q, and a message cannot be published to a wildcard", subject, token),
			}
		}
	}

	return nil
}
