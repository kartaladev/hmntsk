package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/relay"
)

// workflowSink moves an order along as its invoice's tasks complete: a review
// that matches the order asks for approval, one that does not disputes the
// order, and an approval decides it.
//
// It is a relay sink, run by the same relay as the notification projector, so
// it acts only on completions that were committed. The relay delivers at least
// once, so every step is safe to repeat: the approval task's ID is derived
// from its invoice, and an order only moves forward from the status the step
// expects.
type workflowSink struct {
	engine   *hmntsk.Service
	invoices *invoicing.SQLRepository
	orders   *orderStore
}

var _ relay.Sink = (*workflowSink)(nil)

// Name is stable: the relay records per-sink acceptance under it.
func (*workflowSink) Name() string { return "invoice-workflow" }

func (k *workflowSink) Deliver(ctx context.Context, attempt relay.Attempt) relay.Outcome {
	event := attempt.Event
	if event.Type != hmntsk.EventTypeCompleted || event.Correlation.OwnerType != invoicing.OwnerType {
		return relay.Delivered()
	}

	invoiceID := event.Correlation.OwnerRef

	switch event.TaskType {
	case invoicing.ReviewType:
		var review struct {
			MatchesOrder bool `json:"matchesOrder"`
		}

		// The engine validated the output against the type's schema, so an
		// unreadable one will not become readable on a later attempt.
		if err := json.Unmarshal(event.Output, &review); err != nil {
			return relay.Permanent(fmt.Errorf("read review of %s: %w", invoiceID, err))
		}

		if !review.MatchesOrder {
			return outcome(k.orders.Advance(ctx, invoiceID, orderInReview, orderDisputed))
		}

		// The order moves before the approval exists. Created first, an
		// approval could be completed while a failed order update waited for
		// its retry, and its decision would find no order awaiting it. This
		// way a retry finds the order already moved, which changes nothing,
		// and only creates the approval.
		if err := k.orders.Advance(ctx, invoiceID, orderInReview, orderAwaitingApproval); err != nil {
			return relay.Retryable(err)
		}

		return outcome(k.requestApproval(ctx, invoiceID))

	case invoicing.ApproveType:
		var decision struct {
			Approved bool `json:"approved"`
		}

		if err := json.Unmarshal(event.Output, &decision); err != nil {
			return relay.Permanent(fmt.Errorf("read approval of %s: %w", invoiceID, err))
		}

		status := orderRejected
		if decision.Approved {
			status = orderApproved
		}

		return outcome(k.orders.Advance(ctx, invoiceID, orderAwaitingApproval, status))
	}

	return relay.Delivered()
}

// requestApproval creates the invoice's approval task. Its ID is derived from
// the invoice, which is what makes the create idempotent: when the task
// already exists the engine answers a conflict, and that means the work is
// done, however many deliveries or relays asked.
func (k *workflowSink) requestApproval(ctx context.Context, invoiceID string) error {
	invoice, err := k.invoices.Get(ctx, invoiceID)
	if err != nil {
		return err
	}

	_, err = k.engine.Create(ctx, hmntsk.CreateRequest{
		ID:          hmntsk.TaskID("approval-" + invoiceID),
		Type:        invoicing.ApproveType,
		Actor:       billingService,
		Input:       invoicing.Input(invoice),
		Correlation: invoicing.Correlation(invoiceID, invoicing.ActivityApprove),
	})
	if err != nil && !errors.Is(err, hmntsk.ErrConflict) {
		return fmt.Errorf("create approval of %s: %w", invoiceID, err)
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
