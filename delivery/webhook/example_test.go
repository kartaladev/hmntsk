package webhook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/delivery/webhook"
	"github.com/kartaladev/hmntsk/relay"
)

// ExampleVerifier_Verify is the receiving half of the delivery contract: what a
// receiver writes to prove a delivery came from the engine it shares a secret
// with, and to discard one it has already handled.
//
// The delivery it checks is a real one — signed by [webhook.Sink] over a real
// event, delivered over a real connection — so this example cannot drift from
// what the sink actually sends.
func ExampleVerifier_Verify() {
	secret := []byte("the secret both ends were configured with")

	verifier, err := webhook.NewVerifier(secret)
	if err != nil {
		panic(err)
	}

	// A receiver's own idempotency store. An event identifier it has already
	// handled is a redelivery, and the work must not be done twice.
	handled := map[string]bool{}

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		// Verify before parsing. An unverified body is attacker-controlled.
		if err := verifier.Verify(r.Header, body); err != nil {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		eventID := r.Header.Get(webhook.HeaderEventID)
		if handled[eventID] {
			// Already done. 2xx, not an error: the engine is right to have
			// retried, and telling it so is how the retry stops.
			w.WriteHeader(http.StatusOK)
			fmt.Println("duplicate:", eventID)

			return
		}

		var payload webhook.Payload
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		handled[eventID] = true

		fmt.Println("verified:", payload.Event.Type, payload.Event.TaskID)
		fmt.Println("owner:", payload.Correlation.OwnerType, payload.Correlation.OwnerRef)

		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	// The engine's side. AllowLoopback is needed only because this example's
	// receiver is on 127.0.0.1; the default policy refuses it, which is the
	// point of the default.
	sink, err := webhook.New(secret, webhook.WithDestinationPolicy(webhook.AllowLoopback()))
	if err != nil {
		panic(err)
	}

	event := hmntsk.Event{
		ID:         "0193f0a1-1c00-7000-8000-000000000001",
		Type:       hmntsk.EventTypeCompleted,
		TaskID:     hmntsk.TaskID("0193f0a1-1c00-7000-8000-0000000000aa"),
		TaskType:   "approval",
		Status:     hmntsk.StatusCompleted,
		Version:    4,
		OccurredAt: time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC),
		Correlation: hmntsk.CorrelationData{
			OwnerType: "process",
			OwnerRef:  "loan-4711",
		},
		Callback: &hmntsk.CallbackTarget{Address: receiver.URL},
	}

	ctx := context.Background()

	first := sink.Deliver(ctx, relay.Attempt{Event: event, DeliveryID: "delivery-1", Number: 1})
	fmt.Println("first attempt:", first.Status)

	// The same event again, as a crashed relay would redeliver it.
	second := sink.Deliver(ctx, relay.Attempt{Event: event, DeliveryID: "delivery-2", Number: 2})
	fmt.Println("second attempt:", second.Status)

	// Output:
	// verified: task.completed 0193f0a1-1c00-7000-8000-0000000000aa
	// owner: process loan-4711
	// first attempt: delivered
	// duplicate: 0193f0a1-1c00-7000-8000-000000000001
	// second attempt: delivered
}
