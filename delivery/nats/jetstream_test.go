package nats_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/kartaladev/hmntsk"
	hmntsknats "github.com/kartaladev/hmntsk/delivery/nats"
	"github.com/kartaladev/hmntsk/relay"
)

// TestPublishToJetStream runs the JetStream sink against a real server.
//
// What is asserted is what a stream actually stored, and whether the server
// discarded a duplicate, which only the server can say.
func TestPublishToJetStream(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(jetStreamSuite))
}

// jetStreamSuite shares one server across its cases. Each case creates the
// streams it needs, standing in for the host — which is the one thing the sink
// never does.
type jetStreamSuite struct {
	suite.Suite

	js jetstream.JetStream
}

// SetupSuite starts the one server every case publishes to, and refuses to go
// on unless it has JetStream: every case here would otherwise fail for a reason
// that has nothing to do with the sink.
func (s *jetStreamSuite) SetupSuite() {
	s.js = newJetStream(s.T())
}

// newJetStream starts a server and returns a JetStream context on it, failing
// the test unless JetStream is enabled.
func newJetStream(t *testing.T, opts ...hmntsknats.TestOption) jetstream.JetStream {
	t.Helper()

	js, err := jetstream.New(hmntsknats.RunTestNATS(t, opts...))
	require.NoError(t, err)

	_, err = js.AccountInfo(t.Context())
	require.NoError(t, err, "RunTestNATS must start the server with JetStream enabled")

	return js
}

// createStream creates a stream capturing a subject prefix no other case uses,
// as the host would, and returns that prefix and the stream.
func createStream(t *testing.T, js jetstream.JetStream) (prefix string, stream jetstream.Stream) {
	t.Helper()

	prefix = uniquePrefix()

	stream, err := js.CreateStream(t.Context(), jetstream.StreamConfig{
		// Stream names cannot contain dots.
		Name:     strings.ReplaceAll(prefix, ".", "_"),
		Subjects: []string{prefix + ".>"},
		Storage:  jetstream.MemoryStorage,
	})
	require.NoError(t, err, "create the stream the host would have created")

	return prefix, stream
}

// storedCount is how many messages a stream holds.
func storedCount(t *testing.T, stream jetstream.Stream) uint64 {
	t.Helper()

	info, err := stream.Info(t.Context())
	require.NoError(t, err, "read the stream's state")

	return info.State.Msgs
}

// streamNames lists every stream on the server.
func streamNames(t *testing.T, js jetstream.JetStream) []string {
	t.Helper()

	lister := js.StreamNames(t.Context())

	var names []string
	for name := range lister.Name() {
		names = append(names, name)
	}

	require.NoError(t, lister.Err(), "list the streams")

	return names
}

// TestAStreamStoresTheEvent is the JetStream consumer contract: the stream holds
// one message for the event, identified by the event, carrying the same
// subject, headers and body as a plain-subject publication.
func (s *jetStreamSuite) TestAStreamStoresTheEvent() {
	t := s.T()
	ctx := t.Context()

	prefix, stream := createStream(t, s.js)

	sink, err := hmntsknats.NewJetStreamSink(s.js, hmntsknats.WithSubjectPrefix(prefix))
	require.NoError(t, err)

	sent := attempt(testEvent())
	requireDelivered(t, sink.Deliver(ctx, sent))

	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs)

	stored, err := stream.GetMsg(ctx, info.State.LastSeq)
	require.NoError(t, err)

	assert.Equal(t, prefix+".task.completed", stored.Subject)

	wantHeader := contractHeader(sent)
	// The stream's own de-duplication key is the event, not the attempt.
	wantHeader[jetstream.MsgIDHeader] = []string{"evt-0001"}
	assert.Equal(t, wantHeader, stored.Header, "the stored header set is the documented consumer contract")

	want, err := json.Marshal(sent.Event)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(stored.Data), "the body is the whole event")
}

// TestARedeliveryWithinTheDuplicateWindowIsStoredOnce covers the redelivery the
// at-least-once contract produces. The relay mints a new delivery identifier
// for it; the stream must still see the same event.
func (s *jetStreamSuite) TestARedeliveryWithinTheDuplicateWindowIsStoredOnce() {
	t := s.T()
	ctx := t.Context()

	prefix, stream := createStream(t, s.js)

	sink, err := hmntsknats.NewJetStreamSink(s.js, hmntsknats.WithSubjectPrefix(prefix))
	require.NoError(t, err)

	event := testEvent()

	requireDelivered(t, sink.Deliver(ctx, attempt(event)))
	requireDelivered(t, sink.Deliver(ctx, attempt(event)))

	assert.EqualValues(t, 1, storedCount(t, stream),
		"a redelivery inside the duplicate window is acknowledged and discarded")
}

// TestNoStreamIsRetryableAfterOnePublication covers the host that has not
// created its stream. The sink must neither create one on the host's behalf nor
// retry on its own: the client's default is two retries 250ms apart, which
// would multiply the relay's attempt budget behind its back.
func (s *jetStreamSuite) TestNoStreamIsRetryableAfterOnePublication() {
	t := s.T()

	before := streamNames(t, s.js)

	sink, err := hmntsknats.NewJetStreamSink(s.js, hmntsknats.WithSubjectPrefix(uniquePrefix()))
	require.NoError(t, err)

	started := time.Now()
	outcome := sink.Deliver(t.Context(), attempt(testEvent()))
	elapsed := time.Since(started)

	requireOutcome(t, relay.OutcomeRetryable, outcome)
	require.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)
	assert.ErrorIs(t, outcome.Err, jetstream.ErrNoStreamResponse)

	// With the client's retry, the first retry alone waits this long.
	assert.Less(t, elapsed, jetstream.DefaultPubRetryWait,
		"one publication per attempt: the sink must not retry within it")

	assert.ElementsMatch(t, before, streamNames(t, s.js), "the sink must create no stream")
}

// TestAPublicationBoundForAnotherStreamIsRetryable covers WithExpectStream: a
// host with overlapping stream subjects names the stream it expects, and a
// publication that would land in another is refused before it is stored.
func (s *jetStreamSuite) TestAPublicationBoundForAnotherStreamIsRetryable() {
	t := s.T()

	prefix, captured := createStream(t, s.js)
	_, expected := createStream(t, s.js)

	sink, err := hmntsknats.NewJetStreamSink(s.js,
		hmntsknats.WithSubjectPrefix(prefix),
		hmntsknats.WithExpectStream(expected.CachedInfo().Config.Name),
	)
	require.NoError(t, err)

	outcome := sink.Deliver(t.Context(), attempt(testEvent()))

	requireOutcome(t, relay.OutcomeRetryable, outcome)
	assert.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)

	assert.Zero(t, storedCount(t, captured), "the stream capturing the subject must not store it")
	assert.Zero(t, storedCount(t, expected), "the expected stream does not capture the subject")
}

// TestACancelledPassStoresNothing covers a relay pass cancelled before the
// sink got to it. Storing the event and then reporting the cancellation would
// make every cancelled pass a redelivery. Today the client refuses a cancelled
// context before it sends; this pins that, so a sink change that detached the
// context, or a client that stopped checking, is noticed.
func (s *jetStreamSuite) TestACancelledPassStoresNothing() {
	t := s.T()

	prefix, stream := createStream(t, s.js)

	sink, err := hmntsknats.NewJetStreamSink(s.js, hmntsknats.WithSubjectPrefix(prefix))
	require.NoError(t, err)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	outcome := sink.Deliver(cancelled, attempt(testEvent()))

	requireOutcome(t, relay.OutcomeRetryable, outcome)
	assert.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)
	assert.ErrorIs(t, outcome.Err, context.Canceled)
	assert.Zero(t, storedCount(t, stream), "a pass already cancelled must not store the event")
}

// TestFailuresNoAttemptCanChangeArePermanent holds the plain-subject sink's
// permanent verdicts for JetStream too: the event and the server's maximum
// payload are the same on every pass.
func (s *jetStreamSuite) TestFailuresNoAttemptCanChangeArePermanent() {
	type testCase struct {
		name   string
		js     func(t *testing.T) jetstream.JetStream
		event  hmntsk.Event
		assert func(t *testing.T, outcome relay.Outcome)
	}

	noID := testEvent()
	noID.ID = ""

	cases := []testCase{
		{
			name:  "an event without an identifier",
			js:    func(*testing.T) jetstream.JetStream { return s.js },
			event: noID,
			assert: func(t *testing.T, outcome relay.Outcome) {
				requireOutcome(t, relay.OutcomePermanent, outcome)
				assert.ErrorIs(t, outcome.Err, hmntsknats.ErrInvalidEvent)
			},
		},
		{
			name: "a message larger than the server accepts",
			js: func(t *testing.T) jetstream.JetStream {
				return newJetStream(t, hmntsknats.WithTestServerConfig(smallPayloadConfig))
			},
			event: oversizedEvent(),
			assert: func(t *testing.T, outcome relay.Outcome) {
				requireOutcome(t, relay.OutcomePermanent, outcome)
				require.ErrorIs(t, outcome.Err, hmntsknats.ErrPublish)
				assert.ErrorIs(t, outcome.Err, natsgo.ErrMaxPayload)
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			t := s.T()

			js := tc.js(t)
			prefix, stream := createStream(t, js)

			sink, err := hmntsknats.NewJetStreamSink(js, hmntsknats.WithSubjectPrefix(prefix))
			require.NoError(t, err)

			tc.assert(t, sink.Deliver(t.Context(), attempt(tc.event)))
			assert.Zero(t, storedCount(t, stream), "nothing is stored")
		})
	}
}
