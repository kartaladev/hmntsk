package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/delivery/webhook"
	"github.com/kartaladev/hmntsk/relay"
)

// recordedRequest is one request a test receiver was given.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// receiver is a test receiver that records every request it is given.
type receiver struct {
	*httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
}

// newReceiver starts a receiver answering through handler, which may be nil for
// one that accepts everything with 204.
func newReceiver(t *testing.T, handler http.HandlerFunc) *receiver {
	t.Helper()

	rec := &receiver{}

	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not require: this runs on the server's goroutine, and FailNow off
		// the test's goroutine is undefined. Record the failure and let the
		// assertions on the recorded request report it.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the delivery body: %s", err)
		}

		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   body,
		})
		rec.mu.Unlock()

		if handler == nil {
			w.WriteHeader(http.StatusNoContent)

			return
		}

		handler(w, r)
	}))
	t.Cleanup(rec.Close)

	return rec
}

// count reports how many requests reached the receiver.
func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.requests)
}

// last returns the most recent request, and fails the test when there is none.
func (r *receiver) last(t *testing.T) recordedRequest {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	require.NotEmpty(t, r.requests, "the receiver was given no request")

	return r.requests[len(r.requests)-1]
}

// addrPort returns the address the receiver listens on.
func (r *receiver) addrPort(t *testing.T) netip.AddrPort {
	t.Helper()

	parsed, err := netip.ParseAddrPort(r.Listener.Addr().String())
	require.NoError(t, err)

	return parsed
}

// hostnameURL rewrites the receiver's URL to reach it by name rather than by
// address, so that a test can tell a policy on the URL text apart from a policy
// on the address the name resolves to.
func (r *receiver) hostnameURL(t *testing.T) string {
	t.Helper()

	return "http://localhost:" + strconv.Itoa(int(r.addrPort(t).Port())) + "/hooks"
}

// newSink builds a sink permitting loopback destinations, which is what makes a
// receiver on 127.0.0.1 reachable at all.
func newSink(t *testing.T, opts ...webhook.Option) *webhook.Sink {
	t.Helper()

	sink, err := webhook.New(testSecret, append([]webhook.Option{
		webhook.WithDestinationPolicy(webhook.AllowLoopback()),
	}, opts...)...)
	require.NoError(t, err)

	return sink
}

// testEvent returns an event addressed at target, or at nothing when target is
// empty.
func testEvent(target string) hmntsk.Event {
	event := hmntsk.Event{
		ID:       "01920000-0000-7000-8000-000000000001",
		Type:     hmntsk.EventTypeCompleted,
		TaskID:   "task-1",
		TaskType: "approval",
		Status:   hmntsk.StatusCompleted,
		Version:  3,
		Actor:    "alice",
		Assignee: "alice",
		Candidates: hmntsk.CandidatePool{
			Users: []string{"alice", "bob"}, Groups: []string{"approvers"}, Excluded: []string{"mallory"},
		},
		PreviousAssignee: "carol",
		CreatedBy:        "owner",
		OccurredAt:       signedAt.Add(-time.Minute),
		Correlation: hmntsk.CorrelationData{
			OwnerType:   "process",
			OwnerRef:    "order-4711",
			ActivityKey: "approve-discount",
			Extra:       map[string]string{"tenant": "eu-2"},
		},
	}

	if target != "" {
		event.Callback = &hmntsk.CallbackTarget{Address: target}
	}

	return event
}

func TestNew(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		secret []byte
		opts   []webhook.Option
		assert func(t *testing.T, sink *webhook.Sink, err error)
	}

	cases := []testCase{
		{
			name:   "the sink is named webhook by default",
			secret: testSecret,
			assert: func(t *testing.T, sink *webhook.Sink, err error) {
				require.NoError(t, err)
				assert.Equal(t, "webhook", sink.Name())
			},
		},
		{
			name:   "the name is overridable, because it is written to the database",
			secret: testSecret,
			opts:   []webhook.Option{webhook.WithName("callbacks")},
			assert: func(t *testing.T, sink *webhook.Sink, err error) {
				require.NoError(t, err)
				assert.Equal(t, "callbacks", sink.Name())
			},
		},
		{
			name:   "an empty secret is refused at construction",
			secret: nil,
			assert: func(t *testing.T, sink *webhook.Sink, err error) {
				assert.Nil(t, sink)
				assert.ErrorIs(t, err, webhook.ErrConfiguration)
			},
		},
		{
			name:   "a blank name is refused at construction",
			secret: testSecret,
			opts:   []webhook.Option{webhook.WithName("   ")},
			assert: func(t *testing.T, sink *webhook.Sink, err error) {
				assert.Nil(t, sink)
				assert.ErrorIs(t, err, webhook.ErrConfiguration)
			},
		},
		{
			name:   "a non-positive timeout is refused at construction",
			secret: testSecret,
			opts:   []webhook.Option{webhook.WithTimeout(-time.Second)},
			assert: func(t *testing.T, sink *webhook.Sink, err error) {
				assert.Nil(t, sink)
				assert.ErrorIs(t, err, webhook.ErrConfiguration)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink, err := webhook.New(tc.secret, tc.opts...)
			tc.assert(t, sink, err)
		})
	}
}

func TestSinkDeliverWithoutCallbackAddress(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name  string
		event hmntsk.Event
	}

	withEmptyTarget := testEvent("")
	withEmptyTarget.Callback = &hmntsk.CallbackTarget{}

	cases := []testCase{
		{name: "no callback target at all", event: testEvent("")},
		{name: "a callback target with an empty address", event: withEmptyTarget},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := newReceiver(t, nil)
			outcome := newSink(t).Deliver(t.Context(), attempt(tc.event))

			assert.Equal(t, relay.OutcomeDelivered, outcome.Status)
			assert.NoError(t, outcome.Err)
			assert.Zero(t, rec.count(), "no HTTP request may be made")
		})
	}
}

func TestSinkDeliverRequest(t *testing.T) {
	t.Parallel()

	rec := newReceiver(t, nil)
	event := testEvent(rec.URL + "/hooks/tasks")
	event.Callback.ReferenceParameters = json.RawMessage(
		`{"wsa:To":"urn:acme:orders","x-acme-tenant":{"id":7,"shard":"eu-2"}}`,
	)

	sink := newSink(t)

	first := sink.Deliver(t.Context(), attempt(event))
	require.Equal(t, relay.OutcomeDelivered, first.Status, "%v", first.Err)

	firstRequest := rec.last(t)

	second := sink.Deliver(t.Context(), attempt(event))
	require.Equal(t, relay.OutcomeDelivered, second.Status, "%v", second.Err)

	secondRequest := rec.last(t)

	t.Run("it posts to the callback address", func(t *testing.T) {
		assert.Equal(t, http.MethodPost, firstRequest.Method)
		assert.Equal(t, "/hooks/tasks", firstRequest.Path)
		assert.Equal(t, webhook.ContentType, firstRequest.Header.Get("Content-Type"))
	})

	t.Run("the headers carry what a receiver routes on", func(t *testing.T) {
		assert.Equal(t, event.ID, firstRequest.Header.Get(webhook.HeaderEventID))
		assert.Equal(t, string(event.Type), firstRequest.Header.Get(webhook.HeaderEventType))
		assert.Equal(t, string(event.TaskID), firstRequest.Header.Get(webhook.HeaderTaskID))
		assert.Equal(t, event.TaskType, firstRequest.Header.Get(webhook.HeaderTaskType))
		assert.Equal(t, "process", firstRequest.Header.Get(webhook.HeaderOwnerType))
		assert.Equal(t, "order-4711", firstRequest.Header.Get(webhook.HeaderOwnerRef))
		assert.Equal(t, "approve-discount", firstRequest.Header.Get(webhook.HeaderActivityKey))
		assert.NotEmpty(t, firstRequest.Header.Get(webhook.HeaderDeliveryID))
	})

	t.Run("a repeat carries the same event and a different delivery", func(t *testing.T) {
		assert.Equal(t,
			firstRequest.Header.Get(webhook.HeaderEventID),
			secondRequest.Header.Get(webhook.HeaderEventID),
			"the event identifier is what a receiver de-duplicates on")
		assert.NotEqual(t,
			firstRequest.Header.Get(webhook.HeaderDeliveryID),
			secondRequest.Header.Get(webhook.HeaderDeliveryID),
			"a repeat must be distinguishable from a new event")
	})

	t.Run("the body carries the event, the correlation and the delivery", func(t *testing.T) {
		var payload webhook.Payload
		require.NoError(t, json.Unmarshal(firstRequest.Body, &payload))

		assert.Equal(t, firstRequest.Header.Get(webhook.HeaderDeliveryID), payload.DeliveryID)
		assert.Equal(t, event.ID, payload.Event.ID)
		assert.Equal(t, event.Type, payload.Event.Type)
		assert.Equal(t, event.TaskID, payload.Event.TaskID)
		assert.Equal(t, event.TaskType, payload.Event.TaskType)
		assert.Equal(t, event.Status, payload.Event.Status)
		assert.Equal(t, event.Version, payload.Event.Version)
		assert.Equal(t, event.Actor, payload.Event.Actor)
		assert.True(t, event.OccurredAt.Equal(payload.Event.OccurredAt))
		assert.Equal(t, event.Correlation, payload.Correlation)
		assert.Equal(t, event.Candidates, payload.Event.Candidates, "the body carries the pool after the transition")
		assert.Equal(t, event.PreviousAssignee, payload.Event.PreviousAssignee, "the body names the holder replaced")
		assert.Equal(t, event.CreatedBy, payload.Event.CreatedBy, "the body names the task's creator")
	})

	t.Run("the reference parameters arrive byte for byte", func(t *testing.T) {
		assertVerbatim(t,
			string(event.Callback.ReferenceParameters),
			referenceParameters(t, firstRequest.Body),
			"reference parameters are opaque bytes, not a document to re-render")
	})

	t.Run("a receiver can verify the delivery", func(t *testing.T) {
		verifier, err := webhook.NewVerifier(testSecret)
		require.NoError(t, err)

		assert.NoError(t, verifier.Verify(firstRequest.Header, firstRequest.Body))

		tampered := append([]byte(nil), firstRequest.Body...)
		tampered[len(tampered)-1] = ' '
		assert.ErrorIs(t, verifier.Verify(firstRequest.Header, tampered), webhook.ErrSignatureMismatch)

		stale, err := webhook.NewVerifier(
			testSecret,
			webhook.WithTolerance(time.Minute),
			webhook.WithVerifierClock(hmntsk.ClockFunc(func() time.Time { return time.Now().Add(time.Hour) })),
		)
		require.NoError(t, err)
		assert.ErrorIs(t, stale.Verify(firstRequest.Header, firstRequest.Body), webhook.ErrSignatureStale,
			"a captured delivery must go out of date")
	})
}

func TestSinkDeliverClassifiesTheResponse(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		status int
		assert func(t *testing.T, outcome relay.Outcome)
	}

	delivered := func(t *testing.T, outcome relay.Outcome) {
		t.Helper()

		assert.Equal(t, relay.OutcomeDelivered, outcome.Status)
		assert.NoError(t, outcome.Err)
	}

	classified := func(want relay.OutcomeStatus) func(t *testing.T, outcome relay.Outcome) {
		return func(t *testing.T, outcome relay.Outcome) {
			t.Helper()

			assert.Equal(t, want, outcome.Status)
			assert.ErrorIs(t, outcome.Err, webhook.ErrStatus)
		}
	}

	cases := []testCase{
		{name: "200 is delivered", status: http.StatusOK, assert: delivered},
		{name: "201 is delivered", status: http.StatusCreated, assert: delivered},
		{name: "202 is delivered", status: http.StatusAccepted, assert: delivered},
		{name: "204 is delivered", status: http.StatusNoContent, assert: delivered},

		{name: "400 is permanent", status: http.StatusBadRequest, assert: classified(relay.OutcomePermanent)},
		{name: "401 is permanent", status: http.StatusUnauthorized, assert: classified(relay.OutcomePermanent)},
		{name: "403 is permanent", status: http.StatusForbidden, assert: classified(relay.OutcomePermanent)},
		{name: "404 is permanent", status: http.StatusNotFound, assert: classified(relay.OutcomePermanent)},
		{name: "410 is permanent", status: http.StatusGone, assert: classified(relay.OutcomePermanent)},
		{name: "422 is permanent", status: http.StatusUnprocessableEntity, assert: classified(relay.OutcomePermanent)},

		{name: "408 is retryable", status: http.StatusRequestTimeout, assert: classified(relay.OutcomeRetryable)},
		{name: "429 is retryable", status: http.StatusTooManyRequests, assert: classified(relay.OutcomeRetryable)},

		{name: "500 is retryable", status: http.StatusInternalServerError, assert: classified(relay.OutcomeRetryable)},
		{name: "502 is retryable", status: http.StatusBadGateway, assert: classified(relay.OutcomeRetryable)},
		{name: "503 is retryable", status: http.StatusServiceUnavailable, assert: classified(relay.OutcomeRetryable)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := newReceiver(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("receiver says so"))
			})

			outcome := newSink(t).Deliver(t.Context(), attempt(testEvent(rec.URL+"/hooks")))

			require.Equal(t, 1, rec.count())
			tc.assert(t, outcome)
		})
	}
}

func TestSinkDeliverTransportFailureIsRetryable(t *testing.T) {
	t.Parallel()

	// A listener that is closed before the delivery: nothing answers, and
	// nothing about that is the event's fault.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := "http://" + listener.Addr().String() + "/hooks"
	require.NoError(t, listener.Close())

	outcome := newSink(t).Deliver(t.Context(), attempt(testEvent(address)))

	assert.Equal(t, relay.OutcomeRetryable, outcome.Status)
	assert.Error(t, outcome.Err)
}

func TestSinkDeliverAbandonsAnUnresponsiveReceiver(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	rec := newReceiver(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	started := time.Now()
	outcome := newSink(t, webhook.WithTimeout(150*time.Millisecond)).
		Deliver(t.Context(), attempt(testEvent(rec.URL+"/hooks")))

	assert.Equal(t, relay.OutcomeRetryable, outcome.Status,
		"a receiver that never answers must not dead-letter the event")
	assert.ErrorIs(t, outcome.Err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), 5*time.Second,
		"the attempt must be abandoned at the timeout, not held open")
}

func TestSinkDeliverUnusableAddress(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		address string
	}

	cases := []testCase{
		{name: "not a url at all", address: "://not a url"},
		{name: "a scheme this sink does not speak", address: "ftp://receiver.example.com/hooks"},
		{name: "a file url", address: "file:///etc/passwd"},
		{name: "no host", address: "http:///hooks"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			outcome := newSink(t).Deliver(t.Context(), attempt(testEvent(tc.address)))

			assert.Equal(t, relay.OutcomePermanent, outcome.Status)
			assert.ErrorIs(t, outcome.Err, webhook.ErrAddress)
		})
	}
}

func TestSinkDeliverRefusesInternalDestinationsByDefault(t *testing.T) {
	t.Parallel()

	rec := newReceiver(t, nil)

	// The default policy, as a host that configures nothing would get it.
	sink, err := webhook.New(testSecret)
	require.NoError(t, err)

	type testCase struct {
		name    string
		address func() string
	}

	cases := []testCase{
		{
			name:    "an address written as a loopback IP",
			address: func() string { return rec.URL + "/hooks" },
		},
		{
			name: "a hostname whose text says nothing about where it resolves",
			// The URL carries no IP at all. A check on the text of the address
			// would have to resolve the name itself, and whatever it resolved
			// could have changed by the time the connection is made.
			address: func() string { return rec.hostnameURL(t) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			before := rec.count()
			outcome := sink.Deliver(t.Context(), attempt(testEvent(tc.address())))

			assert.Equal(t, relay.OutcomePermanent, outcome.Status,
				"retrying cannot change a policy verdict")
			assert.ErrorIs(t, outcome.Err, webhook.ErrDestinationRefused)
			assert.Equal(t, before, rec.count(), "the refused receiver must be given nothing")
		})
	}
}

func TestSinkDeliverJudgesTheResolvedAddress(t *testing.T) {
	t.Parallel()

	rec := newReceiver(t, nil)

	var (
		mu   sync.Mutex
		seen []webhook.Destination
	)

	sink := newSink(t, webhook.WithDestinationPolicy(webhook.PolicyFunc(
		func(_ context.Context, dest webhook.Destination) error {
			mu.Lock()
			seen = append(seen, dest)
			mu.Unlock()

			return nil
		},
	)))

	outcome := sink.Deliver(t.Context(), attempt(testEvent(rec.hostnameURL(t))))
	require.Equal(t, relay.OutcomeDelivered, outcome.Status, "%v", outcome.Err)

	mu.Lock()
	defer mu.Unlock()

	// A name with both an A and an AAAA record is dialled on both, so what is
	// asserted is that every address dialled was judged — not how many.
	require.NotEmpty(t, seen, "the policy is consulted at dial time")

	for _, dest := range seen {
		assert.Equal(t, "localhost", dest.Host, "the hostname is context for the policy")
		assert.True(t, dest.IP.Unmap().IsLoopback(),
			"the policy must be given the resolved address, not the name: %s", dest.IP)
		assert.Equal(t, rec.addrPort(t).Port(), dest.Port)
	}
}

func TestSinkDeliverUnderAHostPolicyPermittingAnInternalDestination(t *testing.T) {
	t.Parallel()

	permitted := newReceiver(t, nil)
	refused := newReceiver(t, nil)

	only := permitted.addrPort(t)

	sink := newSink(t, webhook.WithDestinationPolicy(webhook.PolicyFunc(
		func(_ context.Context, dest webhook.Destination) error {
			if dest.IP.Unmap() == only.Addr().Unmap() && dest.Port == only.Port() {
				return nil
			}

			return errors.New("only the sidecar is permitted")
		},
	)))

	t.Run("the permitted destination is delivered to", func(t *testing.T) {
		outcome := sink.Deliver(t.Context(), attempt(testEvent(permitted.URL+"/hooks")))

		assert.Equal(t, relay.OutcomeDelivered, outcome.Status, "%v", outcome.Err)
		assert.Equal(t, 1, permitted.count())
	})

	t.Run("every other destination is still refused", func(t *testing.T) {
		outcome := sink.Deliver(t.Context(), attempt(testEvent(refused.URL+"/hooks")))

		assert.Equal(t, relay.OutcomePermanent, outcome.Status)
		assert.ErrorIs(t, outcome.Err, webhook.ErrDestinationRefused)
		assert.Zero(t, refused.count())
	})
}

func TestSinkDeliverDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	internal := newReceiver(t, nil)
	redirector := newReceiver(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, internal.URL+"/hooks", http.StatusFound)
	})

	outcome := newSink(t).Deliver(t.Context(), attempt(testEvent(redirector.URL+"/hooks")))

	assert.Equal(t, relay.OutcomePermanent, outcome.Status)
	assert.ErrorIs(t, outcome.Err, webhook.ErrRedirect)
	assert.Equal(t, 1, redirector.count())
	assert.Zero(t, internal.count(), "the address the redirect pointed at must be given nothing")
}

// attempt wraps an event as the relay would hand it to a sink: with a fresh
// identifier for this attempt, which is what a receiver tells a redelivery from
// a first delivery by.
//
// The counter is atomic because the cases run in parallel, and two attempts
// sharing an identifier would be exactly the bug the identifier exists to rule
// out.
func attempt(event hmntsk.Event) relay.Attempt {
	n := attempts.Add(1)

	return relay.Attempt{
		Event:      event,
		DeliveryID: fmt.Sprintf("delivery-%d", n),
		Number:     int(n),
	}
}

// attempts counts the attempts handed out, so that no two share an identifier.
var attempts atomic.Int64
