package main

import (
	"errors"
	"net/http"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
)

// recordTask is one step of an invoice's workflow as its page shows it.
type recordTask struct {
	ID     hmntsk.TaskID `json:"id"`
	Type   string        `json:"type"`
	Status hmntsk.Status `json:"status"`
	// Terminal is hmntsk's own verdict that nothing can move the task on, so
	// the page never keeps a list of statuses of its own.
	Terminal    bool      `json:"terminal"`
	Assignee    string    `json:"assignee,omitempty"`
	ActivityKey string    `json:"activityKey"`
	CreatedAt   time.Time `json:"createdAt"`
}

type invoiceView struct {
	ID       string `json:"id"`
	Supplier string `json:"supplier"`
	Amount   int64  `json:"amount"`
}

type invoiceRecord struct {
	Invoice invoiceView  `json:"invoice"`
	Order   *order       `json:"order"`
	Tasks   []recordTask `json:"tasks"`
}

// invoiceRecord is the invoice page's record: the invoice, the order it
// billed and every task on it, oldest first.
//
// The host reads the tasks through the engine, filtered on correlation, as
// record-page shows. That is the host's record page with its own authorization
// (here, anyone signed in), not an inbox query, so the task API keeps its
// self-only default.
func (s *server) invoiceRecord(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")

	invoice, err := s.invoices.Get(ctx, id)
	if errors.Is(err, invoicing.ErrNotFound) {
		http.Error(w, "no such invoice", http.StatusNotFound)

		return
	}

	if err != nil {
		http.Error(w, "read invoice", http.StatusInternalServerError)

		return
	}

	record := invoiceRecord{Invoice: invoiceView{ID: invoice.ID, Supplier: invoice.Supplier, Amount: invoice.Amount}}

	billed, found, err := s.orders.ByInvoice(ctx, id)
	if err != nil {
		http.Error(w, "read order", http.StatusInternalServerError)

		return
	}

	if found {
		record.Order = &billed
	}

	page, err := s.engine.Query(ctx, hmntsk.Query{
		OwnerType: invoicing.OwnerType,
		OwnerRef:  id,
		OrderBy:   hmntsk.OrderCreated,
		Limit:     hmntsk.MaxQueryLimit,
	})
	if err != nil {
		http.Error(w, "read tasks", http.StatusInternalServerError)

		return
	}

	record.Tasks = make([]recordTask, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		record.Tasks = append(record.Tasks, recordTask{
			ID:          task.ID,
			Type:        task.Type,
			Status:      task.Status,
			Terminal:    task.Status.IsTerminal(),
			Assignee:    task.Assignee,
			ActivityKey: task.Correlation.ActivityKey,
			CreatedAt:   task.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, record)
}
