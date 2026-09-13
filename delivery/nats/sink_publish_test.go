package nats_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/kartaladev/hmntsk"
	hmntsknats "github.com/kartaladev/hmntsk/delivery/nats"
	"github.com/kartaladev/hmntsk/relay"
)

// TestPublishToSubjects runs the plain-subject sink against a real server.
//
// A container rather than a fake: what is being asserted is what a subscriber
// actually receives — the subject, the headers after the client has framed them,
// and the body — and a stand-in would assert its own idea of those.
func TestPublishToSubjects(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(plainSuite))
}

// plainSuite shares one server across its cases. The container is the
// expensive part; a subscription per case is free.
type plainSuite struct {
	suite.Suite

	conn *natsgo.Conn
}

// SetupSuite starts the one server every case publishes to.
func (s *plainSuite) SetupSuite() {
	s.conn = hmntsknats.RunTestNATS(s.T())
}

// TestPublishesEveryEventWithItsRoutingData is the consumer contract: what a
// subscriber receives is enough to route on without parsing the body, and the
// body is the whole event.
func (s *plainSuite) TestPublishesEveryEventWithItsRoutingData() {
	type testCase struct {
		name   string
		opts   []hmntsknats.Option
		filter string
		event  hmntsk.Event
		assert func(t *testing.T, sent relay.Attempt, outcome relay.Outcome, message *natsgo.Msg)
	}

	noCorrelation := testEvent()
	noCorrelation.ID = "evt-no-correlation"
	noCorrelation.Correlation = hmntsk.CorrelationData{}

	invalidToken := testEvent()
	invalidToken.ID = "evt-invalid-token"
	invalidToken.TaskType = "acme approval.*"

	lineBreak := testEvent()
	lineBreak.ID = "evt-line-break"
	lineBreak.TaskType = "acme\napproval"

	claimed := testEvent()
	claimed.ID = "evt-claimed"
	claimed.Type = hmntsk.EventTypeClaimed

	cases := []testCase{
		{
			name:   "a full event",
			filter: "hmntsk.events.>",
			event:  testEvent(),
			assert: func(t *testing.T, sent relay.Attempt, outcome relay.Outcome, message *natsgo.Msg) {
				requireDelivered(t, outcome)
				require.NotNil(t, message, "the subscriber must receive the event")

				assert.Equal(t, "hmntsk.events.task.completed", message.Subject)
				assert.Equal(t, contractHeader(sent), message.Header,
					"the header set is the documented consumer contract")

				// The whole event travels in the body, byte for byte what the
				// Redis sink's event field carries.
				want, err := json.Marshal(sent.Event)
				require.NoError(t, err)
				assert.JSONEq(t, string(want), string(message.Data))

				var event hmntsk.Event
				require.NoError(t, json.Unmarshal(message.Data, &event))
				assert.JSONEq(t, `{"decision":"approve"}`, string(event.Output))
				require.NotNil(t, event.Callback)
				assert.Equal(t, "https://example.invalid/hooks/tasks", event.Callback.Address)
				assert.Equal(t, map[string]string{"tenant": "acme"}, event.Correlation.Extra)

				// The audience snapshot travels in the body only; the header set
				// asserted above is unchanged by it.
				assert.Equal(t, hmntsk.CandidatePool{
					Users: []string{"alice", "bob"}, Groups: []string{"finance-approvers"}, Excluded: []string{"mallory"},
				}, event.Candidates)
				assert.Equal(t, "carol", event.PreviousAssignee)
				assert.Equal(t, "owner", event.CreatedBy)
			},
		},
		{
			name:   "no correlation data carries no correlation headers",
			filter: "hmntsk.events.>",
			event:  noCorrelation,
			assert: func(t *testing.T, _ relay.Attempt, outcome relay.Outcome, message *natsgo.Msg) {
				requireDelivered(t, outcome)
				require.NotNil(t, message, "the subscriber must receive the event")

				assert.Equal(t, "evt-no-correlation", message.Header.Get(hmntsknats.HeaderEventID))

				for _, header := range []string{
					hmntsknats.HeaderOwnerType,
					hmntsknats.HeaderOwnerRef,
					hmntsknats.HeaderActivityKey,
				} {
					assert.NotContains(t, message.Header, header, "an empty correlation value is omitted")
				}
			},
		},
		{
			name:   "a task type that is not a valid subject token leaves the subject alone",
			filter: "hmntsk.events.>",
			event:  invalidToken,
			assert: func(t *testing.T, _ relay.Attempt, outcome relay.Outcome, message *natsgo.Msg) {
				requireDelivered(t, outcome)
				require.NotNil(t, message, "the subscriber must receive the event")

				assert.Equal(t, "hmntsk.events.task.completed", message.Subject)
				assert.Equal(t, "acme approval.*", message.Header.Get(hmntsknats.HeaderTaskType))
			},
		},
		{
			name:   "a line break is sanitised in the header and kept in the body",
			filter: "hmntsk.events.>",
			event:  lineBreak,
			assert: func(t *testing.T, _ relay.Attempt, outcome relay.Outcome, message *natsgo.Msg) {
				requireDelivered(t, outcome)
				require.NotNil(t, message, "the subscriber must receive the event")

				assert.Equal(t, "acme approval", message.Header.Get(hmntsknats.HeaderTaskType),
					"the client replaces a line break in a header value with a space")

				var event hmntsk.Event
				require.NoError(t, json.Unmarshal(message.Data, &event))
				assert.Equal(t, "acme\napproval", event.TaskType, "the body is authoritative")
			},
		},
		{
			name:   "a configured prefix",
			opts:   []hmntsknats.Option{hmntsknats.WithSubjectPrefix("acme.tasks")},
			filter: "acme.tasks.>",
			event:  claimed,
			assert: func(t *testing.T, _ relay.Attempt, outcome relay.Outcome, message *natsgo.Msg) {
				requireDelivered(t, outcome)
				require.NotNil(t, message, "the subscriber must receive the event")

				assert.Equal(t, "acme.tasks.task.claimed", message.Subject)
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()

			messages := subscribe(t, s.conn, tc.filter)

			sink, err := hmntsknats.NewSink(s.conn, tc.opts...)
			require.NoError(t, err)

			sent := attempt(tc.event)
			outcome := sink.Deliver(t.Context(), sent)

			tc.assert(t, sent, outcome, nextMessage(messages, receiveWait))
		})
	}
}

// TestAnEventWithNoSubscriberIsDeliveredAndLost pins the property of plain
// NATS the sink states rather than hides: the server receiving a message is
// all delivered means, so an event nobody was listening for is delivered, and
// is not offered again.
func (s *plainSuite) TestAnEventWithNoSubscriberIsDeliveredAndLost() {
	t := s.T()

	prefix := uniquePrefix()

	sink, err := hmntsknats.NewSink(s.conn, hmntsknats.WithSubjectPrefix(prefix))
	require.NoError(t, err)

	requireDelivered(t, sink.Deliver(t.Context(), attempt(testEvent())))

	// A subscriber arriving afterwards finds nothing: plain subjects keep no
	// history.
	late := subscribe(t, s.conn, prefix+".>")
	assert.Nil(t, nextMessage(late, quietPeriod), "nothing is retained for a subscriber that was not there")
}

// TestAnOutageIsRetryableUntilTheServerIsBack is the requirement that makes
// the flush worth its round trip: while the server cannot be reached, the
// client buffers a publication and reports success, and only the missing
// receipt tells the sink that nothing arrived.
func (s *plainSuite) TestAnOutageIsRetryableUntilTheServerIsBack() {
	t := s.T()
	ctx := t.Context()

	proxy := newBrokerProxy(t, s.conn.ConnectedAddr())
	proxy.Open()

	disconnected := make(chan struct{}, 1)

	conn, err := natsgo.Connect(
		"nats://"+proxy.Addr(),
		// A host's connection that loses its server keeps trying rather than
		// failing, and buffers what it is given in the meantime.
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(50*time.Millisecond),
		natsgo.ReconnectJitter(0, 0),
		natsgo.DisconnectErrHandler(func(*natsgo.Conn, error) {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	// Connected first, so the client has the server's INFO — headers
	// supported, the maximum payload — and loses it afterwards, as a host's
	// long-lived connection does.
	proxy.Close()

	select {
	case <-disconnected:
	case <-time.After(receiveWait):
		require.Fail(t, "the client never noticed the server had gone")
	}

	prefix := uniquePrefix()
	messages := subscribe(t, s.conn, prefix+".>")

	// Short, because this attempt is certain to wait out its whole timeout.
	sink, err := hmntsknats.NewSink(
		conn,
		hmntsknats.WithSubjectPrefix(prefix),
		hmntsknats.WithTimeout(200*time.Millisecond),
	)
	require.NoError(t, err)

	event := testEvent()

	started := time.Now()
	outcome := sink.Deliver(ctx, attempt(event))
	elapsed := time.Since(started)

	require.Equal(t, relay.OutcomeRetryable, outcome.Status,
		"a publication the server never confirmed must not be delivered; outcome error: %v", outcome.Err)
	require.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)

	var publishErr *hmntsknats.PublishError
	require.ErrorAs(t, outcome.Err, &publishErr)
	assert.Equal(t, event.ID, publishErr.EventID)
	assert.Equal(t, prefix+".task.completed", publishErr.Subject)
	assert.ErrorIs(t, outcome.Err, context.DeadlineExceeded,
		"the client buffered the publication; only the missing receipt shows it did not arrive")
	assert.Less(t, elapsed, 3*time.Second, "the attempt ends at the sink's timeout")

	assert.Nil(t, nextMessage(messages, quietPeriod), "nothing reached the server while it was unreachable")

	proxy.Open()

	// The default timeout, so a slow reconnect is not mistaken for a failure.
	recovered, err := hmntsknats.NewSink(conn, hmntsknats.WithSubjectPrefix(prefix))
	require.NoError(t, err)

	requireDelivered(t, recovered.Deliver(ctx, attempt(event)))

	received := nextMessage(messages, receiveWait)
	require.NotNil(t, received, "once the server is back, the event reaches it")
	assert.Equal(t, event.ID, received.Header.Get(hmntsknats.HeaderEventID))
}

// TestAConnectionThatNeverConnectedIsRetryable covers a host that starts its
// relay while its server is still unreachable, with a connection built to keep
// trying. Until the first connect the client has no server information, so it
// refuses every message with headers as if the server could not carry them.
// That is a moment, not a property of the server: a permanent verdict would
// dead-letter every event published before the first connect.
func (s *plainSuite) TestAConnectionThatNeverConnectedIsRetryable() {
	t := s.T()

	proxy := newBrokerProxy(t, s.conn.ConnectedAddr())

	conn, err := natsgo.Connect(
		"nats://"+proxy.Addr(),
		natsgo.RetryOnFailedConnect(true),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(50*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	prefix := uniquePrefix()
	messages := subscribe(t, s.conn, prefix+".>")

	sink, err := hmntsknats.NewSink(conn, hmntsknats.WithSubjectPrefix(prefix))
	require.NoError(t, err)

	outcome := sink.Deliver(t.Context(), attempt(testEvent()))

	require.Equal(t, relay.OutcomeRetryable, outcome.Status, "outcome error: %v", outcome.Err)
	assert.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)
	assert.ErrorIs(t, outcome.Err, natsgo.ErrHeadersNotSupported)
	assert.Nil(t, nextMessage(messages, quietPeriod), "nothing reached the server")
}

// TestACancelledPassPublishesNothing covers a relay pass that was cancelled
// before the sink got to it. Publishing anyway and reporting the cancellation
// would be a guaranteed duplicate on the next pass.
func (s *plainSuite) TestACancelledPassPublishesNothing() {
	t := s.T()

	prefix := uniquePrefix()
	messages := subscribe(t, s.conn, prefix+".>")

	sink, err := hmntsknats.NewSink(s.conn, hmntsknats.WithSubjectPrefix(prefix))
	require.NoError(t, err)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	outcome := sink.Deliver(cancelled, attempt(testEvent()))

	require.Equal(t, relay.OutcomeRetryable, outcome.Status)
	assert.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)
	assert.ErrorIs(t, outcome.Err, context.Canceled)
	assert.Nil(t, nextMessage(messages, quietPeriod), "a pass already cancelled must not publish")
}

// TestFailuresNoAttemptCanChangeArePermanent pins the permanent verdicts. Each
// is a property of the event or of the server's configuration, so the next
// pass would fail in exactly the same place, and spending the remaining
// attempts on it would only delay the dead letter.
func (s *plainSuite) TestFailuresNoAttemptCanChangeArePermanent() {
	type testCase struct {
		name   string
		conn   func(t *testing.T) *natsgo.Conn
		event  hmntsk.Event
		assert func(t *testing.T, outcome relay.Outcome)
	}

	noID := testEvent()
	noID.ID = ""

	unmarshalable := testEvent()
	unmarshalable.Output = json.RawMessage(`{"unterminated":`)

	// Not a catalogue event type, which is the only way a validated prefix can
	// still yield a subject NATS refuses.
	badSubject := testEvent()
	badSubject.Type = "task completed"

	suiteConn := func(*testing.T) *natsgo.Conn { return s.conn }

	cases := []testCase{
		{
			name:  "an event without an identifier",
			conn:  suiteConn,
			event: noID,
			assert: func(t *testing.T, outcome relay.Outcome) {
				requireOutcome(t, relay.OutcomePermanent, outcome)
				require.ErrorIs(t, outcome.Err, hmntsknats.ErrInvalidEvent)

				var invalidErr *hmntsknats.InvalidEventError
				require.ErrorAs(t, outcome.Err, &invalidErr)
				assert.Equal(t, noID.TaskID, invalidErr.TaskID)
			},
		},
		{
			name:  "an event that will not marshal",
			conn:  suiteConn,
			event: unmarshalable,
			assert: func(t *testing.T, outcome relay.Outcome) {
				requireOutcome(t, relay.OutcomePermanent, outcome)
				assert.ErrorIs(t, outcome.Err, hmntsknats.ErrInvalidEvent)
			},
		},
		{
			name: "a message larger than the server accepts",
			conn: func(t *testing.T) *natsgo.Conn {
				return hmntsknats.RunTestNATS(t, hmntsknats.WithTestServerConfig(smallPayloadConfig))
			},
			event: oversizedEvent(),
			assert: func(t *testing.T, outcome relay.Outcome) {
				requireOutcome(t, relay.OutcomePermanent, outcome)
				require.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)
				assert.ErrorIs(t, outcome.Err, natsgo.ErrMaxPayload)
			},
		},
		{
			name:  "a subject the client refuses",
			conn:  suiteConn,
			event: badSubject,
			assert: func(t *testing.T, outcome relay.Outcome) {
				requireOutcome(t, relay.OutcomePermanent, outcome)
				require.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)
				assert.ErrorIs(t, outcome.Err, natsgo.ErrBadSubject)
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()

			conn := tc.conn(t)
			prefix := uniquePrefix()
			messages := subscribe(t, conn, prefix+".>")

			sink, err := hmntsknats.NewSink(conn, hmntsknats.WithSubjectPrefix(prefix))
			require.NoError(t, err)

			tc.assert(t, sink.Deliver(t.Context(), attempt(tc.event)))
			assert.Nil(t, nextMessage(messages, quietPeriod), "nothing is published")
		})
	}
}
