// Package invoicing is the fictional business domain every scenario shares:
// invoices that need reviewing and approving, and the people who do it.
//
// It stands for your application. Nothing in it is part of hmntsk; it shows
// what a host brings: its records, its task types and its directory of people.
package invoicing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	// The pure-Go SQLite driver, registered as "sqlite".
	_ "modernc.org/sqlite"

	"github.com/kartaladev/hmntsk"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
)

// Task types and the activities they stand for. The activity is what goes into
// CorrelationData.ActivityKey.
const (
	ReviewType      = "invoice.review"
	ApproveType     = "invoice.approve"
	ActivityReview  = "review"
	ActivityApprove = "approve"
)

// OwnerType is what goes into CorrelationData.OwnerType for every invoice task.
const OwnerType = "invoice"

// Groups and people in the directory.
const (
	GroupApprovers = "finance-approvers"
	GroupManagers  = "finance-managers"
	GroupAuditors  = "auditors"
	Alice          = "alice" // approver
	Bob            = "bob"   // approver
	Carol          = "carol" // manager
	Dave           = "dave"  // auditor, takes part in no task
)

// Route is the link from an invoice task to the page where its work is done.
// It is stored as the hmntsk.route metadata of both types and expanded with
// hmntsk.ExpandRoute.
const Route = "/invoices/{correlation.ownerRef}/{correlation.activityKey}?task={task.id}"

// ReviewForm and ApproveForm name the forms a client renders, stored as the
// hmntsk.formKey metadata.
const (
	ReviewForm  = "invoice-review-form"
	ApproveForm = "invoice-approve-form"
)

// ReviewDeadline is how long a review has by default.
const ReviewDeadline = 48 * time.Hour

// ApproveDeadline is how long an approval has by default.
const ApproveDeadline = 24 * time.Hour

// ErrNotFound reports an invoice that does not exist.
var ErrNotFound = errors.New("invoicing: invoice not found")

// Invoice is the business record tasks are about.
type Invoice struct {
	ID       string
	Supplier string
	Amount   int64
}

var invoiceInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "invoiceId": {"type": "string"},
    "supplier": {"type": "string"},
    "amount": {"type": "integer", "minimum": 0}
  },
  "required": ["invoiceId", "supplier", "amount"]
}`)

// ReviewSpec is the review task type: checking an invoice against its order.
func ReviewSpec() hmntsk.TypeSpec {
	return hmntsk.TypeSpec{
		Name:        ReviewType,
		Title:       "Review invoice",
		Description: "Check the invoice against its purchase order.",
		InputSchema: invoiceInputSchema,
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "matchesOrder": {"type": "boolean"},
    "note": {"type": "string"}
  },
  "required": ["matchesOrder"]
}`),
		DefaultDeadline:   ReviewDeadline,
		DefaultAssignment: hmntsk.CandidatePool{Groups: []string{GroupApprovers}},
		Metadata: map[string]string{
			hmntsk.MetadataRoute:   Route,
			hmntsk.MetadataFormKey: ReviewForm,
		},
	}
}

// ApproveSpec is the approval task type. An overdue approval widens to the
// managers.
func ApproveSpec() hmntsk.TypeSpec {
	return hmntsk.TypeSpec{
		Name:        ApproveType,
		Title:       "Approve invoice",
		Description: "Approve or reject payment of the invoice.",
		InputSchema: invoiceInputSchema,
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "approved": {"type": "boolean"},
    "reason": {"type": "string", "enum": ["within-budget", "over-budget", "duplicate", "other"]},
    "note": {"type": "string"}
  },
  "required": ["approved", "reason"]
}`),
		DefaultPriority:   hmntsk.PriorityDefault,
		DefaultDeadline:   ApproveDeadline,
		DefaultAssignment: hmntsk.CandidatePool{Groups: []string{GroupApprovers}},
		DefaultEscalation: &hmntsk.EscalationPolicy{
			Action:    hmntsk.EscalationWiden,
			AddGroups: []string{GroupManagers},
		},
		Metadata: map[string]string{
			hmntsk.MetadataRoute:   Route,
			hmntsk.MetadataFormKey: ApproveForm,
		},
	}
}

// Register registers both invoice task types.
func Register(svc *hmntsk.Service) error {
	for _, spec := range []hmntsk.TypeSpec{ReviewSpec(), ApproveSpec()} {
		if err := svc.Register(spec); err != nil {
			return fmt.Errorf("register %s: %w", spec.Name, err)
		}
	}

	return nil
}

// Directory is who belongs to which group. A real host implements
// hmntsk.GroupResolver over the directory it already runs.
func Directory() *hmntsk.StaticAssignment {
	return hmntsk.NewStaticAssignment(map[string][]string{
		GroupApprovers: {Alice, Bob},
		GroupManagers:  {Carol},
		GroupAuditors:  {Dave},
	})
}

// Correlation ties a task to one activity on one invoice.
func Correlation(invoiceID, activity string) hmntsk.CorrelationData {
	return hmntsk.CorrelationData{OwnerType: OwnerType, OwnerRef: invoiceID, ActivityKey: activity}
}

// Input is the task input for an invoice.
func Input(invoice Invoice) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"invoiceId": invoice.ID,
		"supplier":  invoice.Supplier,
		"amount":    invoice.Amount,
	})

	return raw
}

// Repository stores invoices.
type Repository interface {
	Save(ctx context.Context, invoice Invoice) error
	Get(ctx context.Context, id string) (Invoice, error)
}

// MemoryRepository keeps invoices in memory.
type MemoryRepository struct {
	mu       sync.Mutex
	invoices map[string]Invoice
}

// NewMemoryRepository returns an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{invoices: make(map[string]Invoice)}
}

// Save stores an invoice, replacing one with the same ID.
func (r *MemoryRepository) Save(_ context.Context, invoice Invoice) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.invoices[invoice.ID] = invoice

	return nil
}

// Get returns an invoice, or ErrNotFound.
func (r *MemoryRepository) Get(_ context.Context, id string) (Invoice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	invoice, ok := r.invoices[id]
	if !ok {
		return Invoice{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	return invoice, nil
}

// SQLRepository keeps invoices in the same database as the engine's tables.
//
// It joins a transaction the host put on the context with
// sqlstore.ContextWithTx, exactly as the engine's store does. That is what lets
// an invoice and the tasks about it commit or roll back together.
type SQLRepository struct {
	db *sql.DB
}

// NewSQLRepository returns a repository over db.
func NewSQLRepository(db *sql.DB) *SQLRepository {
	return &SQLRepository{db: db}
}

// Migrate creates the invoices table.
func (r *SQLRepository) Migrate(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS invoices (
  id TEXT PRIMARY KEY,
  supplier TEXT NOT NULL,
  amount INTEGER NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("create invoices table: %w", err)
	}

	return nil
}

// Save stores an invoice, replacing one with the same ID.
func (r *SQLRepository) Save(ctx context.Context, invoice Invoice) error {
	_, err := r.execer(ctx).ExecContext(ctx,
		`INSERT INTO invoices (id, supplier, amount) VALUES (?, ?, ?)
ON CONFLICT (id) DO UPDATE SET supplier = excluded.supplier, amount = excluded.amount`,
		invoice.ID, invoice.Supplier, invoice.Amount)
	if err != nil {
		return fmt.Errorf("save invoice %s: %w", invoice.ID, err)
	}

	return nil
}

// Get returns an invoice, or ErrNotFound.
func (r *SQLRepository) Get(ctx context.Context, id string) (Invoice, error) {
	var invoice Invoice

	err := r.execer(ctx).QueryRowContext(ctx,
		`SELECT id, supplier, amount FROM invoices WHERE id = ?`, id).
		Scan(&invoice.ID, &invoice.Supplier, &invoice.Amount)
	if errors.Is(err, sql.ErrNoRows) {
		return Invoice{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	if err != nil {
		return Invoice{}, fmt.Errorf("get invoice %s: %w", id, err)
	}

	return invoice, nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execer is the host's transaction when there is one, and the pool otherwise.
func (r *SQLRepository) execer(ctx context.Context) execer {
	if tx, ok := sqlstore.TxFromContext(ctx); ok {
		return tx
	}

	return r.db
}

// OpenSQLite opens a SQLite database file with the settings docs/schema.md
// requires: foreign keys on, write-ahead logging, and a busy timeout so that
// concurrent writers wait instead of failing.
func OpenSQLite(path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	return db, nil
}
