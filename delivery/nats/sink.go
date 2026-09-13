package nats

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	natsgo "github.com/nats-io/nats.go"

	"github.com/kartaladev/hmntsk/relay"
)

// Defaults a sink takes when the corresponding option is not given.
const (
	// DefaultName is the plain-subject [Sink]'s name, as recorded in the relay's
	// per-sink acceptance. It differs from [DefaultJetStreamName] on purpose;
	// see [WithName].
	DefaultName = "nats"

	// DefaultJetStreamName is the [JetStreamSink]'s name.
	DefaultJetStreamName = "jetstream"

	// DefaultSubjectPrefix is the prefix every subject starts with. An event is
	// published to the prefix, a dot, and its event type:
	// hmntsk.events.task.completed.
	DefaultSubjectPrefix = "hmntsk.events"

	// DefaultTimeout bounds one publish attempt. It is short because the relay
	// is drained in passes: an unresponsive server that held an attempt open
	// would hold the whole pass open behind it.
	DefaultTimeout = 5 * time.Second
)

// Schema is the value of the [HeaderSchema] header on every published message.
// It names this header-and-body contract, which is not the Redis sink's field
// contract, so a consumer can reject a shape it was not written for rather than
// silently mis-parsing one.
const Schema = "hmntsk.nats.event.v1"

// ContentType is the value of the Content-Type header on every published
// message: the body is the whole event as JSON.
const ContentType = "application/json"

// The headers of a published message. The names and values are the webhook
// sink's, so a receiver of either already knows them. They are routing hints:
// the body carries every value too, and is authoritative where the two differ.
const (
	// HeaderEventID carries the event identifier. It is stable across
	// redeliveries, and is what a consumer de-duplicates on.
	HeaderEventID = "Hmntsk-Event-Id"
	// HeaderDeliveryID carries this attempt's identifier, fresh for every
	// attempt, so a consumer can tell one event seen twice from two events.
	HeaderDeliveryID = "Hmntsk-Delivery-Id"
	// HeaderAttempt carries which attempt this is, counting from one.
	HeaderAttempt = "Hmntsk-Attempt"
	// HeaderEventType carries the event type, such as "task.completed".
	HeaderEventType = "Hmntsk-Event-Type"
	// HeaderTaskID carries the task the event is about.
	HeaderTaskID = "Hmntsk-Task-Id"
	// HeaderTaskType carries the task's registered type name. A consumer
	// filters task types on it, because the task type is not in the subject.
	HeaderTaskType = "Hmntsk-Task-Type"
	// HeaderOwnerType carries the correlation owner type, and is omitted when
	// none was supplied.
	HeaderOwnerType = "Hmntsk-Correlation-Owner-Type"
	// HeaderOwnerRef carries the correlation owner reference, and is omitted
	// when none was supplied.
	HeaderOwnerRef = "Hmntsk-Correlation-Owner-Ref"
	// HeaderActivityKey carries the correlation activity key, and is omitted
	// when none was supplied.
	HeaderActivityKey = "Hmntsk-Correlation-Activity-Key"
	// HeaderSchema carries [Schema].
	HeaderSchema = "Hmntsk-Schema"
)

// config is the settings both sinks share. Validated, it is frozen into the
// sink as it is.
type config struct {
	name          string
	subjectPrefix string
	timeout       time.Duration
}

// Option varies how either sink publishes. Every Option is also a
// [JetStreamOption].
type Option func(*config)

// applyJetStream makes every shared option a [JetStreamOption] too.
func (o Option) applyJetStream(c *jetStreamConfig) { o(&c.config) }

// WithName overrides the sink's name, which is [DefaultName] for a [Sink] and
// [DefaultJetStreamName] for a [JetStreamSink].
//
// The name is written to the outbox as part of the relay's per-sink acceptance,
// so it must be stable across restarts: renaming a sink redelivers every event
// it has already taken. Do not give the two modes the same name. The relay
// would count one mode's acceptance as the other's, and a host switching modes
// would skip the events already taken.
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// WithSubjectPrefix overrides the prefix every subject starts with, which is
// [DefaultSubjectPrefix].
//
// The prefix must be a subject a message can be published to: non-empty, with
// no empty token, no wildcard and no whitespace. The constructors refuse any
// other.
func WithSubjectPrefix(prefix string) Option {
	return func(c *config) { c.subjectPrefix = prefix }
}

// WithTimeout overrides the per-attempt timeout, which is [DefaultTimeout].
//
// It bounds one publication and its confirmation, not the relay pass. A
// deadline already on the context passed to Deliver still applies: whichever
// expires first ends the attempt.
func WithTimeout(timeout time.Duration) Option {
	return func(c *config) { c.timeout = timeout }
}

// Sink publishes hmntsk events to plain NATS subjects.
//
// It is safe for concurrent use: it holds no mutable state, and the underlying
// connection is itself concurrency-safe.
type Sink struct {
	conn *natsgo.Conn
	config
}

// Sink is a relay sink. Asserted here so that a change to the interface is a
// compile error in this module rather than a runtime surprise in a host's.
var _ relay.Sink = (*Sink)(nil)

// NewSink returns a sink publishing to plain subjects on conn, named
// [DefaultName].
//
// Delivered means the NATS server has received the message, and nothing more.
// Plain NATS neither acknowledges receipt by a subscriber nor keeps history, so
// an event published while no subscriber matches its subject is delivered and
// lost: it is not offered again, and no subscriber ever sees it. Use
// [NewJetStreamSink] when that is not acceptable. See docs/delivery.md, under
// "Publishing to NATS".
//
// The connection is the host's: its servers, credentials, TLS and reconnect
// policy are configured where it is built, and this package neither dials nor
// closes it.
//
// A wiring mistake is reported as an error matching [ErrConfiguration], not a
// panic: it is found at construction, where a host can still refuse to start.
func NewSink(conn *natsgo.Conn, opts ...Option) (*Sink, error) {
	cfg := defaultConfig(DefaultName)
	for _, opt := range opts {
		opt(&cfg)
	}

	if conn == nil {
		return nil, &ConfigurationError{Detail: "a NATS connection is required; pass the *nats.Conn the host dialled"}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &Sink{conn: conn, config: cfg}, nil
}

// defaultConfig is the shared settings a sink named name starts from.
func defaultConfig(name string) config {
	return config{name: name, subjectPrefix: DefaultSubjectPrefix, timeout: DefaultTimeout}
}

// validate reports the first shared setting a sink could not publish with.
func (c config) validate() error {
	if c.name == "" {
		return &ConfigurationError{Detail: "the sink name must not be empty; omit WithName to keep the default"}
	}

	if c.timeout <= 0 {
		return &ConfigurationError{Detail: "the publish timeout must be positive; omit WithTimeout to keep the default"}
	}

	return validateSubjectPrefix(c.subjectPrefix)
}

// validateSubjectPrefix applies the rules NATS applies to a publish subject, so
// that a prefix no event could be published under fails at wiring rather than
// on every event.
func validateSubjectPrefix(prefix string) error {
	if prefix == "" {
		return &ConfigurationError{
			Detail: fmt.Sprintf("the subject prefix must not be empty; omit WithSubjectPrefix to keep %q", DefaultSubjectPrefix),
		}
	}

	if strings.IndexFunc(prefix, unicode.IsSpace) >= 0 {
		return &ConfigurationError{
			Detail: fmt.Sprintf("the subject prefix %q contains whitespace, which a NATS subject cannot", prefix),
		}
	}

	for token := range strings.SplitSeq(prefix, ".") {
		switch token {
		case "":
			return &ConfigurationError{
				Detail: fmt.Sprintf("the subject prefix %q has an empty token; remove the leading, trailing or doubled dot", prefix),
			}
		case "*", ">":
			return &ConfigurationError{
				Detail: fmt.Sprintf("the subject prefix %q contains the wildcard %q, and a message cannot be published to a wildcard", prefix, token),
			}
		}
	}

	return nil
}

// deliver is the attempt both sinks make: render the message, publish it
// within the sink's timeout, and classify the result. Only publish differs
// between the modes.
func deliver(
	ctx context.Context,
	cfg config,
	attempt relay.Attempt,
	publish func(context.Context, *natsgo.Msg) error,
) relay.Outcome {
	msg, err := message(cfg.subjectPrefix, attempt)
	if err != nil {
		return relay.Permanent(err)
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	if err := publish(ctx, msg); err != nil {
		return publishFailed(msg, err)
	}

	return relay.Delivered()
}

// Name implements [relay.Sink].
func (s *Sink) Name() string { return s.name }

// SubjectPrefix returns the prefix events are published under.
func (s *Sink) SubjectPrefix() string { return s.subjectPrefix }

// Deliver publishes one event, waits for the server to confirm receiving it,
// and classifies the result, all within the sink's timeout.
//
// The failures no further attempt can change are permanent: an event with no
// identifier or one that will not marshal ([ErrInvalidEvent]), a message larger
// than the server's maximum payload, and a subject the client refuses.
// Everything else — a lost or reconnecting connection, a confirmation that did
// not arrive in time, a cancelled pass — is retryable. Every publication
// failure is a [PublishError].
func (s *Sink) Deliver(ctx context.Context, attempt relay.Attempt) relay.Outcome {
	return deliver(ctx, s.config, attempt, s.publish)
}

// publish hands msg to the client and waits for the server to confirm it has
// received everything published so far.
//
// PublishMsg alone proves nothing: while the client is reconnecting it appends
// to a buffer and returns nil. The flush is a PING answered by a PONG, which
// cannot arrive until the server has read what came before it, so a nil flush
// is the server's receipt. FlushWithContext refuses a context with no deadline,
// and ctx always has one.
func (s *Sink) publish(ctx context.Context, msg *natsgo.Msg) error {
	// PublishMsg takes no context. Without this, a pass already cancelled would
	// still put the message on the wire and then report the cancellation:
	// a guaranteed duplicate on the next pass.
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := s.conn.PublishMsg(msg); err != nil {
		return err
	}

	return s.conn.FlushWithContext(ctx)
}
