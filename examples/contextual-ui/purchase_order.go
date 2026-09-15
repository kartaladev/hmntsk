package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// maxUploadBytes bounds an uploaded purchase order.
const maxUploadBytes = 5 << 20

// uploadTypes are what a purchase order may be uploaded as, by content rather
// than by the name or type the browser claims. Anything a browser would render
// as a page, such as HTML, is refused.
var uploadTypes = []string{"application/pdf", "image/png", "image/jpeg"}

// issued is what issuing a purchase order answers: the document, and the task
// it completed.
type issued struct {
	Document document    `json:"document"`
	Task     hmntsk.Task `json:"task"`
}

// sendPurchaseOrder is purchasing sending a registered supplier the purchase
// order the application drafts. The document is stored, and the purchase order
// task completed, in one transaction: the application's own form does the
// work, and the task records that it was done.
func (s *server) sendPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	var req struct {
		TaskID  hmntsk.TaskID `json:"taskId"`
		Version int64         `json:"version"`
	}

	if err := decodeJSON(w, r, &req); err != nil || req.TaskID == "" {
		http.Error(w, "the body must be JSON naming the task and its version", http.StatusBadRequest)

		return
	}

	placed, ok := s.purchaseOrderOf(w, r, req.TaskID, sendOrderType)
	if !ok {
		return
	}

	registry, ok := registeredSupplier(placed.Supplier)
	if !ok {
		http.Error(w, "the supplier is no longer on the registry: upload the purchase order", http.StatusConflict)

		return
	}

	doc := sentPurchaseOrder(placed, registry, s.engine.Clock().Now().UTC())
	doc.CreatedBy = user.ID

	s.issue(w, r, user, req.TaskID, req.Version, doc, map[string]any{"documentId": doc.ID, "sentTo": doc.SentTo})
}

// uploadPurchaseOrder is purchasing uploading the purchase order agreed with a
// supplier who is not on the registry, as a multipart form with the task's ID
// and version and the file.
func (s *server) uploadPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+64<<10)

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "a purchase order may be at most 5 MB", http.StatusRequestEntityTooLarge)

			return
		}

		http.Error(w, "the body must be a multipart form", http.StatusBadRequest)

		return
	}

	taskID := hmntsk.TaskID(r.FormValue("taskId"))

	version, err := strconv.ParseInt(r.FormValue("version"), 10, 64)
	if taskID == "" || err != nil {
		http.Error(w, "the form must name the task and its version", http.StatusBadRequest)

		return
	}

	placed, ok := s.purchaseOrderOf(w, r, taskID, uploadOrderType)
	if !ok {
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "the form must carry the purchase order as file", http.StatusBadRequest)

		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil || len(content) == 0 || len(content) > maxUploadBytes {
		http.Error(w, "the purchase order must be a file of at most 5 MB", http.StatusBadRequest)

		return
	}

	contentType := http.DetectContentType(content)
	if !slices.Contains(uploadTypes, contentType) {
		http.Error(w, "a purchase order must be a PDF, PNG or JPEG", http.StatusUnsupportedMediaType)

		return
	}

	doc := document{
		ID:          "PO-" + orderNumber(placed.ID),
		OrderID:     placed.ID,
		FileName:    uploadedName(header.Filename),
		ContentType: contentType,
		Method:      documentUploaded,
		CreatedBy:   user.ID,
		CreatedAt:   s.engine.Clock().Now().UTC(),
		Content:     content,
	}

	s.issue(w, r, user, taskID, version, doc, map[string]any{"documentId": doc.ID, "fileName": doc.FileName})
}

// purchaseOrderOf is the order a purchase order task belongs to, checked to be
// issued the way taskType is. On any other answer it writes the response and
// reports false.
func (s *server) purchaseOrderOf(w http.ResponseWriter, r *http.Request, taskID hmntsk.TaskID, taskType string) (order, bool) {
	ctx := r.Context()

	placed, err := s.records.Order(ctx, r.PathValue("id"))
	if errors.Is(err, errRecordNotFound) {
		http.Error(w, "no such order", http.StatusNotFound)

		return order{}, false
	}

	if err != nil {
		http.Error(w, "read order", http.StatusInternalServerError)

		return order{}, false
	}

	task, err := s.engine.Get(ctx, taskID)
	if err != nil {
		writeTaskError(w, err)

		return order{}, false
	}

	if task.Correlation.OwnerType != ownerType || task.Correlation.OwnerRef != placed.ID ||
		task.Correlation.ActivityKey != activityPurchaseOrder {
		http.Error(w, "the task is not this order's purchase order", http.StatusBadRequest)

		return order{}, false
	}

	if task.Type != taskType {
		how := map[string]string{sendOrderType: "sent", uploadOrderType: "uploaded"}[task.Type]
		http.Error(w, "this order's purchase order is "+how, http.StatusBadRequest)

		return order{}, false
	}

	return placed, true
}

// issue completes the purchase order task as user and stores its document, in
// one transaction, and dispatches the completion after it commits. The engine
// decides whether user may complete the task, and whether version is current.
func (s *server) issue(
	w http.ResponseWriter, r *http.Request, user demoUser, taskID hmntsk.TaskID, version int64,
	doc document, output map[string]any,
) {
	raw, err := json.Marshal(output)
	if err != nil {
		http.Error(w, "encode output", http.StatusInternalServerError)

		return
	}

	var completed hmntsk.Result

	err = s.transact(r.Context(), func(ctx context.Context) ([]hmntsk.Result, error) {
		var err error

		completed, err = s.engine.Complete(ctx, hmntsk.CompleteRequest{
			TaskRequest: hmntsk.TaskRequest{TaskID: taskID, Actor: user.ID, Version: &version},
			Output:      raw,
		})
		if err != nil {
			return nil, err
		}

		if err := s.records.SaveDocument(ctx, doc); err != nil {
			return nil, err
		}

		return []hmntsk.Result{completed}, nil
	})
	if err != nil {
		writeTaskError(w, err)

		return
	}

	doc.Size = int64(len(doc.Content))

	writeJSON(w, http.StatusOK, issued{Document: doc, Task: completed.Task})
}

// writeTaskError answers an engine error with the status the task contract
// uses for it.
func writeTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hmntsk.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, hmntsk.ErrUnauthorized):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, hmntsk.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, hmntsk.ErrValidation):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, "issue purchase order", http.StatusInternalServerError)
	}
}

// uploadedName is an uploaded file's own name, without any directories the
// browser sent with it.
func uploadedName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		return "purchase-order"
	}

	return name
}

// downloadDocument serves a purchase order document to anyone signed in, as an
// attachment that the browser never renders in the page's origin.
func (s *server) downloadDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}

	doc, err := s.records.Document(r.Context(), r.PathValue("id"))
	if errors.Is(err, errRecordNotFound) {
		http.Error(w, "no such document", http.StatusNotFound)

		return
	}

	if err != nil {
		http.Error(w, "read document", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", doc.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": doc.FileName}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Length", strconv.Itoa(len(doc.Content)))
	_, _ = w.Write(doc.Content)
}

// sentPurchaseOrder is the purchase order the application drafts for a
// supplier on the registry, and sends to their registry contact.
func sentPurchaseOrder(record order, registry supplier, now time.Time) document {
	number := orderNumber(record.ID)

	var content strings.Builder

	fmt.Fprintf(&content, "PURCHASE ORDER PO-%s\n\n", number)
	fmt.Fprintf(&content, "Acme Purchasing\nOrder:        %s\nDate:         %s\n\n", record.ID, now.Format(time.DateOnly))
	fmt.Fprintf(&content, "To:           %s <%s>\n", record.Supplier, registry.Contact)
	fmt.Fprintf(&content, "Item:         %s\nAmount:       %d USD\n", record.Description, record.Amount)
	fmt.Fprintf(&content, "Requested by: %s\n\nPlease invoice quoting %s.\n", record.RequestedBy, record.ID)

	return document{
		ID:          "PO-" + number,
		OrderID:     record.ID,
		FileName:    "PO-" + number + ".txt",
		ContentType: "text/plain; charset=utf-8",
		Method:      documentSent,
		SentTo:      registry.Contact,
		CreatedBy:   record.RequestedBy,
		CreatedAt:   now,
		Content:     []byte(content.String()),
	}
}
