package redis_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/kartaladev/hmntsk"
	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
	"github.com/kartaladev/hmntsk/relay"
)

// TestPublish runs the sink against a real broker.
//
// A container rather than a fake: what is being asserted is that a message
// written with XADD comes back off the stream in a shape a consumer can route
// on, and an in-memory stand-in would be asserting its own idea of that shape
// rather than Redis's.
func TestPublish(t *testing.T) {
	suite.Run(t, new(publishSuite))
}

// publishSuite shares one broker across its cases. The container is the
// expensive part; a stream per case is free, and is what keeps the cases from
// seeing each other's messages.
type publishSuite struct {
	suite.Suite

	client *goredis.Client
}

// SetupSuite starts the one broker every case publishes to.
func (s *publishSuite) SetupSuite() {
	s.client = hmntskredis.RunTestRedis(s.T())
}

// streams hands out a distinct stream name per call.
var streams atomic.Uint64

// newSink returns a sink publishing to a stream no other case will touch, and
// the name of that stream.
func (s *publishSuite) newSink(opts ...hmntskredis.Option) (sink *hmntskredis.Sink, stream string) {
	s.T().Helper()

	return newStreamSink(s.T(), s.client, opts...)
}

// entries reads every message on a stream, oldest first.
func (s *publishSuite) entries(ctx context.Context, stream string) []goredis.XMessage {
	s.T().Helper()

	messages, err := s.client.XRange(ctx, stream, "-", "+").Result()
	s.Require().NoError(err, "read the stream back")

	return messages
}

// TestPublishesEveryEventWithItsRoutingData is the consumer contract: every
// relayed event reaches the bus, and what arrives is enough to route on.
//
// The cases vary what the task carried, because the interesting claim is that
// none of it changes whether the event is published. In particular a task with
// no callback address still reaches the bus: the bus serves internal consumers
// and owes nothing to the per-task callback mechanism.
func (s *publishSuite) TestPublishesEveryEventWithItsRoutingData() {
	type testCase struct {
		name   string
		event  hmntsk.Event
		ctx    func(ctx context.Context) context.Context
		assert func(t *testing.T, outcome relay.Outcome, messages []goredis.XMessage)
	}

	noCallback := testEvent()
	noCallback.ID = "evt-no-callback"
	noCallback.Callback = nil

	noCorrelation := testEvent()
	noCorrelation.ID = "evt-no-correlation"
	noCorrelation.Callback = nil
	noCorrelation.Correlation = hmntsk.CorrelationData{}
	noCorrelation.Actor = ""
	noCorrelation.Assignee = ""

	cases := []testCase{
		{
			name:  "a task with a callback address",
			event: testEvent(),
			assert: func(t *testing.T, outcome relay.Outcome, messages []goredis.XMessage) {
				requireDelivered(t, outcome)
				require.Len(t, messages, 1)

				fields := fieldsOf(t, messages[0])
				require.Equal(t, map[string]string{
					hmntskredis.FieldSchema:      hmntskredis.Schema,
					hmntskredis.FieldEventID:     "evt-0001",
					hmntskredis.FieldEventType:   "task.completed",
					hmntskredis.FieldTaskID:      "tsk-0001",
					hmntskredis.FieldTaskType:    "acme.approval",
					hmntskredis.FieldStatus:      "COMPLETED",
					hmntskredis.FieldVersion:     "7",
					hmntskredis.FieldOccurredAt:  "2026-09-13T10:30:00.123456Z",
					hmntskredis.FieldActor:       "alice",
					hmntskredis.FieldAssignee:    "alice",
					hmntskredis.FieldOwnerType:   "process",
					hmntskredis.FieldOwnerRef:    "ord-42",
					hmntskredis.FieldActivityKey: "approve-invoice",
					// The attempt's identity varies by construction; that it is
					// there, and distinct per attempt, is asserted in
					// TestRedeliveryCarriesTheSameEventIdentifier.
					hmntskredis.FieldDeliveryID: fields[hmntskredis.FieldDeliveryID],
					hmntskredis.FieldAttempt:    fields[hmntskredis.FieldAttempt],
					hmntskredis.FieldEvent:      fields[hmntskredis.FieldEvent],
				}, fields, "the published field set is the documented consumer contract")

				assert.NotEmpty(t, fields[hmntskredis.FieldDeliveryID],
					"every publish carries an identifier for the attempt")
				assert.NotEmpty(t, fields[hmntskredis.FieldAttempt],
					"every publish says which attempt it is")

				// The whole event travels with it, so a consumer that needs the
				// output or the callback target need not read the database
				// either.
				var event hmntsk.Event
				require.NoError(t, json.Unmarshal([]byte(fields[hmntskredis.FieldEvent]), &event))
				require.Equal(t, "evt-0001", event.ID)
				require.JSONEq(t, `{"decision":"approve"}`, string(event.Output))
				require.NotNil(t, event.Callback)
				require.Equal(t, "https://example.invalid/hooks/tasks", event.Callback.Address)
				require.Equal(t, map[string]string{"tenant": "acme"}, event.Correlation.Extra)
			},
		},
		{
			name:  "a task with no callback address still reaches the bus",
			event: noCallback,
			assert: func(t *testing.T, outcome relay.Outcome, messages []goredis.XMessage) {
				requireDelivered(t, outcome)
				require.Len(t, messages, 1)

				fields := fieldsOf(t, messages[0])
				require.Equal(t, "evt-no-callback", fields[hmntskredis.FieldEventID])
				require.Equal(t, "acme.approval", fields[hmntskredis.FieldTaskType])
				require.Equal(t, "process", fields[hmntskredis.FieldOwnerType])
			},
		},
		{
			name:  "an event with nothing optional set",
			event: noCorrelation,
			assert: func(t *testing.T, outcome relay.Outcome, messages []goredis.XMessage) {
				requireDelivered(t, outcome)
				require.Len(t, messages, 1)

				fields := fieldsOf(t, messages[0])
				require.Equal(t, "evt-no-correlation", fields[hmntskredis.FieldEventID])
				// The always-present set is still complete...
				require.Equal(t, hmntskredis.Schema, fields[hmntskredis.FieldSchema])
				require.Equal(t, "tsk-0001", fields[hmntskredis.FieldTaskID])
				require.Equal(t, "7", fields[hmntskredis.FieldVersion])
				// ...and the optional fields are absent rather than empty.
				for _, field := range []string{
					hmntskredis.FieldActor,
					hmntskredis.FieldAssignee,
					hmntskredis.FieldOwnerType,
					hmntskredis.FieldOwnerRef,
					hmntskredis.FieldActivityKey,
				} {
					require.NotContains(t, fields, field)
				}
			},
		},
		{
			name:  "a cancelled pass publishes nothing",
			event: testEvent(),
			ctx: func(ctx context.Context) context.Context {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				return cancelled
			},
			assert: func(t *testing.T, outcome relay.Outcome, messages []goredis.XMessage) {
				require.Equal(t, relay.OutcomeRetryable, outcome.Status)
				require.Empty(t, messages)
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()

			sink, stream := s.newSink()

			ctx := t.Context()
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}

			outcome := sink.Deliver(ctx, attempt(tc.event))
			tc.assert(t, outcome, s.entries(t.Context(), stream))
		})
	}
}

// TestAConsumerRoutesWithoutADatabaseLookup reads the stream the way a consumer
// would and dispatches on what it finds, with no access to the engine's store.
func (s *publishSuite) TestAConsumerRoutesWithoutADatabaseLookup() {
	t := s.T()
	ctx := t.Context()

	sink, stream := s.newSink()

	invoice := testEvent()
	invoice.ID = "evt-invoice"

	shipment := testEvent()
	shipment.ID = "evt-shipment"
	shipment.TaskType = "acme.shipping"
	shipment.Correlation = hmntsk.CorrelationData{
		OwnerType:   "order",
		OwnerRef:    "ord-99",
		ActivityKey: "confirm-shipment",
	}

	for _, event := range []hmntsk.Event{invoice, shipment} {
		requireDelivered(t, sink.Deliver(ctx, attempt(event)))
	}

	// A consumer subscribing to the one stream, routing on the message alone.
	routed := map[string]string{}

	for _, message := range s.entries(ctx, stream) {
		fields := fieldsOf(t, message)
		routed[fields[hmntskredis.FieldEventID]] = fmt.Sprintf(
			"%s/%s/%s/%s",
			fields[hmntskredis.FieldOwnerType],
			fields[hmntskredis.FieldOwnerRef],
			fields[hmntskredis.FieldActivityKey],
			fields[hmntskredis.FieldTaskType],
		)
	}

	require.Equal(t, map[string]string{
		"evt-invoice":  "process/ord-42/approve-invoice/acme.approval",
		"evt-shipment": "order/ord-99/confirm-shipment/acme.shipping",
	}, routed)
}

// TestRedeliveryCarriesTheSameEventIdentifier covers the de-duplication the
// at-least-once contract leaves to the consumer: a crash between a successful
// write and the record of it republishes the event, and the consumer has to be
// able to tell that it is the same one.
func (s *publishSuite) TestRedeliveryCarriesTheSameEventIdentifier() {
	t := s.T()
	ctx := t.Context()

	sink, stream := s.newSink()
	event := testEvent()

	requireDelivered(t, sink.Deliver(ctx, attempt(event)))
	requireDelivered(t, sink.Deliver(ctx, attempt(event)))

	messages := s.entries(ctx, stream)
	require.Len(t, messages, 2)
	require.NotEqual(t, messages[0].ID, messages[1].ID, "two entries, not one overwritten")

	first, second := fieldsOf(t, messages[0]), fieldsOf(t, messages[1])

	assert.Equal(t, event.ID, first[hmntskredis.FieldEventID])
	assert.Equal(t, first[hmntskredis.FieldEventID], second[hmntskredis.FieldEventID],
		"a consumer de-duplicates on the event identifier, so a republish must not mint a new one")

	assert.NotEqual(t, first[hmntskredis.FieldDeliveryID], second[hmntskredis.FieldDeliveryID],
		"the delivery identifier is what tells one event published twice from two events")
	assert.NotEmpty(t, first[hmntskredis.FieldDeliveryID],
		"every publish carries an attempt identifier")

	// Everything that describes the event, rather than the attempt, is
	// unchanged: a consumer routing on these fields must not see a redelivery
	// as different work.
	delete(first, hmntskredis.FieldDeliveryID)
	delete(first, hmntskredis.FieldAttempt)
	delete(second, hmntskredis.FieldDeliveryID)
	delete(second, hmntskredis.FieldAttempt)

	assert.Equal(t, first, second, "a redelivery describes the same event in the same way")
}

// TestCreatesNoConsumerMachinery holds the producer-only line. Consumer groups,
// offsets and acknowledgement are the host's side of it, and a sink that
// quietly created a group would be making a scaling decision on the host's
// behalf that the host could not undo without losing messages.
func (s *publishSuite) TestCreatesNoConsumerMachinery() {
	t := s.T()
	ctx := t.Context()

	sink, stream := s.newSink()

	for _, id := range []string{"evt-a", "evt-b", "evt-c"} {
		event := testEvent()
		event.ID = id
		requireDelivered(t, sink.Deliver(ctx, attempt(event)))
	}

	groups, err := s.client.XInfoGroups(ctx, stream).Result()
	require.NoError(t, err)
	require.Empty(t, groups, "the sink must create no consumer group")

	info, err := s.client.XInfoStream(ctx, stream).Result()
	require.NoError(t, err)
	require.Zero(t, info.Groups, "the sink must track no consumer position")
	require.EqualValues(t, 3, info.Length)

	// Nor anything else: no offset key, no checkpoint, no bookkeeping of its
	// own beside the stream.
	keys, err := s.client.Keys(ctx, stream+"*").Result()
	require.NoError(t, err)
	require.Equal(t, []string{stream}, keys)
}

// TestBacklogSurvivesAnOutage is the requirement that makes the retryable
// classification worth having: a broker that is gone for several passes and
// comes back must publish the backlog, not lose it.
func (s *publishSuite) TestBacklogSurvivesAnOutage() {
	t := s.T()
	ctx := t.Context()

	proxy := newBrokerProxy(t, s.client.Options().Addr)

	client := goredis.NewClient(&goredis.Options{
		Addr:        proxy.Addr(),
		DialTimeout: time.Second,
		ReadTimeout: time.Second,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { _ = client.Close() })

	sink, stream := newStreamSink(t, client, hmntskredis.WithTimeout(2*time.Second))

	backlog := make([]hmntsk.Event, 0, 3)
	for i := range 3 {
		event := testEvent()
		event.ID = fmt.Sprintf("evt-backlog-%d", i)
		backlog = append(backlog, event)
	}

	// Three passes with the broker down. Every one of them must be retryable:
	// a single permanent verdict here dead-letters the backlog.
	for pass := range 3 {
		for _, event := range backlog {
			outcome := sink.Deliver(ctx, attempt(event))
			require.Equal(t, relay.OutcomeRetryable, outcome.Status,
				"pass %d, event %s: an outage must never be permanent", pass, event.ID)
			require.ErrorIs(t, outcome.Err, hmntskredis.ErrPublish)
		}
	}

	require.Zero(t, s.client.Exists(ctx, stream).Val(), "nothing was published while the broker was down")

	proxy.Open()

	for _, event := range backlog {
		requireDelivered(t, sink.Deliver(ctx, attempt(event)))
	}

	require.Equal(t, []string{"evt-backlog-0", "evt-backlog-1", "evt-backlog-2"},
		eventIDs(t, ctx, s.client, stream))
}

// requireDelivered fails the test unless the sink took the event.
func requireDelivered(t *testing.T, outcome relay.Outcome) {
	t.Helper()

	require.Equal(t, relay.OutcomeDelivered, outcome.Status, "outcome error: %v", outcome.Err)
	require.NoError(t, outcome.Err)
}

// fieldsOf narrows a stream entry to the string fields the contract is written
// in, failing the test on anything else.
func fieldsOf(t *testing.T, message goredis.XMessage) map[string]string {
	t.Helper()

	fields := make(map[string]string, len(message.Values))
	for name, value := range message.Values {
		text, ok := value.(string)
		require.True(t, ok, "field %q is %T, and every published field is a string", name, value)
		fields[name] = text
	}

	return fields
}

// newStreamSink returns a sink on client publishing to a stream no other case
// touches, and that stream.
func newStreamSink(
	t *testing.T,
	client goredis.UniversalClient,
	opts ...hmntskredis.Option,
) (sink *hmntskredis.Sink, stream string) {
	t.Helper()

	stream = fmt.Sprintf("hmntsk.test.%d", streams.Add(1))

	sink, err := hmntskredis.New(client, append([]hmntskredis.Option{
		hmntskredis.WithStream(stream),
	}, opts...)...)
	require.NoError(t, err)

	return sink, stream
}

// publishEvent delivers one event with the given identifier, failing the test
// unless the sink took it.
func publishEvent(t *testing.T, ctx context.Context, sink *hmntskredis.Sink, eventID string) {
	t.Helper()

	event := testEvent()
	event.ID = eventID
	requireDelivered(t, sink.Deliver(ctx, attempt(event)))
}

// eventIDs reads the event identifiers on a stream, oldest first.
func eventIDs(t *testing.T, ctx context.Context, client goredis.UniversalClient, stream string) []string {
	t.Helper()

	messages, err := client.XRange(ctx, stream, "-", "+").Result()
	require.NoError(t, err, "read the stream back")

	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, fieldsOf(t, message)[hmntskredis.FieldEventID])
	}

	return ids
}

// brokerProxy is a switchable TCP relay in front of the real broker, so a test
// can take the broker away and give it back without restarting a container —
// which would not come back on the same port.
//
// It starts closed: connections are accepted and dropped, which is what a
// broker that is no longer there looks like to a client.
type brokerProxy struct {
	listener net.Listener
	upstream string

	mu    sync.Mutex
	open  bool
	conns []net.Conn
}

// newBrokerProxy starts a proxy in front of upstream, closed.
func newBrokerProxy(t *testing.T, upstream string) *brokerProxy {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "start the broker proxy")

	proxy := &brokerProxy{listener: listener, upstream: upstream}

	t.Cleanup(func() {
		_ = listener.Close()
		proxy.drop()
	})

	go proxy.serve()

	return proxy
}

// Addr is the address a client dials to reach the proxy.
func (p *brokerProxy) Addr() string { return p.listener.Addr().String() }

// Open lets the broker be reached again.
func (p *brokerProxy) Open() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.open = true
}

// serve accepts connections for the life of the listener.
func (p *brokerProxy) serve() {
	for {
		downstream, err := p.listener.Accept()
		if err != nil {
			return
		}

		p.accept(downstream)
	}
}

// accept either wires a connection through to the broker or drops it.
func (p *brokerProxy) accept(downstream net.Conn) {
	p.mu.Lock()
	open := p.open
	p.mu.Unlock()

	if !open {
		_ = downstream.Close()

		return
	}

	upstream, err := net.Dial("tcp", p.upstream)
	if err != nil {
		_ = downstream.Close()

		return
	}

	p.mu.Lock()
	p.conns = append(p.conns, downstream, upstream)
	p.mu.Unlock()

	go func() { _, _ = io.Copy(upstream, downstream) }()
	go func() { _, _ = io.Copy(downstream, upstream) }()
}

// drop closes every connection the proxy has wired up.
func (p *brokerProxy) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conn := range p.conns {
		_ = conn.Close()
	}

	p.conns = nil
}
