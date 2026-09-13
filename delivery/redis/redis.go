// Package redis publishes hmntsk events to a Redis Stream.
//
// It is a [github.com/kartaladev/hmntsk/relay.Sink] and nothing else: it
// appends to a stream with XADD and stops there. It creates no consumer group,
// tracks no consumer position and offers no consumer-side helper — what a host
// does with the stream is the host's business, and the relay's own durability
// comes from the outbox rather than from the broker.
//
// Every relayed event is published, whether or not the task carries a callback
// address. The bus serves internal consumers and is independent of the per-task
// callback mechanism.
//
// # The message contract
//
// One stream carries every event type, with routing data in the message fields
// so a consumer can filter without a stream per task type and without reading
// the engine's database. A message has these fields:
//
//	schema       the message contract version; always [Schema]
//	eventId      the event's identifier, stable across redeliveries
//	eventType    the event type, such as "task.completed"
//	taskId       the task the event is about
//	taskType     the task's registered type name
//	status       the task's status after the transition
//	version      the task's version after the transition, in decimal
//	occurredAt   when the transition happened, RFC 3339 with nanoseconds, UTC
//	actor        who performed the operation; omitted when the system acted
//	assignee     the assignee after the transition; omitted when none
//	ownerType    the correlation owner type; omitted when not supplied
//	ownerRef     the correlation owner reference; omitted when not supplied
//	activityKey  the correlation activity key; omitted when not supplied
//	event        the whole [hmntsk.Event] as JSON
//
// The first eight fields are always present. The rest are omitted when empty,
// because a Redis client reading a message into a map cannot tell an absent
// field from an empty one anyway, and omitting keeps the common message small.
//
// A consumer that only routes reads the flat fields; one that needs the reason,
// the output, the callback target or the transition record parses the event
// field. Neither needs a database.
//
// Redelivery carries the same eventId, so a consumer de-duplicates on it. The
// relay delivers at least once: a crash between a successful XADD and the
// record of that success republishes the event.
//
// # Retention
//
// By default the stream is never trimmed and grows without limit. [WithMaxLen]
// or [WithMaxAge] bounds it as part of each publish, and [WithTrimMode] decides
// what trimming does about consumer groups. Every choice here either loses
// messages or stops publishing when made wrongly: read docs/delivery.md, under
// "Bounding the Redis stream", before setting one.
package redis

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// Defaults a [Sink] takes when the corresponding option is not given.
const (
	// DefaultName is the sink's name, as recorded in the relay's per-sink
	// acceptance. Renaming a sink re-delivers everything it has already taken,
	// so override it only deliberately.
	DefaultName = "redis"

	// DefaultStream is the stream events are appended to.
	DefaultStream = "hmntsk.events"

	// DefaultTimeout bounds one publish attempt. It is short because the relay
	// is drained in passes: an unresponsive broker that held an attempt open
	// would hold the whole pass open behind it.
	DefaultTimeout = 5 * time.Second
)

// Schema is the value of the schema field on every published message. It names
// the message contract, so a consumer can reject a shape it was not written
// for rather than silently mis-parsing one.
const Schema = "hmntsk.event.v1"

// The field names of a published message. They are part of the consumer
// contract documented on the package, and are exported so that a consumer in Go
// need not spell them out again.
const (
	// FieldSchema carries [Schema].
	FieldSchema = "schema"
	// FieldEventID carries the event identifier, on which a consumer
	// de-duplicates.
	FieldEventID = "eventId"
	// FieldDeliveryID carries this publish attempt's identifier. It is fresh
	// for every attempt, including a republish of an event the stream already
	// carries, so a consumer can tell one event seen twice from two events.
	FieldDeliveryID = "deliveryId"
	// FieldAttempt carries which attempt this is, counting from one.
	FieldAttempt = "attempt"
	// FieldEventType carries the event type.
	FieldEventType = "eventType"
	// FieldTaskID carries the task identifier.
	FieldTaskID = "taskId"
	// FieldTaskType carries the task's registered type name.
	FieldTaskType = "taskType"
	// FieldStatus carries the task's status after the transition.
	FieldStatus = "status"
	// FieldVersion carries the task's version after the transition, in decimal.
	FieldVersion = "version"
	// FieldOccurredAt carries the transition time, RFC 3339 with nanoseconds,
	// in UTC.
	FieldOccurredAt = "occurredAt"
	// FieldActor carries who performed the operation, and is omitted when the
	// system acted.
	FieldActor = "actor"
	// FieldAssignee carries the assignee after the transition, and is omitted
	// when the task sits in its pool.
	FieldAssignee = "assignee"
	// FieldOwnerType carries the correlation owner type, and is omitted when
	// none was supplied.
	FieldOwnerType = "ownerType"
	// FieldOwnerRef carries the correlation owner reference, and is omitted
	// when none was supplied.
	FieldOwnerRef = "ownerRef"
	// FieldActivityKey carries the correlation activity key, and is omitted
	// when none was supplied.
	FieldActivityKey = "activityKey"
	// FieldEvent carries the whole [hmntsk.Event] as JSON.
	FieldEvent = "event"
)

// config is the sink's settings before they are frozen into a [Sink].
type config struct {
	name      string
	stream    string
	timeout   time.Duration
	retention retention
	clock     hmntsk.Clock
}

// Option varies how a [Sink] publishes.
type Option func(*config)

// WithName overrides the sink's name, which is [DefaultName].
//
// The name is written to the outbox as part of the relay's per-sink acceptance,
// so it must be stable across restarts. Use it to tell two bus sinks apart when
// a host runs more than one.
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// WithStream overrides the stream events are appended to, which is
// [DefaultStream].
func WithStream(stream string) Option {
	return func(c *config) { c.stream = stream }
}

// WithTimeout overrides the per-attempt publish timeout, which is
// [DefaultTimeout].
//
// It bounds one XADD, not the relay pass. A deadline already on the context
// passed to [Sink.Deliver] still applies: whichever expires first ends the
// attempt.
func WithTimeout(timeout time.Duration) Option {
	return func(c *config) { c.timeout = timeout }
}

// Sink publishes hmntsk events to a Redis Stream.
//
// It is safe for concurrent use: it holds no mutable state, and the underlying
// client is itself concurrency-safe.
type Sink struct {
	client    goredis.UniversalClient
	name      string
	stream    string
	timeout   time.Duration
	retention retention
	clock     hmntsk.Clock
}

// Sink is a relay sink. Asserted here so that a change to the interface is a
// compile error in this module rather than a runtime surprise in a host's.
var _ relay.Sink = (*Sink)(nil)

// New returns a sink publishing to client.
//
// The client is the host's: its address, credentials, TLS, pool sizing and
// retry policy are configured where it is built, and this module neither
// dials nor closes it. A [goredis.UniversalClient] is taken rather than a
// concrete client so that a single node, a failover client, a ring and a
// cluster are all equally acceptable.
//
// A wiring mistake is reported as an error matching [ErrConfiguration], not a
// panic: it is found at construction, where a host can still refuse to start.
func New(client goredis.UniversalClient, opts ...Option) (*Sink, error) {
	cfg := &config{
		name:    DefaultName,
		stream:  DefaultStream,
		timeout: DefaultTimeout,
		clock:   hmntsk.SystemClock{},
	}

	for _, opt := range opts {
		opt(cfg)
	}

	if client == nil {
		return nil, &ConfigurationError{Detail: "a Redis client is required"}
	}

	if cfg.name == "" {
		return nil, &ConfigurationError{Detail: "the sink name must not be empty"}
	}

	if cfg.stream == "" {
		return nil, &ConfigurationError{Detail: "the stream name must not be empty"}
	}

	if cfg.timeout <= 0 {
		return nil, &ConfigurationError{Detail: "the publish timeout must be positive"}
	}

	if err := cfg.retention.validate(); err != nil {
		return nil, err
	}

	return &Sink{
		client:    client,
		name:      cfg.name,
		stream:    cfg.stream,
		timeout:   cfg.timeout,
		retention: cfg.retention,
		clock:     cfg.clock,
	}, nil
}

// Name implements [relay.Sink].
func (s *Sink) Name() string { return s.name }

// Stream returns the stream events are appended to.
func (s *Sink) Stream() string { return s.stream }

// Deliver publishes one event with XADD and classifies the result.
//
// Every failure the broker or the network produces is retryable: unreachable,
// timed out, and a rejected write alike. That is deliberate. A rejection is an
// operational condition — the key holds the wrong type, the server is out of
// memory, a replica is read-only — and it is a property of the broker at that
// moment, not of the event. Classifying it permanently would dead-letter a
// whole backlog over an outage that ends in a minute, which is exactly what the
// outbox exists to survive.
//
// The one permanent failure is an event that cannot be rendered as a message at
// all: it will be byte-identical on the next pass and fail in the same place.
//
// Each attempt is bounded by the sink's timeout, so an unresponsive broker
// cannot hold the relay pass open behind it. See [Sink.publish] for why that
// bound cannot be left to the client.
func (s *Sink) Deliver(ctx context.Context, attempt relay.Attempt) relay.Outcome {
	event := attempt.Event

	values, err := message(attempt)
	if err != nil {
		return relay.Permanent(err)
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	if err := s.publish(ctx, values); err != nil {
		return relay.Retryable(&PublishError{
			Stream:  s.stream,
			EventID: event.ID,
			Cause:   err,
		})
	}

	return relay.Delivered()
}

// publish runs one XADD and returns when ctx expires whether or not the client
// has.
//
// The indirection is not defensive programming, it is a fact about the client:
// go-redis derives its socket deadlines from its own DialTimeout, ReadTimeout
// and WriteTimeout, and ignores the context's deadline entirely unless the host
// built the client with ContextTimeoutEnabled. A sink that simply passed a
// deadline down would therefore promise a bound it did not have, and an
// unresponsive broker would hold the attempt for the client's ReadTimeout —
// which can be configured to never expire at all.
//
// The in-flight command is abandoned rather than cancelled: the goroutine ends
// when the client's own timeout fires, or at once when the host did enable
// context timeouts. Two consequences are worth stating, because both are
// visible to a consumer:
//
//   - An abandoned write may still reach the broker. The attempt is reported
//     retryable and the event is published again on the next pass, so the
//     stream can carry it twice. That is the at-least-once contract the whole
//     relay is built on, and is why every message carries an event identifier.
//   - A host whose client has no read timeout leaves one goroutine per
//     abandoned attempt parked until the connection breaks. Configure the
//     client's ReadTimeout; it is the client's own backstop, not this sink's.
func (s *Sink) publish(ctx context.Context, values []any) error {
	// Buffered, so the goroutine can always deliver its result and exit even
	// when nobody is waiting for it any more.
	done := make(chan error, 1)

	args := &goredis.XAddArgs{Stream: s.stream, Values: values}
	s.retention.trim(args, s.clock)

	go func() {
		done <- s.client.XAdd(ctx, args).Err()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// message renders an event as the field-value pairs of one stream entry.
//
// The pairs are a slice rather than a map so that the order is the documented
// one: a message read in redis-cli is then a readable record rather than a
// shuffle, and every entry on the stream looks the same.
func message(attempt relay.Attempt) ([]any, error) {
	event := attempt.Event

	if event.ID == "" {
		return nil, &InvalidEventError{
			TaskID: event.TaskID,
			Detail: "the event has no identifier, so a consumer could not de-duplicate it",
		}
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, &InvalidEventError{
			TaskID: event.TaskID,
			Detail: "the event will not marshal to JSON",
			Cause:  err,
		}
	}

	values := []any{
		FieldSchema, Schema,
		FieldEventID, event.ID,
		FieldDeliveryID, attempt.DeliveryID,
		FieldAttempt, strconv.Itoa(attempt.Number),
		FieldEventType, string(event.Type),
		FieldTaskID, string(event.TaskID),
		FieldTaskType, event.TaskType,
		FieldStatus, string(event.Status),
		FieldVersion, strconv.FormatInt(event.Version, 10),
		FieldOccurredAt, event.OccurredAt.UTC().Format(time.RFC3339Nano),
	}

	optional := [...]struct {
		field string
		value string
	}{
		{FieldActor, event.Actor},
		{FieldAssignee, event.Assignee},
		{FieldOwnerType, event.Correlation.OwnerType},
		{FieldOwnerRef, event.Correlation.OwnerRef},
		{FieldActivityKey, event.Correlation.ActivityKey},
	}

	for _, pair := range optional {
		if pair.value != "" {
			values = append(values, pair.field, pair.value)
		}
	}

	return append(values, FieldEvent, string(encoded)), nil
}
