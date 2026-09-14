package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
)

// Where an order has got to. hmntsk stores none of this: the order is the
// host's record, and the workflow moves it along as its invoice's tasks
// complete.
const (
	orderInReview         = "in-review"
	orderAwaitingApproval = "awaiting-approval"
	orderDisputed         = "disputed"
	orderApproved         = "approved"
	orderRejected         = "rejected"
)

// billingService creates every invoice task: the supplier's invoice arrives in
// billing, not from whoever placed the order.
const billingService = "billing-service"

// order is a purchase and the invoice it was billed with.
type order struct {
	ID          string    `json:"id"`
	InvoiceID   string    `json:"invoiceId"`
	Supplier    string    `json:"supplier"`
	Description string    `json:"description"`
	Amount      int64     `json:"amount"`
	RequestedBy string    `json:"requestedBy"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

// orderStore keeps orders next to the engine's tables and the invoices, and
// joins a transaction the host put on the context, as the invoice repository
// does.
type orderStore struct {
	db *sql.DB
}

func (o *orderStore) Migrate(ctx context.Context) error {
	_, err := o.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS orders (
  number INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  invoice_id TEXT NOT NULL UNIQUE,
  supplier TEXT NOT NULL,
  description TEXT NOT NULL,
  amount INTEGER NOT NULL,
  requested_by TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at INTEGER NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("create orders table: %w", err)
	}

	return nil
}

func (o *orderStore) Save(ctx context.Context, number int, record order) error {
	_, err := o.execer(ctx).ExecContext(ctx,
		`INSERT INTO orders (number, id, invoice_id, supplier, description, amount, requested_by, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		number, record.ID, record.InvoiceID, record.Supplier, record.Description, record.Amount,
		record.RequestedBy, record.Status, record.CreatedAt.UnixMicro())
	if err != nil {
		return fmt.Errorf("save order %s: %w", record.ID, err)
	}

	return nil
}

// Advance moves the invoice's order from one status to the next. An order no
// longer in from is left alone, so a delivery repeated after the order has
// moved on changes nothing.
func (o *orderStore) Advance(ctx context.Context, invoiceID, from, to string) error {
	_, err := o.execer(ctx).ExecContext(ctx,
		`UPDATE orders SET status = ? WHERE invoice_id = ? AND status = ?`, to, invoiceID, from)
	if err != nil {
		return fmt.Errorf("advance order for %s to %s: %w", invoiceID, to, err)
	}

	return nil
}

const orderColumns = `id, invoice_id, supplier, description, amount, requested_by, status, created_at`

// List is every order, newest first.
func (o *orderStore) List(ctx context.Context) ([]order, error) {
	rows, err := o.execer(ctx).QueryContext(ctx, `SELECT `+orderColumns+` FROM orders ORDER BY number DESC`)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	var orders []order

	for rows.Next() {
		record, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}

		orders = append(orders, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}

	return orders, nil
}

// ByInvoice is the order an invoice billed, if any.
func (o *orderStore) ByInvoice(ctx context.Context, invoiceID string) (order, bool, error) {
	record, err := scanOrder(o.execer(ctx).QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE invoice_id = ?`, invoiceID))
	if errors.Is(err, sql.ErrNoRows) {
		return order{}, false, nil
	}

	if err != nil {
		return order{}, false, err
	}

	return record, true, nil
}

func scanOrder(row interface{ Scan(dest ...any) error }) (order, error) {
	var (
		record    order
		createdAt int64
	)

	err := row.Scan(&record.ID, &record.InvoiceID, &record.Supplier, &record.Description, &record.Amount,
		&record.RequestedBy, &record.Status, &createdAt)
	if err != nil {
		return order{}, fmt.Errorf("scan order: %w", err)
	}

	record.CreatedAt = time.UnixMicro(createdAt).UTC()

	return record, nil
}

type orderExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (o *orderStore) execer(ctx context.Context) orderExecer {
	if tx, ok := sqlstore.TxFromContext(ctx); ok {
		return tx
	}

	return o.db
}

// newOrder is what opening an order needs. A placed order starts at review; a
// seeded one may start at approval, with a priority and deadline of its own.
type newOrder struct {
	Number      int
	RequestedBy string
	Supplier    string
	Description string
	Amount      int64
	TaskType    string
	Priority    *hmntsk.Priority
	DueAt       *time.Time
}

// openOrder saves the order, its invoice and the invoice's first task in one
// transaction, and dispatches the task's events only after it commits, as
// correlated-tasks shows. Either all three exist or none does.
func (s *server) openOrder(ctx context.Context, spec newOrder) (order, hmntsk.Task, error) {
	invoice := invoicing.Invoice{ID: fmt.Sprintf("INV-%d", spec.Number), Supplier: spec.Supplier, Amount: spec.Amount}

	status := orderInReview
	if spec.TaskType == invoicing.ApproveType {
		status = orderAwaitingApproval
	}

	record := order{
		ID:          fmt.Sprintf("ORD-%d", spec.Number),
		InvoiceID:   invoice.ID,
		Supplier:    spec.Supplier,
		Description: spec.Description,
		Amount:      spec.Amount,
		RequestedBy: spec.RequestedBy,
		Status:      status,
		CreatedAt:   s.engine.Clock().Now().UTC(),
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return order{}, hmntsk.Task{}, fmt.Errorf("begin: %w", err)
	}

	// A rollback after the commit does nothing, so every early return below
	// undoes the transaction without saying so.
	defer func() { _ = tx.Rollback() }()

	txCtx := sqlstore.ContextWithTx(ctx, tx)

	if err := s.invoices.Save(txCtx, invoice); err != nil {
		return order{}, hmntsk.Task{}, err
	}

	if err := s.orders.Save(txCtx, spec.Number, record); err != nil {
		return order{}, hmntsk.Task{}, err
	}

	created, err := s.engine.Create(txCtx, hmntsk.CreateRequest{
		Type:        spec.TaskType,
		Actor:       billingService,
		Input:       invoicing.Input(invoice),
		Correlation: invoicing.Correlation(invoice.ID, invoicing.ActivityOf(spec.TaskType)),
		Priority:    spec.Priority,
		DueAt:       spec.DueAt,
	})
	if err != nil {
		return order{}, hmntsk.Task{}, fmt.Errorf("create %s task: %w", spec.TaskType, err)
	}

	if err := tx.Commit(); err != nil {
		return order{}, hmntsk.Task{}, fmt.Errorf("commit order %s: %w", record.ID, err)
	}

	if err := created.Dispatch(ctx); err != nil {
		return order{}, hmntsk.Task{}, fmt.Errorf("dispatch: %w", err)
	}

	return record, created.Task, nil
}

func (s *server) listOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}

	orders, err := s.orders.List(r.Context())
	if err != nil {
		http.Error(w, "list orders", http.StatusInternalServerError)

		return
	}

	writeJSON(w, http.StatusOK, map[string][]order{"orders": orders})
}

// placeOrder is erin buying something. The supplier's invoice for it arrives
// straight away, which is the demo's shortcut for billing receiving it later.
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
		TaskType:    invoicing.ReviewType,
	})
	if err != nil {
		http.Error(w, "place order", http.StatusInternalServerError)

		return
	}

	writeJSON(w, http.StatusCreated, record)
}
