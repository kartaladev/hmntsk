package nats_test

import (
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmntsknats "github.com/kartaladev/hmntsk/delivery/nats"
)

// TestNew covers construction-time validation of both sinks: a wiring mistake
// is an error from the constructor, before a single event is published.
func TestNew(t *testing.T) {
	t.Parallel()

	// constructed is what a case can observe of a sink either constructor built.
	type constructed struct {
		name          string
		subjectPrefix string
	}

	type testCase struct {
		name      string
		construct func() (*constructed, error)
		assert    func(t *testing.T, sink *constructed, err error)
	}

	// Neither constructor dials, so a connection that never connected is enough
	// to build a sink on.
	conn := new(natsgo.Conn)

	js, err := jetstream.New(conn)
	require.NoError(t, err)

	plain := func(conn *natsgo.Conn, opts ...hmntsknats.Option) func() (*constructed, error) {
		return func() (*constructed, error) {
			sink, err := hmntsknats.NewSink(conn, opts...)
			if sink == nil {
				return nil, err
			}

			return &constructed{name: sink.Name(), subjectPrefix: sink.SubjectPrefix()}, err
		}
	}

	jetStream := func(js jetstream.JetStream, opts ...hmntsknats.JetStreamOption) func() (*constructed, error) {
		return func() (*constructed, error) {
			sink, err := hmntsknats.NewJetStreamSink(js, opts...)
			if sink == nil {
				return nil, err
			}

			return &constructed{name: sink.Name(), subjectPrefix: sink.SubjectPrefix()}, err
		}
	}

	rejected := func(t *testing.T, sink *constructed, err error) {
		t.Helper()

		require.ErrorIs(t, err, hmntsknats.ErrConfiguration)
		assert.Nil(t, sink)
	}

	cases := []testCase{
		{
			name:      "plain-subject defaults",
			construct: plain(conn),
			assert: func(t *testing.T, sink *constructed, err error) {
				require.NoError(t, err)
				require.NotNil(t, sink)
				assert.Equal(t, "nats", sink.name)
				assert.Equal(t, hmntsknats.DefaultName, sink.name)
				assert.Equal(t, "hmntsk.events", sink.subjectPrefix)
			},
		},
		{
			name:      "JetStream defaults",
			construct: jetStream(js),
			assert: func(t *testing.T, sink *constructed, err error) {
				require.NoError(t, err)
				require.NotNil(t, sink)
				assert.Equal(t, "jetstream", sink.name)
				assert.Equal(t, hmntsknats.DefaultJetStreamName, sink.name)
				assert.Equal(t, "hmntsk.events", sink.subjectPrefix)
				// The relay records acceptance per sink name: a shared default
				// would make a switch of mode skip every event the other mode
				// already took.
				assert.NotEqual(t, hmntsknats.DefaultName, sink.name,
					"the two modes must not share a default name")
			},
		},
		{
			name: "plain-subject name, prefix and timeout overridden",
			construct: plain(conn,
				hmntsknats.WithName("acme-bus"),
				hmntsknats.WithSubjectPrefix("acme.tasks"),
				hmntsknats.WithTimeout(2*time.Second),
			),
			assert: func(t *testing.T, sink *constructed, err error) {
				require.NoError(t, err)
				require.NotNil(t, sink)
				assert.Equal(t, "acme-bus", sink.name)
				assert.Equal(t, "acme.tasks", sink.subjectPrefix)
			},
		},
		{
			name: "JetStream name, prefix, timeout and expected stream overridden",
			construct: jetStream(js,
				hmntsknats.WithName("acme-stream"),
				hmntsknats.WithSubjectPrefix("acme.tasks"),
				hmntsknats.WithTimeout(2*time.Second),
				hmntsknats.WithExpectStream("ACME"),
			),
			assert: func(t *testing.T, sink *constructed, err error) {
				require.NoError(t, err)
				require.NotNil(t, sink)
				assert.Equal(t, "acme-stream", sink.name)
				assert.Equal(t, "acme.tasks", sink.subjectPrefix)
			},
		},
		{
			name:      "nil connection",
			construct: plain(nil),
			assert:    rejected,
		},
		{
			name:      "nil JetStream context",
			construct: jetStream(nil),
			assert:    rejected,
		},
		{
			// Naming no stream is not "expect none"; omitting the option is.
			// Accepting it would quietly publish without the check the host
			// asked for.
			name:      "empty expected stream",
			construct: jetStream(js, hmntsknats.WithExpectStream("")),
			assert:    rejected,
		},
	}

	// Every shared mistake is rejected by both constructors alike.
	shared := []struct {
		name string
		opt  hmntsknats.Option
	}{
		{"empty name", hmntsknats.WithName("")},
		{"zero timeout", hmntsknats.WithTimeout(0)},
		{"negative timeout", hmntsknats.WithTimeout(-time.Second)},
		{`empty prefix`, hmntsknats.WithSubjectPrefix("")},
		{`prefix "a..b" has an empty token`, hmntsknats.WithSubjectPrefix("a..b")},
		{`prefix ".a" has a leading dot`, hmntsknats.WithSubjectPrefix(".a")},
		{`prefix "a." has a trailing dot`, hmntsknats.WithSubjectPrefix("a.")},
		{`prefix "a.*" has a wildcard`, hmntsknats.WithSubjectPrefix("a.*")},
		{`prefix "a.>" has a full wildcard`, hmntsknats.WithSubjectPrefix("a.>")},
		{`prefix "a b" has a space`, hmntsknats.WithSubjectPrefix("a b")},
		{`prefix "a\tb" has a tab`, hmntsknats.WithSubjectPrefix("a\tb")},
	}

	for _, mistake := range shared {
		cases = append(cases,
			testCase{name: "plain-subject " + mistake.name, construct: plain(conn, mistake.opt), assert: rejected},
			testCase{name: "JetStream " + mistake.name, construct: jetStream(js, mistake.opt), assert: rejected},
		)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink, err := tc.construct()
			tc.assert(t, sink, err)
		})
	}
}
