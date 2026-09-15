package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// The pure-Go SQLite driver, registered as "sqlite".
	_ "modernc.org/sqlite"

	sqlstore "github.com/kartaladev/hmntsk/store/sql"
)

// Where an order has got to. hmntsk stores none of this: the order is the
// host's record, and the workflow moves it along as its tasks complete.
const (
	orderPendingApproval       = "pending-approval"
	orderDeclined              = "declined"
	orderAwaitingPurchaseOrder = "awaiting-purchase-order"
	orderAwaitingInvoice       = "awaiting-invoice"
	orderInvoiceReview         = "invoice-review"
	orderInvoiceApproval       = "invoice-approval"
	orderDisputed              = "disputed"
	orderApproved              = "approved"
	orderRejected              = "rejected"
)

// order is a purchase, from its approval to its invoice's.
type order struct {
	ID          string `json:"id"`
	Supplier    string `json:"supplier"`
	Description string `json:"description"`
	Amount      int64  `json:"amount"`
	RequestedBy string `json:"requestedBy"`
	Status      string `json:"status"`
	// SupplierRegistered is decided when the order is placed: it chooses
	// whether the purchase order is sent or uploaded.
	SupplierRegistered bool `json:"supplierRegistered"`
	// InvoiceID is empty until the supplier's invoice arrives.
	InvoiceID string `json:"invoiceId,omitempty"`
	// InvoiceExpectedAt is when the supplier's invoice arrives, once the
	// purchase order has gone to them.
	InvoiceExpectedAt *time.Time `json:"invoiceExpectedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// invoice is the supplier's bill for an order.
type invoice struct {
	ID         string    `json:"id"`
	OrderID    string    `json:"orderId"`
	Supplier   string    `json:"supplier"`
	Amount     int64     `json:"amount"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// Ways a purchase order document reached the supplier.
const (
	documentSent     = "sent"
	documentUploaded = "uploaded"
)

// document is a purchase order, generated and sent or uploaded. Content is
// only ever read for a download.
type document struct {
	ID          string `json:"id"`
	OrderID     string `json:"orderId"`
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Method      string `json:"method"`
	// SentTo is the registry contact a sent purchase order went to.
	SentTo    string    `json:"sentTo,omitempty"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	Content   []byte    `json:"-"`
}

// errRecordNotFound reports an order, invoice or document that does not exist.
var errRecordNotFound = errors.New("record not found")

// store keeps the application's records, orders, invoices and purchase order
// documents, next to the engine's tables. Every method joins a transaction the
// host put on the context with sqlstore.ContextWithTx, exactly as the engine's
// store does, which is what lets a record and the tasks about it commit or
// roll back together.
type store struct {
	db *sql.DB
}

// openSQLite opens a SQLite database file with the settings docs/schema.md
// requires: foreign keys on, write-ahead logging, and a busy timeout so that
// concurrent writers wait instead of failing.
func openSQLite(path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	return db, nil
}

func (s *store) Migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS orders (
  number INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  supplier TEXT NOT NULL,
  supplier_registered INTEGER NOT NULL,
  description TEXT NOT NULL,
  amount INTEGER NOT NULL,
  requested_by TEXT NOT NULL,
  status TEXT NOT NULL,
  invoice_id TEXT UNIQUE,
  invoice_expected_at INTEGER,
  created_at INTEGER NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS invoices (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL UNIQUE REFERENCES orders (id),
  supplier TEXT NOT NULL,
  amount INTEGER NOT NULL,
  received_at INTEGER NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS documents (
  id TEXT PRIMARY KEY,
  order_id TEXT NOT NULL REFERENCES orders (id),
  file_name TEXT NOT NULL,
  content_type TEXT NOT NULL,
  size INTEGER NOT NULL,
  method TEXT NOT NULL,
  sent_to TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  content BLOB NOT NULL
)`,
	}

	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate purchasing records: %w", err)
		}
	}

	return nil
}

func (s *store) SaveOrder(ctx context.Context, number int, record order) error {
	_, err := s.execer(ctx).ExecContext(ctx,
		`INSERT INTO orders (number, id, supplier, supplier_registered, description, amount, requested_by, status,
  invoice_id, invoice_expected_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		number, record.ID, record.Supplier, record.SupplierRegistered, record.Description, record.Amount,
		record.RequestedBy, record.Status, nullString(record.InvoiceID), nullMicros(record.InvoiceExpectedAt),
		record.CreatedAt.UnixMicro())
	if err != nil {
		return fmt.Errorf("save order %s: %w", record.ID, err)
	}

	return nil
}

// AdvanceOrder moves an order from one status to the next and reports whether
// it moved. An order no longer in from is left alone, so a delivery repeated
// after the order has moved on changes nothing.
func (s *store) AdvanceOrder(ctx context.Context, orderID, from, to string) (bool, error) {
	return s.update(ctx, fmt.Sprintf("advance order %s to %s", orderID, to),
		`UPDATE orders SET status = ? WHERE id = ? AND status = ?`, to, orderID, from)
}

// AwaitInvoice records that the purchase order went to the supplier, whose
// invoice is expected at expectedAt. It moves only an order awaiting its
// purchase order.
func (s *store) AwaitInvoice(ctx context.Context, orderID string, expectedAt time.Time) (bool, error) {
	return s.update(ctx, "await invoice for "+orderID,
		`UPDATE orders SET status = ?, invoice_expected_at = ? WHERE id = ? AND status = ?`,
		orderAwaitingInvoice, expectedAt.UnixMicro(), orderID, orderAwaitingPurchaseOrder)
}

// ReceiveInvoice records the arrived invoice's ID on its order and moves the
// order to review. It moves only an order awaiting its invoice, so an invoice
// received twice is received once.
func (s *store) ReceiveInvoice(ctx context.Context, orderID, invoiceID string) (bool, error) {
	return s.update(ctx, "receive invoice for "+orderID,
		`UPDATE orders SET status = ?, invoice_id = ? WHERE id = ? AND status = ?`,
		orderInvoiceReview, invoiceID, orderID, orderAwaitingInvoice)
}

func (s *store) update(ctx context.Context, what, query string, args ...any) (bool, error) {
	result, err := s.execer(ctx).ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}

	return affected > 0, nil
}

const orderColumns = `id, supplier, supplier_registered, description, amount, requested_by, status, invoice_id,
  invoice_expected_at, created_at`

// Orders is every order, newest first.
func (s *store) Orders(ctx context.Context) ([]order, error) {
	return s.queryOrders(ctx, `SELECT `+orderColumns+` FROM orders ORDER BY number DESC`)
}

// InvoicesDue is every order whose invoice is expected by now, oldest first.
func (s *store) InvoicesDue(ctx context.Context, now time.Time) ([]order, error) {
	return s.queryOrders(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE status = ? AND invoice_expected_at <= ? ORDER BY number`,
		orderAwaitingInvoice, now.UnixMicro())
}

func (s *store) queryOrders(ctx context.Context, query string, args ...any) ([]order, error) {
	rows, err := s.execer(ctx).QueryContext(ctx, query, args...)
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

// Order is one order, or errRecordNotFound.
func (s *store) Order(ctx context.Context, id string) (order, error) {
	record, err := scanOrder(s.execer(ctx).QueryRowContext(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return order{}, fmt.Errorf("order %s: %w", id, errRecordNotFound)
	}

	return record, err
}

func scanOrder(row interface{ Scan(dest ...any) error }) (order, error) {
	var (
		record     order
		invoiceID  sql.NullString
		expectedAt sql.NullInt64
		createdAt  int64
	)

	err := row.Scan(&record.ID, &record.Supplier, &record.SupplierRegistered, &record.Description, &record.Amount,
		&record.RequestedBy, &record.Status, &invoiceID, &expectedAt, &createdAt)
	if err != nil {
		return order{}, fmt.Errorf("scan order: %w", err)
	}

	record.InvoiceID = invoiceID.String
	record.CreatedAt = time.UnixMicro(createdAt).UTC()

	if expectedAt.Valid {
		at := time.UnixMicro(expectedAt.Int64).UTC()
		record.InvoiceExpectedAt = &at
	}

	return record, nil
}

func (s *store) SaveInvoice(ctx context.Context, record invoice) error {
	_, err := s.execer(ctx).ExecContext(ctx,
		`INSERT INTO invoices (id, order_id, supplier, amount, received_at) VALUES (?, ?, ?, ?, ?)`,
		record.ID, record.OrderID, record.Supplier, record.Amount, record.ReceivedAt.UnixMicro())
	if err != nil {
		return fmt.Errorf("save invoice %s: %w", record.ID, err)
	}

	return nil
}

// InvoiceOf is an order's invoice, or errRecordNotFound before it arrives.
func (s *store) InvoiceOf(ctx context.Context, orderID string) (invoice, error) {
	var (
		record     invoice
		receivedAt int64
	)

	err := s.execer(ctx).QueryRowContext(ctx,
		`SELECT id, order_id, supplier, amount, received_at FROM invoices WHERE order_id = ?`, orderID).
		Scan(&record.ID, &record.OrderID, &record.Supplier, &record.Amount, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return invoice{}, fmt.Errorf("invoice of %s: %w", orderID, errRecordNotFound)
	}

	if err != nil {
		return invoice{}, fmt.Errorf("invoice of %s: %w", orderID, err)
	}

	record.ReceivedAt = time.UnixMicro(receivedAt).UTC()

	return record, nil
}

func (s *store) SaveDocument(ctx context.Context, record document) error {
	_, err := s.execer(ctx).ExecContext(ctx,
		`INSERT INTO documents (id, order_id, file_name, content_type, size, method, sent_to, created_by, created_at, content)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.OrderID, record.FileName, record.ContentType, len(record.Content), record.Method,
		record.SentTo, record.CreatedBy, record.CreatedAt.UnixMicro(), record.Content)
	if err != nil {
		return fmt.Errorf("save document %s: %w", record.ID, err)
	}

	return nil
}

const documentColumns = `id, order_id, file_name, content_type, size, method, sent_to, created_by, created_at`

// DocumentsOf is an order's documents, without their content, oldest first.
func (s *store) DocumentsOf(ctx context.Context, orderID string) ([]document, error) {
	rows, err := s.execer(ctx).QueryContext(ctx,
		`SELECT `+documentColumns+` FROM documents WHERE order_id = ? ORDER BY created_at, id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("documents of %s: %w", orderID, err)
	}
	defer rows.Close()

	documents := []document{}

	for rows.Next() {
		record, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}

		documents = append(documents, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("documents of %s: %w", orderID, err)
	}

	return documents, nil
}

// Document is one document with its content, or errRecordNotFound.
func (s *store) Document(ctx context.Context, id string) (document, error) {
	record, err := scanDocument(s.execer(ctx).QueryRowContext(ctx,
		`SELECT `+documentColumns+`, content FROM documents WHERE id = ?`, id), withContent())
	if errors.Is(err, sql.ErrNoRows) {
		return document{}, fmt.Errorf("document %s: %w", id, errRecordNotFound)
	}

	return record, err
}

type scanOption func(record *document) []any

func withContent() scanOption {
	return func(record *document) []any { return []any{&record.Content} }
}

func scanDocument(row interface{ Scan(dest ...any) error }, opts ...scanOption) (document, error) {
	var (
		record    document
		createdAt int64
	)

	dest := []any{
		&record.ID, &record.OrderID, &record.FileName, &record.ContentType, &record.Size, &record.Method,
		&record.SentTo, &record.CreatedBy, &createdAt,
	}

	for _, opt := range opts {
		dest = append(dest, opt(&record)...)
	}

	if err := row.Scan(dest...); err != nil {
		return document{}, fmt.Errorf("scan document: %w", err)
	}

	record.CreatedAt = time.UnixMicro(createdAt).UTC()

	return record, nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execer is the host's transaction when there is one, and the pool otherwise.
func (s *store) execer(ctx context.Context) execer {
	if tx, ok := sqlstore.TxFromContext(ctx); ok {
		return tx
	}

	return s.db
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullMicros(at *time.Time) sql.NullInt64 {
	if at == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: at.UnixMicro(), Valid: true}
}
