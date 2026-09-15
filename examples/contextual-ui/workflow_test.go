package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// delivery is what one delivery to the sink returned, and what it left behind.
type delivery struct {
	outcome relay.Outcome
	// tasks counts the order's tasks by activity.
	tasks map[string]int64
	order order
}

// TestWorkflowSinkDelivery hands the sink events directly, as a relay that
// repeats a delivery would, which a relay pass in a test never does.
func TestWorkflowSinkDelivery(t *testing.T) {
	t.Parallel()

	completed := func(taskType, output string) hmntsk.Event {
		return hmntsk.Event{
			Type:        hmntsk.EventTypeCompleted,
			TaskType:    taskType,
			Correlation: correlation("ORD-201", activityOf(taskType)),
			Output:      json.RawMessage(output),
		}
	}

	approvedOrder := completed(approveOrderType, `{"approved":true}`)
	sentOrder := completed(sendOrderType, `{"documentId":"PO-201","sentTo":"procurement@globex.example"}`)
	matchingReview := completed(reviewInvoiceType, `{"matchesOrder":true}`)

	type testCase struct {
		name string
		// supplier chooses whether the order's supplier is registered.
		supplier string
		// status is where the order starts; empty is a freshly placed order.
		status string
		// events are delivered in order, each to a fresh attempt.
		events []hmntsk.Event
		// failFirst makes the first delivery's order update fail, as a database
		// that stays busy would; later deliveries succeed.
		failFirst bool
		assert    func(t *testing.T, deliveries []delivery)
	}

	cases := []testCase{
		{
			name:     "an approved order delivered twice asks a registered supplier's purchase order to be sent, once",
			supplier: "Globex Cloud",
			events:   []hmntsk.Event{approvedOrder, approvedOrder},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[0].outcome.Status)
				assert.Equal(t, relay.OutcomeDelivered, deliveries[1].outcome.Status)
				assert.Equal(t, int64(1), deliveries[1].tasks[sendOrderType])
				assert.Zero(t, deliveries[1].tasks[uploadOrderType])
				assert.Equal(t, orderAwaitingPurchaseOrder, deliveries[1].order.Status)
			},
		},
		{
			name:     "an approved order with an unregistered supplier asks for the purchase order to be uploaded",
			supplier: "Stark Industries",
			events:   []hmntsk.Event{approvedOrder},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, int64(1), deliveries[0].tasks[uploadOrderType])
				assert.Zero(t, deliveries[0].tasks[sendOrderType])
				assert.Equal(t, orderAwaitingPurchaseOrder, deliveries[0].order.Status)
			},
		},
		{
			name:     "a declined order asks for no purchase order",
			supplier: "Globex Cloud",
			events:   []hmntsk.Event{completed(approveOrderType, `{"approved":false,"note":"no budget"}`)},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[0].outcome.Status)
				assert.Zero(t, deliveries[0].tasks[sendOrderType])
				assert.Equal(t, orderDeclined, deliveries[0].order.Status)
			},
		},
		{
			// Were the purchase order task created first, purchasing could send
			// it while the order still waited for approval, and the completion
			// would find no order awaiting its purchase order.
			name:      "a failed order update asks for no purchase order until the retry moves the order",
			supplier:  "Globex Cloud",
			events:    []hmntsk.Event{approvedOrder, approvedOrder},
			failFirst: true,
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeRetryable, deliveries[0].outcome.Status)
				assert.Zero(t, deliveries[0].tasks[sendOrderType], "no purchase order while the order awaits approval")
				assert.Equal(t, orderPendingApproval, deliveries[0].order.Status)

				assert.Equal(t, relay.OutcomeDelivered, deliveries[1].outcome.Status)
				assert.Equal(t, int64(1), deliveries[1].tasks[sendOrderType])
			},
		},
		{
			name:     "a purchase order sent starts waiting for the supplier's invoice, after the delay",
			supplier: "Globex Cloud",
			status:   orderAwaitingPurchaseOrder,
			events:   []hmntsk.Event{sentOrder, sentOrder},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[1].outcome.Status)
				assert.Equal(t, orderAwaitingInvoice, deliveries[1].order.Status)
				require.NotNil(t, deliveries[1].order.InvoiceExpectedAt)
				assert.Equal(t, testStart.Add(testInvoiceDelay), *deliveries[1].order.InvoiceExpectedAt,
					"a repeated delivery does not push the invoice back")
			},
		},
		{
			name:     "a purchase order uploaded starts waiting for the invoice too",
			supplier: "Stark Industries",
			status:   orderAwaitingPurchaseOrder,
			events:   []hmntsk.Event{completed(uploadOrderType, `{"documentId":"PO-201"}`)},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, orderAwaitingInvoice, deliveries[0].order.Status)
			},
		},
		{
			name:     "a matching review delivered twice asks for approval once",
			supplier: "Globex Cloud",
			status:   orderInvoiceReview,
			events:   []hmntsk.Event{matchingReview, matchingReview},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[1].outcome.Status)
				assert.Equal(t, int64(1), deliveries[1].tasks[approveInvoiceType])
				assert.Equal(t, orderInvoiceApproval, deliveries[1].order.Status)
			},
		},
		{
			name:     "a review that does not match disputes the order",
			supplier: "Globex Cloud",
			status:   orderInvoiceReview,
			events:   []hmntsk.Event{completed(reviewInvoiceType, `{"matchesOrder":false,"note":"billed twice"}`)},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Zero(t, deliveries[0].tasks[approveInvoiceType])
				assert.Equal(t, orderDisputed, deliveries[0].order.Status)
			},
		},
		{
			name:     "a review repeated after approval does not move the order back",
			supplier: "Globex Cloud",
			status:   orderInvoiceReview,
			events: []hmntsk.Event{
				matchingReview,
				completed(approveInvoiceType, `{"approved":true,"reason":"within-budget"}`),
				matchingReview,
			},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, int64(1), deliveries[2].tasks[approveInvoiceType])
				assert.Equal(t, orderApproved, deliveries[2].order.Status)
			},
		},
		{
			name:     "a rejected invoice rejects the order",
			supplier: "Globex Cloud",
			status:   orderInvoiceApproval,
			events:   []hmntsk.Event{completed(approveInvoiceType, `{"approved":false,"reason":"over-budget"}`)},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, orderRejected, deliveries[0].order.Status)
			},
		},
		{
			name:     "an invoice approval before any review decides nothing",
			supplier: "Globex Cloud",
			status:   orderInvoiceReview,
			events:   []hmntsk.Event{completed(approveInvoiceType, `{"approved":true,"reason":"within-budget"}`)},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[0].outcome.Status)
				assert.Equal(t, orderInvoiceReview, deliveries[0].order.Status)
			},
		},
		{
			name:     "an event other than a completion is taken and ignored",
			supplier: "Globex Cloud",
			events: []hmntsk.Event{
				{Type: hmntsk.EventTypeClaimed, TaskType: approveOrderType, Correlation: correlation("ORD-201", activityApproveOrder)},
			},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomeDelivered, deliveries[0].outcome.Status)
				assert.Equal(t, orderPendingApproval, deliveries[0].order.Status)
			},
		},
		{
			name:     "an unreadable output is permanent, because retrying cannot fix it",
			supplier: "Globex Cloud",
			events:   []hmntsk.Event{completed(approveOrderType, `"yes"`)},
			assert: func(t *testing.T, deliveries []delivery) {
				assert.Equal(t, relay.OutcomePermanent, deliveries[0].outcome.Status)
				assert.Error(t, deliveries[0].outcome.Err)
				assert.Equal(t, orderPendingApproval, deliveries[0].order.Status)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := newTestServer(t)
			ctx := t.Context()

			_, _, err := ts.s.openOrder(ctx, newOrder{
				Number: 201, RequestedBy: erin, Supplier: tc.supplier, Description: "Laptops", Amount: 4200,
			})
			require.NoError(t, err)

			if tc.status != "" {
				_, err := ts.s.db.ExecContext(ctx, `UPDATE orders SET status = ? WHERE id = 'ORD-201'`, tc.status)
				require.NoError(t, err)
			}

			if tc.failFirst {
				_, err := ts.s.db.ExecContext(ctx,
					`CREATE TRIGGER fail_order_update BEFORE UPDATE ON orders BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
				require.NoError(t, err)
			}

			sink := ts.s.workflow

			deliveries := make([]delivery, 0, len(tc.events))

			for i, event := range tc.events {
				outcome := sink.Deliver(ctx, relay.Attempt{Event: event, Number: i + 1})

				if tc.failFirst && i == 0 {
					_, err := ts.s.db.ExecContext(ctx, `DROP TRIGGER fail_order_update`)
					require.NoError(t, err)
				}

				tasks := map[string]int64{}

				for _, taskType := range []string{approveOrderType, sendOrderType, uploadOrderType, reviewInvoiceType, approveInvoiceType} {
					page, err := ts.s.engine.Query(ctx, hmntsk.Query{OwnerType: ownerType, OwnerRef: "ORD-201", Types: []string{taskType}})
					require.NoError(t, err)

					tasks[taskType] = int64(len(page.Tasks))
				}

				placed, err := ts.s.records.Order(ctx, "ORD-201")
				require.NoError(t, err)

				deliveries = append(deliveries, delivery{outcome: outcome, tasks: tasks, order: placed})
			}

			tc.assert(t, deliveries)
		})
	}
}

// TestReceiveDueInvoices is the supplier's side of the demo: an invoice arrives
// once its expected time has passed, with its review, and only once. The seed's
// ORD-105 was sent its purchase order at the same moment as ORD-201, so its
// invoice is counted too.
func TestReceiveDueInvoices(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		// advance moves the clock past the moment the purchase order was sent.
		advance time.Duration
		// passes is how many times invoices are received.
		passes int
		assert func(t *testing.T, received int, record order, reviews int)
	}

	cases := []testCase{
		{
			name:    "before its expected time the invoice has not arrived",
			advance: testInvoiceDelay - time.Second,
			passes:  1,
			assert: func(t *testing.T, received int, record order, reviews int) {
				assert.Zero(t, received)
				assert.Equal(t, orderAwaitingInvoice, record.Status)
				assert.Empty(t, record.InvoiceID)
				assert.Zero(t, reviews)
			},
		},
		{
			name:    "once due, the invoice arrives with its review",
			advance: testInvoiceDelay,
			passes:  1,
			assert: func(t *testing.T, received int, record order, reviews int) {
				assert.Equal(t, 2, received, "ORD-201's and the seed's ORD-105's")
				assert.Equal(t, orderInvoiceReview, record.Status)
				assert.Equal(t, "INV-201", record.InvoiceID)
				assert.Equal(t, 1, reviews)
			},
		},
		{
			name:    "a second pass receives nothing more",
			advance: testInvoiceDelay,
			passes:  2,
			assert: func(t *testing.T, received int, record order, reviews int) {
				assert.Equal(t, 2, received, "counted across both passes: the first pass received both")
				assert.Equal(t, 1, reviews)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := newTestServer(t)
			ctx := t.Context()

			_, _, err := ts.s.openOrder(ctx, newOrder{
				Number: 201, RequestedBy: erin, Supplier: "Globex Cloud", Description: "Laptops", Amount: 4200,
			})
			require.NoError(t, err)

			_, err = ts.s.db.ExecContext(ctx, `UPDATE orders SET status = ? WHERE id = 'ORD-201'`, orderAwaitingPurchaseOrder)
			require.NoError(t, err)

			_, err = ts.s.records.AwaitInvoice(ctx, "ORD-201", ts.clock.Now().Add(testInvoiceDelay))
			require.NoError(t, err)

			ts.clock.Advance(tc.advance)

			received := 0

			for range tc.passes {
				n, err := ts.s.receiveDueInvoices(ctx)
				require.NoError(t, err)

				received += n
			}

			record, err := ts.s.records.Order(ctx, "ORD-201")
			require.NoError(t, err)

			page, err := ts.s.engine.Query(ctx, hmntsk.Query{OwnerType: ownerType, OwnerRef: "ORD-201", Types: []string{reviewInvoiceType}})
			require.NoError(t, err)

			tc.assert(t, received, record, len(page.Tasks))
		})
	}
}
