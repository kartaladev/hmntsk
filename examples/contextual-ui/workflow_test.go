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

// delivery is what one delivery to the sink returned, and what it left behind.
type delivery struct {
	outcome   relay.Outcome
	approvals int64
	status    string
}

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

	matchingReview := completed(invoicing.ReviewType, `{"matchesOrder":true}`)

	type testCase struct {
		name string
		// events are delivered in order, each to a fresh attempt.
		events []hmntsk.Event
		// failFirst makes the first delivery's order update fail, as a database
		// that stays busy would; later deliveries succeed.
		failFirst bool
		assert    func(t *testing.T, deliveries []delivery)
	}

	cases := []testCase{
		{
			name:   "a matching review delivered twice asks for approval once",
			events: []hmntsk.Event{matchingReview, matchingReview},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[0].outcome.Status)
				assert.Equal(t, relay.OutcomeDelivered, deliveries[1].outcome.Status)
				assert.Equal(t, int64(1), deliveries[1].approvals)
				assert.Equal(t, orderAwaitingApproval, deliveries[1].status)
			},
		},
		{
			name: "a review repeated after approval does not move the order back",
			events: []hmntsk.Event{
				matchingReview,
				completed(invoicing.ApproveType, `{"approved":true,"reason":"within-budget"}`),
				matchingReview,
			},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, int64(1), deliveries[2].approvals)
				assert.Equal(t, orderApproved, deliveries[2].status)
			},
		},
		{
			// Were the approval created first, an approver could complete it
			// while the order still waited in review for the retry, and the
			// approval's decision would find no order awaiting it.
			name:      "a failed order update asks for no approval until the retry moves the order",
			events:    []hmntsk.Event{matchingReview, matchingReview},
			failFirst: true,
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeRetryable, deliveries[0].outcome.Status)
				assert.Zero(t, deliveries[0].approvals, "no approval while the order is still in review")
				assert.Equal(t, orderInReview, deliveries[0].status)

				assert.Equal(t, relay.OutcomeDelivered, deliveries[1].outcome.Status)
				assert.Equal(t, int64(1), deliveries[1].approvals)
				assert.Equal(t, orderAwaitingApproval, deliveries[1].status)
			},
		},
		{
			name: "an approval before any review decides nothing",
			events: []hmntsk.Event{
				completed(invoicing.ApproveType, `{"approved":true,"reason":"within-budget"}`),
			},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[0].outcome.Status)
				assert.Zero(t, deliveries[0].approvals)
				assert.Equal(t, orderInReview, deliveries[0].status)
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
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[0].outcome.Status)
				assert.Zero(t, deliveries[0].approvals)
				assert.Equal(t, orderInReview, deliveries[0].status)
			},
		},
		{
			name:   "an unreadable output is permanent, because retrying cannot fix it",
			events: []hmntsk.Event{completed(invoicing.ReviewType, `"yes"`)},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomePermanent, deliveries[0].outcome.Status)
				assert.Error(t, deliveries[0].outcome.Err)
				assert.Zero(t, deliveries[0].approvals)
				assert.Equal(t, orderInReview, deliveries[0].status)
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

			if tc.failFirst {
				_, err := s.db.ExecContext(ctx,
					`CREATE TRIGGER fail_order_update BEFORE UPDATE ON orders BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
				require.NoError(t, err)
			}

			sink := &workflowSink{engine: s.engine, invoices: s.invoices, orders: s.orders}

			deliveries := make([]delivery, 0, len(tc.events))

			for i, event := range tc.events {
				outcome := sink.Deliver(ctx, relay.Attempt{Event: event, Number: i + 1})

				if tc.failFirst && i == 0 {
					_, err := s.db.ExecContext(ctx, `DROP TRIGGER fail_order_update`)
					require.NoError(t, err)
				}

				approvals, err := s.engine.Count(ctx, hmntsk.Query{
					OwnerType: invoicing.OwnerType, OwnerRef: "INV-201", ActivityKey: invoicing.ActivityApprove,
				})
				require.NoError(t, err)

				placed, found, err := s.orders.ByInvoice(ctx, "INV-201")
				require.NoError(t, err)
				require.True(t, found)

				deliveries = append(deliveries, delivery{outcome: outcome, approvals: approvals, status: placed.Status})
			}

			tc.assert(t, deliveries)
		})
	}
}
