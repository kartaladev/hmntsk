package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// workflowSink moves an order along as its tasks complete:
//
//   - an approved order asks purchasing for its purchase order, sent to a
//     supplier on the registry or uploaded for anyone else, and a declined one
//     goes no further;
//   - an issued purchase order starts waiting for the supplier's invoice, whose
//     arrival [server.receiveDueInvoices] stands in for;
//   - a review that matches the order asks for the invoice's approval, and one
//     that does not disputes the order;
//   - the invoice's approval decides the order.
//
// It is a relay sink, run by the same relay as the notification projector, so
// it acts only on completions that were committed. The relay delivers at least
// once, so every step is safe to repeat: every task it creates has an ID
// derived from its order, and an order only moves forward from the status the
// step expects.
type workflowSink struct {
	engine       *hmntsk.Service
	records      *store
	invoiceDelay func() time.Duration
}

var _ relay.Sink = (*workflowSink)(nil)

// Name is stable: the relay records per-sink acceptance under it.
func (*workflowSink) Name() string { return "order-workflow" }

func (k *workflowSink) Deliver(ctx context.Context, attempt relay.Attempt) relay.Outcome {
	event := attempt.Event
	if event.Type != hmntsk.EventTypeCompleted || event.Correlation.OwnerType != ownerType {
		return relay.Delivered()
	}

	orderID := event.Correlation.OwnerRef

	switch event.TaskType {
	case approveOrderType:
		var decision struct {
			Approved bool `json:"approved"`
		}

		// The engine validated the output against the type's schema, so an
		// unreadable one will not become readable on a later attempt.
		if err := json.Unmarshal(event.Output, &decision); err != nil {
			return relay.Permanent(fmt.Errorf("read approval of %s: %w", orderID, err))
		}

		if !decision.Approved {
			return k.advance(ctx, orderID, orderPendingApproval, orderDeclined)
		}

		return k.advanceAndRequest(ctx, orderID, orderPendingApproval, orderAwaitingPurchaseOrder, purchaseOrderType)

	case sendOrderType, uploadOrderType:
		_, err := k.records.AwaitInvoice(ctx, orderID, k.engine.Clock().Now().UTC().Add(k.invoiceDelay()))

		return outcome(err)

	case reviewInvoiceType:
		var review struct {
			MatchesOrder bool `json:"matchesOrder"`
		}

		if err := json.Unmarshal(event.Output, &review); err != nil {
			return relay.Permanent(fmt.Errorf("read review of %s: %w", orderID, err))
		}

		if !review.MatchesOrder {
			return k.advance(ctx, orderID, orderInvoiceReview, orderDisputed)
		}

		return k.advanceAndRequest(ctx, orderID, orderInvoiceReview, orderInvoiceApproval,
			func(order) string { return approveInvoiceType })

	case approveInvoiceType:
		var decision struct {
			Approved bool `json:"approved"`
		}

		if err := json.Unmarshal(event.Output, &decision); err != nil {
			return relay.Permanent(fmt.Errorf("read invoice approval of %s: %w", orderID, err))
		}

		status := orderRejected
		if decision.Approved {
			status = orderApproved
		}

		return k.advance(ctx, orderID, orderInvoiceApproval, status)
	}

	return relay.Delivered()
}

func (k *workflowSink) advance(ctx context.Context, orderID, from, to string) relay.Outcome {
	_, err := k.records.AdvanceOrder(ctx, orderID, from, to)

	return outcome(err)
}

// advanceAndRequest moves the order, then creates the task its next status
// waits on.
//
// The order moves before the task exists. Created first, the task could be
// completed while a failed order update waited for its retry, and its
// completion would find no order waiting on it. This way a retry finds the
// order already moved, which changes nothing, and only creates the task.
func (k *workflowSink) advanceAndRequest(
	ctx context.Context, orderID, from, to string, taskTypeOf func(order) string,
) relay.Outcome {
	if _, err := k.records.AdvanceOrder(ctx, orderID, from, to); err != nil {
		return relay.Retryable(err)
	}

	record, err := k.records.Order(ctx, orderID)
	if errors.Is(err, errRecordNotFound) {
		return relay.Permanent(err)
	}

	if err != nil {
		return relay.Retryable(err)
	}

	return outcome(k.request(ctx, record, taskTypeOf(record)))
}

// request creates the order's task of taskType. Its ID is derived from the
// order, which is what makes the create idempotent: when the task already
// exists the engine answers a conflict, and that means the work is done,
// however many deliveries or relays asked.
func (k *workflowSink) request(ctx context.Context, record order, taskType string) error {
	activity := activityOf(taskType)

	_, err := k.engine.Create(ctx, hmntsk.CreateRequest{
		ID:          taskID(activity, record.ID),
		Type:        taskType,
		Actor:       systemActor,
		Input:       taskInput(record),
		Correlation: correlation(record.ID, activity),
	})
	if err != nil && !errors.Is(err, hmntsk.ErrConflict) {
		return fmt.Errorf("create %s for %s: %w", taskType, record.ID, err)
	}

	return nil
}

// outcome is a database write's result as a delivery outcome: a failed write
// may well succeed on the next attempt.
func outcome(err error) relay.Outcome {
	if err != nil {
		return relay.Retryable(err)
	}

	return relay.Delivered()
}
