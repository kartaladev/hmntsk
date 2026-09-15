package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
)

// taskID is the ID of an order's task for one activity. Deriving it is what
// makes every create idempotent: a repeated create is answered with a
// conflict, which means the task already exists.
func taskID(activity, orderID string) hmntsk.TaskID {
	return hmntsk.TaskID(activity + "-" + orderID)
}

// orderNumber is the number an order's ID and its invoice and documents share.
func orderNumber(orderID string) string {
	return strings.TrimPrefix(orderID, "ORD-")
}

// newOrder is what opening an order needs. A placed order starts awaiting
// approval; a seeded one may start further along, with a priority and deadline
// of its own.
type newOrder struct {
	Number      int
	RequestedBy string
	Supplier    string
	Description string
	Amount      int64
	// Stage is the status a seeded order starts at. Empty is a placed order.
	Stage    string
	Priority *hmntsk.Priority
	DueAt    *time.Time
}

// transact runs fn in one transaction and dispatches the events of the
// results it returns only after the transaction commits, as correlated-tasks
// shows. Either every record and task fn wrote exists, or none does.
func (s *server) transact(ctx context.Context, fn func(ctx context.Context) ([]hmntsk.Result, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	// A rollback after the commit does nothing, so every early return undoes
	// the transaction without saying so.
	defer func() { _ = tx.Rollback() }()

	results, err := fn(sqlstore.ContextWithTx(ctx, tx))
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	for _, result := range results {
		if err := result.Dispatch(ctx); err != nil {
			return fmt.Errorf("dispatch: %w", err)
		}
	}

	return nil
}

// openOrder saves the order, and whatever its stage needs, with the task its
// stage waits on, in one transaction. A placed order waits on its approval.
func (s *server) openOrder(ctx context.Context, spec newOrder) (order, hmntsk.Task, error) {
	now := s.engine.Clock().Now().UTC()
	registry, registered := registeredSupplier(spec.Supplier)

	stage := spec.Stage
	if stage == "" {
		stage = orderPendingApproval
	}

	record := order{
		ID:                 fmt.Sprintf("ORD-%d", spec.Number),
		Supplier:           spec.Supplier,
		Description:        spec.Description,
		Amount:             spec.Amount,
		RequestedBy:        spec.RequestedBy,
		Status:             stage,
		SupplierRegistered: registered,
		CreatedAt:          now,
	}

	var taskType string

	switch stage {
	case orderPendingApproval:
		taskType = approveOrderType
	case orderAwaitingPurchaseOrder:
		taskType = purchaseOrderType(record)
	case orderAwaitingInvoice:
		expected := now.Add(s.invoiceDelay())
		record.InvoiceExpectedAt = &expected
	case orderInvoiceReview, orderInvoiceApproval:
		record.InvoiceID = fmt.Sprintf("INV-%d", spec.Number)
		taskType = map[string]string{orderInvoiceReview: reviewInvoiceType, orderInvoiceApproval: approveInvoiceType}[stage]
	default:
		return order{}, hmntsk.Task{}, fmt.Errorf("an order cannot start at %q", stage)
	}

	var created hmntsk.Result

	err := s.transact(ctx, func(ctx context.Context) ([]hmntsk.Result, error) {
		if err := s.records.SaveOrder(ctx, spec.Number, record); err != nil {
			return nil, err
		}

		// An order past its purchase order has sent one, and one past review
		// has its invoice.
		if record.InvoiceExpectedAt != nil || record.InvoiceID != "" {
			if err := s.records.SaveDocument(ctx, sentPurchaseOrder(record, registry, now)); err != nil {
				return nil, err
			}
		}

		if record.InvoiceID != "" {
			if err := s.records.SaveInvoice(ctx, invoiceFor(record, now)); err != nil {
				return nil, err
			}
		}

		if taskType == "" {
			return nil, nil
		}

		var err error

		created, err = s.engine.Create(ctx, hmntsk.CreateRequest{
			ID:          taskID(activityOf(taskType), record.ID),
			Type:        taskType,
			Actor:       systemActor,
			Input:       taskInput(record),
			Correlation: correlation(record.ID, activityOf(taskType)),
			Priority:    spec.Priority,
			DueAt:       spec.DueAt,
		})
		if err != nil {
			return nil, fmt.Errorf("create %s task: %w", taskType, err)
		}

		return []hmntsk.Result{created}, nil
	})
	if err != nil {
		return order{}, hmntsk.Task{}, fmt.Errorf("open order %s: %w", record.ID, err)
	}

	return record, created.Task, nil
}

// purchaseOrderType is how an order's purchase order is issued: sent to a
// supplier on the registry, uploaded for anyone else.
func purchaseOrderType(record order) string {
	if record.SupplierRegistered {
		return sendOrderType
	}

	return uploadOrderType
}

// invoiceFor is the invoice a supplier bills an order with. The demo's
// suppliers bill exactly what was ordered; the review is where a person checks.
func invoiceFor(record order, receivedAt time.Time) invoice {
	return invoice{
		ID:         record.InvoiceID,
		OrderID:    record.ID,
		Supplier:   record.Supplier,
		Amount:     record.Amount,
		ReceivedAt: receivedAt,
	}
}

// receiveDueInvoices stands for the suppliers: every invoice whose expected
// time has passed arrives, and it reports how many did. An invoice that fails
// to arrive stays due, so the next pass tries it again.
func (s *server) receiveDueInvoices(ctx context.Context) (int, error) {
	now := s.engine.Clock().Now().UTC()

	due, err := s.records.InvoicesDue(ctx, now)
	if err != nil {
		return 0, err
	}

	var (
		received int
		errs     []error
	)

	for _, record := range due {
		arrived, err := s.receiveInvoice(ctx, record, now)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if arrived {
			received++
		}
	}

	return received, errors.Join(errs...)
}

// receiveInvoice saves the order's invoice, moves the order to review and
// creates the review, in one transaction. An order that has already moved on
// receives nothing, so two passes racing for the same invoice save it once.
func (s *server) receiveInvoice(ctx context.Context, record order, now time.Time) (bool, error) {
	record.InvoiceID = "INV-" + orderNumber(record.ID)
	arrived := false

	err := s.transact(ctx, func(ctx context.Context) ([]hmntsk.Result, error) {
		moved, err := s.records.ReceiveInvoice(ctx, record.ID, record.InvoiceID)
		if err != nil || !moved {
			return nil, err
		}

		if err := s.records.SaveInvoice(ctx, invoiceFor(record, now)); err != nil {
			return nil, err
		}

		created, err := s.engine.Create(ctx, hmntsk.CreateRequest{
			ID:          taskID(activityReviewInvoice, record.ID),
			Type:        reviewInvoiceType,
			Actor:       systemActor,
			Input:       taskInput(record),
			Correlation: correlation(record.ID, activityReviewInvoice),
		})
		if err != nil {
			return nil, fmt.Errorf("create review of %s: %w", record.InvoiceID, err)
		}

		arrived = true

		return []hmntsk.Result{created}, nil
	})
	if err != nil {
		return false, fmt.Errorf("receive invoice for %s: %w", record.ID, err)
	}

	return arrived, nil
}

func (s *server) listOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}

	orders, err := s.records.Orders(r.Context())
	if err != nil {
		http.Error(w, "list orders", http.StatusInternalServerError)

		return
	}

	writeJSON(w, http.StatusOK, map[string][]order{"orders": orders})
}

// placeOrder is erin buying something. The order waits for a budget holder to
// approve it before anything is ordered from the supplier.
func (s *server) placeOrder(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	if !slices.Contains(user.Groups, groupPurchasing) {
		http.Error(w, "only purchasing places orders", http.StatusForbidden)

		return
	}

	var req struct {
		Supplier    string `json:"supplier"`
		Description string `json:"description"`
		Amount      int64  `json:"amount"`
	}

	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "the body must be a JSON order", http.StatusBadRequest)

		return
	}

	req.Supplier = strings.TrimSpace(req.Supplier)
	req.Description = strings.TrimSpace(req.Description)

	if req.Supplier == "" || req.Description == "" || req.Amount <= 0 {
		http.Error(w, "an order needs a supplier, a description and a positive amount", http.StatusBadRequest)

		return
	}

	// A registered supplier is recorded under the registry's spelling.
	if registry, ok := registeredSupplier(req.Supplier); ok {
		req.Supplier = registry.Name
	}

	s.mu.Lock()
	number := s.nextNumber
	s.nextNumber++
	s.mu.Unlock()

	record, _, err := s.openOrder(r.Context(), newOrder{
		Number:      number,
		RequestedBy: user.ID,
		Supplier:    req.Supplier,
		Description: req.Description,
		Amount:      req.Amount,
	})
	if err != nil {
		http.Error(w, "place order", http.StatusInternalServerError)

		return
	}

	writeJSON(w, http.StatusCreated, record)
}

func listSuppliers(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string][]supplier{"suppliers": supplierRegistry})
}
