package nats_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	hmntsknats "github.com/kartaladev/hmntsk/delivery/nats"
	"github.com/kartaladev/hmntsk/relay"
)

// receiveWait bounds how long a case waits for a message it expects. It is
// generous because a container on a loaded CI runner is slow, and a case that
// expects nothing does not wait for it: see quietPeriod.
const receiveWait = 5 * time.Second

// quietPeriod is how long a case listens to be satisfied that nothing arrives.
const quietPeriod = 250 * time.Millisecond

// testEvent returns a fully populated event, so that a case narrowing one field
// is visibly about that field.
func testEvent() hmntsk.Event {
	occurred := time.Date(2026, time.September, 13, 10, 30, 0, 123456000, time.UTC)

	return hmntsk.Event{
		ID:         "evt-0001",
		Type:       hmntsk.EventTypeCompleted,
		TaskID:     "tsk-0001",
		TaskType:   "acme.approval",
		Status:     hmntsk.StatusCompleted,
		Version:    7,
		Actor:      "alice",
		Assignee:   "alice",
		OccurredAt: occurred,
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

// attempts counts the attempts handed out, so that no two share an identifier.
var attempts atomic.Int64

// attempt wraps an event as the relay would hand it to a sink: with a fresh
// identifier for this attempt.
func attempt(event hmntsk.Event) relay.Attempt {
	n := attempts.Add(1)

	return relay.Attempt{
		Event:      event,
		DeliveryID: fmt.Sprintf("delivery-%d", n),
		Number:     int(n),
	}
}

// prefixes hands out a distinct subject prefix per call.
var prefixes atomic.Uint64

// uniquePrefix returns a subject prefix no other case publishes under, so cases
// sharing one server cannot see each other's messages.
func uniquePrefix() string {
	return fmt.Sprintf("hmntsk.test%d", prefixes.Add(1))
}

// smallPayloadConfig starts a server whose maximum payload oversizedEvent
// exceeds. The two numbers mean something only together, so they live together.
const smallPayloadConfig = "max_payload: 1024"

// oversizedEvent returns an event whose message is larger than a server started
// with smallPayloadConfig accepts.
func oversizedEvent() hmntsk.Event {
	event := testEvent()
	event.Output = json.RawMessage(`{"blob":"` + strings.Repeat("x", 4096) + `"}`)

	return event
}

// contractHeader is the documented header set of the message for sent, an
// attempt at testEvent. Both suites assert it, so the contract is written once.
func contractHeader(sent relay.Attempt) natsgo.Header {
	return natsgo.Header{
		"Content-Type":               {hmntsknats.ContentType},
		hmntsknats.HeaderSchema:      {hmntsknats.Schema},
		hmntsknats.HeaderEventID:     {"evt-0001"},
		hmntsknats.HeaderDeliveryID:  {sent.DeliveryID},
		hmntsknats.HeaderAttempt:     {strconv.Itoa(sent.Number)},
		hmntsknats.HeaderEventType:   {"task.completed"},
		hmntsknats.HeaderTaskID:      {"tsk-0001"},
		hmntsknats.HeaderTaskType:    {"acme.approval"},
		hmntsknats.HeaderOwnerType:   {"process"},
		hmntsknats.HeaderOwnerRef:    {"ord-42"},
		hmntsknats.HeaderActivityKey: {"approve-invoice"},
	}
}

// requireOutcome fails the test unless the sink returned the verdict want.
func requireOutcome(t *testing.T, want relay.OutcomeStatus, outcome relay.Outcome) {
	t.Helper()

	require.Equal(t, want, outcome.Status, "outcome error: %v", outcome.Err)
}

// requireDelivered fails the test unless the sink took the event.
func requireDelivered(t *testing.T, outcome relay.Outcome) {
	t.Helper()

	requireOutcome(t, relay.OutcomeDelivered, outcome)
	require.NoError(t, outcome.Err)
}

// subscribe listens on subject until the test ends, and returns once the server
// has registered the interest — a publication racing a subscription that the
// server has not yet seen would be lost, and the case would fail for a reason
// that has nothing to do with the sink.
func subscribe(t *testing.T, conn *natsgo.Conn, subject string) <-chan *natsgo.Msg {
	t.Helper()

	messages := make(chan *natsgo.Msg, 16)

	subscription, err := conn.ChanSubscribe(subject, messages)
	require.NoError(t, err, "subscribe to %s", subject)

	t.Cleanup(func() { _ = subscription.Unsubscribe() })

	require.NoError(t, conn.FlushTimeout(receiveWait), "register the subscription with the server")

	return messages
}

// nextMessage returns the next message, or nil when none arrives within wait.
func nextMessage(messages <-chan *natsgo.Msg, wait time.Duration) *natsgo.Msg {
	select {
	case message := <-messages:
		return message
	case <-time.After(wait):
		return nil
	}
}
