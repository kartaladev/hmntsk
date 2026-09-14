package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/relay"
)

// TestWorkflowSinkDelivery hands the sink events directly, as a relay that
// repeats a delivery would, which a relay pass in a test never does.
func TestWorkflowSinkDelivery(t *testing.T) {
	t.Parallel()

	completed := func(taskType, output string) hmntsk.Event {
		return hmntsk.Event{
			Type:        hmntsk.EventTypeCompleted,
			TaskType:    taskType,
			Correlation: invoicing.Correlation("INV-201", invoicing.ActivityOf(taskType)),
			Output:      json.RawMessage(output),
		}
	}

	type testCase struct {
		name string
		// events are delivered in order, each to a fresh attempt.
		events []hmntsk.Event
		assert func(t *testing.T, outcomes []relay.Outcome, approvals int64, status string)
	}

	cases := []testCase{
		{
			name: "a matching review delivered twice asks for approval once",
			events: []hmntsk.Event{
				completed(invoicing.ReviewType, `{"matchesOrder":true}`),
				completed(invoicing.ReviewType, `{"matchesOrder":true}`),
			},
			assert: func(t *testing.T, outcomes []relay.Outcome, approvals int64, status string) {
				assert.Equal(t, relay.OutcomeDelivered, outcomes[0].Status)
				assert.Equal(t, relay.OutcomeDelivered, outcomes[1].Status)
				assert.Equal(t, int64(1), approvals)
				assert.Equal(t, orderAwaitingApproval, status)
			},
		},
		{
			name: "a review repeated after approval does not move the order back",
			events: []hmntsk.Event{
				completed(invoicing.ReviewType, `{"matchesOrder":true}`),
				completed(invoicing.ApproveType, `{"approved":true,"reason":"within-budget"}`),
				completed(invoicing.ReviewType, `{"matchesOrder":true}`),
			},
			assert: func(t *testing.T, _ []relay.Outcome, approvals int64, status string) {
				assert.Equal(t, int64(1), approvals)
				assert.Equal(t, orderApproved, status)
			},
		},
		{
			name: "an approval before any review decides nothing",
			events: []hmntsk.Event{
				completed(invoicing.ApproveType, `{"approved":true,"reason":"within-budget"}`),
			},
			assert: func(t *testing.T, outcomes []relay.Outcome, approvals int64, status string) {
				assert.Equal(t, relay.OutcomeDelivered, outcomes[0].Status)
				assert.Zero(t, approvals)
				assert.Equal(t, orderInReview, status)
			},
		},
		{
			name: "an event other than a completion is taken and ignored",
			events: []hmntsk.Event{
				{
					Type: hmntsk.EventTypeClaimed, TaskType: invoicing.ReviewType,
					Correlation: invoicing.Correlation("INV-201", invoicing.ActivityReview),
				},
			},
			assert: func(t *testing.T, outcomes []relay.Outcome, approvals int64, status string) {
				assert.Equal(t, relay.OutcomeDelivered, outcomes[0].Status)
				assert.Zero(t, approvals)
				assert.Equal(t, orderInReview, status)
			},
		},
		{
			name:   "an unreadable output is permanent, because retrying cannot fix it",
			events: []hmntsk.Event{completed(invoicing.ReviewType, `"yes"`)},
			assert: func(t *testing.T, outcomes []relay.Outcome, approvals int64, status string) {
				assert.Equal(t, relay.OutcomePermanent, outcomes[0].Status)
				assert.Error(t, outcomes[0].Err)
				assert.Zero(t, approvals)
				assert.Equal(t, orderInReview, status)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, _ := newTestServer(t)
			ctx := t.Context()

			_, _, err := s.openOrder(ctx, newOrder{
				Number: 201, RequestedBy: erin, Supplier: "Stark Industries", Description: "Laptops",
				Amount: 4200, TaskType: invoicing.ReviewType,
			})
			require.NoError(t, err)

			sink := &workflowSink{engine: s.engine, invoices: s.invoices, orders: s.orders}

			outcomes := make([]relay.Outcome, 0, len(tc.events))
			for i, event := range tc.events {
				outcomes = append(outcomes, sink.Deliver(ctx, relay.Attempt{Event: event, Number: i + 1}))
			}

			approvals, err := s.engine.Count(ctx, hmntsk.Query{
				OwnerType: invoicing.OwnerType, OwnerRef: "INV-201", ActivityKey: invoicing.ActivityApprove,
			})
			require.NoError(t, err)

			placed, found, err := s.orders.ByInvoice(ctx, "INV-201")
			require.NoError(t, err)
			require.True(t, found)

			tc.assert(t, outcomes, approvals, placed.Status)
		})
	}
}
