package main

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/kartaladev/hmntsk"
)

// recordTask is one step of an order's workflow as its page shows it.
type recordTask struct {
	ID     hmntsk.TaskID `json:"id"`
	Type   string        `json:"type"`
	Status hmntsk.Status `json:"status"`
	// Terminal is hmntsk's own verdict that nothing can move the task on, so
	// the page never keeps a list of statuses of its own.
	Terminal    bool       `json:"terminal"`
	Assignee    string     `json:"assignee,omitempty"`
	ActivityKey string     `json:"activityKey"`
	Priority    int        `json:"priority"`
	DueAt       *time.Time `json:"dueAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type orderRecord struct {
	Order order `json:"order"`
	// Invoice is null until the supplier's invoice arrives.
	Invoice   *invoice     `json:"invoice"`
	Documents []document   `json:"documents"`
	Tasks     []recordTask `json:"tasks"`
}

// orderRecord is the order page's record: the order, its purchase order
// documents, its invoice once it has arrived, and every task on it, oldest
// first.
//
// The host reads the tasks through the engine, filtered on correlation, as
// record-page shows. That is the host's record page with its own authorization
// (here, anyone signed in), not an inbox query, so the task API keeps its
// self-only default.
func (s *server) orderRecord(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}

	ctx := r.Context()
	id := r.PathValue("id")

	placed, err := s.records.Order(ctx, id)
	if errors.Is(err, errRecordNotFound) {
		http.Error(w, "no such order", http.StatusNotFound)

		return
	}

	if err != nil {
		http.Error(w, "read order", http.StatusInternalServerError)

		return
	}

	record := orderRecord{Order: placed}

	billed, err := s.records.InvoiceOf(ctx, id)
	switch {
	case err == nil:
		record.Invoice = &billed
	case !errors.Is(err, errRecordNotFound):
		http.Error(w, "read invoice", http.StatusInternalServerError)

		return
	}

	if record.Documents, err = s.records.DocumentsOf(ctx, id); err != nil {
		http.Error(w, "read documents", http.StatusInternalServerError)

		return
	}

	page, err := s.engine.Query(ctx, hmntsk.Query{
		OwnerType: ownerType,
		OwnerRef:  id,
		OrderBy:   hmntsk.OrderCreated,
		Limit:     hmntsk.MaxQueryLimit,
	})
	if err != nil {
		http.Error(w, "read tasks", http.StatusInternalServerError)

		return
	}

	// The workflow's tasks have IDs derived from their order, which do not sort
	// the way they were created, so the record puts them in creation order
	// itself.
	slices.SortStableFunc(page.Tasks, func(a, b hmntsk.Task) int { return a.CreatedAt.Compare(b.CreatedAt) })

	record.Tasks = make([]recordTask, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		record.Tasks = append(record.Tasks, recordTask{
			ID:          task.ID,
			Type:        task.Type,
			Status:      task.Status,
			Terminal:    task.Status.IsTerminal(),
			Assignee:    task.Assignee,
			ActivityKey: task.Correlation.ActivityKey,
			Priority:    int(task.Priority),
			DueAt:       task.DueAt,
			CreatedAt:   task.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, record)
}
