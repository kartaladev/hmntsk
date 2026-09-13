package redis_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
	"github.com/kartaladev/hmntsk/relay"
)

// unreachableAddr is a port nothing listens on, so a dial is refused rather
// than left hanging. Port 1 needs privileges to bind and is never a broker.
const unreachableAddr = "127.0.0.1:1"

// TestDeliverClassifiesFailures pins the outcome the relay is handed for every
// way a publish can go wrong.
//
// The classification is the whole point of the sink: a transient outage
// reported as permanent dead-letters a backlog the outbox exists to survive,
// and a malformed event reported as retryable spends every attempt failing in
// the same place.
func TestDeliverClassifiesFailures(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		client func(t *testing.T) goredis.UniversalClient
		opts   []hmntskredis.Option
		event  hmntsk.Event
		ctx    func(ctx context.Context) context.Context
		assert func(t *testing.T, outcome relay.Outcome, elapsed time.Duration)
	}

	cases := []testCase{
		{
			name: "broker unreachable is retryable",
			client: func(*testing.T) goredis.UniversalClient {
				return goredis.NewClient(&goredis.Options{
					Addr:        unreachableAddr,
					DialTimeout: time.Second,
					MaxRetries:  -1,
				})
			},
			event: testEvent(),
			assert: func(t *testing.T, outcome relay.Outcome, _ time.Duration) {
				assert.Equal(t, relay.OutcomeRetryable, outcome.Status)
				require.ErrorIs(t, outcome.Err, hmntskredis.ErrPublish)

				var publishErr *hmntskredis.PublishError
				require.ErrorAs(t, outcome.Err, &publishErr)
				assert.Equal(t, testEvent().ID, publishErr.EventID)
			},
		},
		{
			name: "broker that never acknowledges is abandoned at the timeout",
			client: func(t *testing.T) goredis.UniversalClient {
				return goredis.NewClient(&goredis.Options{
					Addr: hangingBroker(t),
					// Far longer than the sink's own timeout, so that what
					// ends the attempt is demonstrably the sink's bound and
					// not the client's.
					ReadTimeout: 10 * time.Second,
					MaxRetries:  -1,
				})
			},
			opts:  []hmntskredis.Option{hmntskredis.WithTimeout(500 * time.Millisecond)},
			event: testEvent(),
			assert: func(t *testing.T, outcome relay.Outcome, elapsed time.Duration) {
				assert.Equal(t, relay.OutcomeRetryable, outcome.Status)
				require.ErrorIs(t, outcome.Err, hmntskredis.ErrPublish)
				assert.ErrorIs(t, outcome.Err, context.DeadlineExceeded)
				assert.Less(t, elapsed, 10*time.Second,
					"the attempt must be abandoned at the timeout, not held open by the broker")
			},
		},
		{
			name: "cancelled context is retryable",
			client: func(*testing.T) goredis.UniversalClient {
				return goredis.NewClient(&goredis.Options{Addr: unreachableAddr, MaxRetries: -1})
			},
			event: testEvent(),
			ctx: func(ctx context.Context) context.Context {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				return cancelled
			},
			assert: func(t *testing.T, outcome relay.Outcome, _ time.Duration) {
				assert.Equal(t, relay.OutcomeRetryable, outcome.Status)
				assert.ErrorIs(t, outcome.Err, context.Canceled)
			},
		},
		{
			name: "event without an identifier is permanent",
			client: func(*testing.T) goredis.UniversalClient {
				return goredis.NewClient(&goredis.Options{Addr: unreachableAddr, MaxRetries: -1})
			},
			event: func() hmntsk.Event {
				event := testEvent()
				event.ID = ""

				return event
			}(),
			assert: func(t *testing.T, outcome relay.Outcome, _ time.Duration) {
				assert.Equal(t, relay.OutcomePermanent, outcome.Status)
				require.ErrorIs(t, outcome.Err, hmntskredis.ErrInvalidEvent)

				var invalidErr *hmntskredis.InvalidEventError
				require.ErrorAs(t, outcome.Err, &invalidErr)
				assert.Equal(t, testEvent().TaskID, invalidErr.TaskID)
			},
		},
		{
			name: "event that will not marshal is permanent",
			client: func(*testing.T) goredis.UniversalClient {
				return goredis.NewClient(&goredis.Options{Addr: unreachableAddr, MaxRetries: -1})
			},
			event: func() hmntsk.Event {
				event := testEvent()
				event.Output = json.RawMessage(`{"unterminated":`)

				return event
			}(),
			assert: func(t *testing.T, outcome relay.Outcome, _ time.Duration) {
				assert.Equal(t, relay.OutcomePermanent, outcome.Status)
				assert.ErrorIs(t, outcome.Err, hmntskredis.ErrInvalidEvent)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := tc.client(t)
			t.Cleanup(func() { _ = client.Close() })

			sink, err := hmntskredis.New(client, tc.opts...)
			require.NoError(t, err)

			ctx := t.Context()
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}

			started := time.Now()
			outcome := sink.Deliver(ctx, attempt(tc.event))
			tc.assert(t, outcome, time.Since(started))
		})
	}
}

// hangingBroker returns the address of a listener that accepts connections and
// answers nothing, which is how a broker that has taken a write and never
// acknowledged it looks from here.
func hangingBroker(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "start the hanging broker")

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			// Held, never answered, and closed only when the test ends.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	return listener.Addr().String()
}

// testEvent returns a fully populated event, so that a case narrowing one field
// is visibly about that field.
func testEvent() hmntsk.Event {
	occurred := time.Date(2026, time.September, 13, 10, 30, 0, 123456000, time.UTC)

	return hmntsk.Event{
		ID:       "evt-0001",
		Type:     hmntsk.EventTypeCompleted,
		TaskID:   "tsk-0001",
		TaskType: "acme.approval",
		Status:   hmntsk.StatusCompleted,
		Version:  7,
		Actor:    "alice",
		Assignee: "alice",
		Candidates: hmntsk.CandidatePool{
			Users: []string{"alice", "bob"}, Groups: []string{"finance-approvers"}, Excluded: []string{"mallory"},
		},
		PreviousAssignee: "carol",
		CreatedBy:        "owner",
		OccurredAt:       occurred,
		Correlation: hmntsk.CorrelationData{
			OwnerType:   "process",
			OwnerRef:    "ord-42",
			ActivityKey: "approve-invoice",
			Extra:       map[string]string{"tenant": "acme"},
		},
		Callback: &hmntsk.CallbackTarget{
			Address:             "https://example.invalid/hooks/tasks",
			ReferenceParameters: json.RawMessage(`{"ref":"r-1"}`),
		},
		Transition: hmntsk.TransitionRecord{
			TaskID:    "tsk-0001",
			Version:   7,
			Operation: hmntsk.OpComplete,
			From:      hmntsk.StatusInProgress,
			To:        hmntsk.StatusCompleted,
			Actor:     "alice",
			At:        occurred,
		},
		Reason: "approved",
		Output: json.RawMessage(`{"decision":"approve"}`),
	}
}

// attempt wraps an event as the relay would hand it to a sink: with a fresh
// identifier for this attempt.
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
