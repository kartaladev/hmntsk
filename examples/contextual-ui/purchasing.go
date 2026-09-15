package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// The purchasing domain this demo declares for itself. Nothing in it is part of
// hmntsk: it is what a host brings, its records, its task types and its
// directory of people.

// OwnerType is what goes into CorrelationData.OwnerType for every task: every
// task in the demo is about one order, from its approval to its invoice's.
const ownerType = "order"

// Task types. Each is one step of an order's workflow.
const (
	approveOrderType   = "purchase.approve-order"
	sendOrderType      = "purchase.send-order"
	uploadOrderType    = "purchase.upload-order"
	reviewInvoiceType  = "purchase.review-invoice"
	approveInvoiceType = "purchase.approve-invoice"
)

// Activities, which go into CorrelationData.ActivityKey. Sending and uploading
// are the same activity, issuing the purchase order, done two ways.
const (
	activityApproveOrder   = "approve-order"
	activityPurchaseOrder  = "purchase-order"
	activityReviewInvoice  = "review-invoice"
	activityApproveInvoice = "approve-invoice"
)

// Forms a client renders, stored as the hmntsk.formKey metadata. The purchase
// order forms are the application's own, not rendered from a schema: sending
// and uploading are done through the application's API, which completes the
// task in the same transaction that stores the document.
const (
	approveOrderForm   = "approve-order-form"
	sendOrderForm      = "send-purchase-order-form"
	uploadOrderForm    = "upload-purchase-order-form"
	reviewInvoiceForm  = "review-invoice-form"
	approveInvoiceForm = "approve-invoice-form"
)

// orderRoute is the link from every task to the order page, where its work is
// done. It is stored as the hmntsk.route metadata of every type.
const orderRoute = "/orders/{correlation.ownerRef}/{correlation.activityKey}?task={task.id}"

// systemActor creates every task the workflow asks for.
const systemActor = "acme-purchasing"

// Groups and people.
const (
	groupPurchasing    = "purchasing"
	groupBudgetHolders = "budget-holders"
	groupApprovers     = "finance-approvers"
	groupManagers      = "finance-managers"
	groupAuditors      = "auditors"

	alice = "alice" // approves invoices
	bob   = "bob"   // approves invoices
	carol = "carol" // holds the budget: approves orders, and overdue invoices
	dave  = "dave"  // audits, takes part in no task
	erin  = "erin"  // purchasing: places orders and issues purchase orders
)

// directory is who belongs to which group. A real host implements
// hmntsk.GroupResolver over the directory it already runs.
func directory() *hmntsk.StaticAssignment {
	return hmntsk.NewStaticAssignment(map[string][]string{
		groupPurchasing:    {erin},
		groupBudgetHolders: {carol},
		groupApprovers:     {alice, bob},
		groupManagers:      {carol},
		groupAuditors:      {dave},
	})
}

// correlation ties a task to one activity on one order.
func correlation(orderID, activity string) hmntsk.CorrelationData {
	return hmntsk.CorrelationData{OwnerType: ownerType, OwnerRef: orderID, ActivityKey: activity}
}

// activityOf is the activity a task type stands for.
func activityOf(taskType string) string {
	switch taskType {
	case approveOrderType:
		return activityApproveOrder
	case sendOrderType, uploadOrderType:
		return activityPurchaseOrder
	case reviewInvoiceType:
		return activityReviewInvoice
	default:
		return activityApproveInvoice
	}
}

// taskTypes are every type the demo registers.
func taskTypes() []hmntsk.TypeSpec {
	metadata := func(form string) map[string]string {
		return map[string]string{hmntsk.MetadataRoute: orderRoute, hmntsk.MetadataFormKey: form}
	}

	return []hmntsk.TypeSpec{
		{
			Name:        approveOrderType,
			Title:       "Approve order",
			Description: "Decide whether the purchase goes ahead before anything is ordered from the supplier.",
			InputSchema: orderInputSchema,
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "approved": {"type": "boolean"},
    "note": {"type": "string"}
  },
  "required": ["approved"]
}`),
			DefaultDeadline:   24 * time.Hour,
			DefaultAssignment: hmntsk.CandidatePool{Groups: []string{groupBudgetHolders}},
			Metadata:          metadata(approveOrderForm),
		},
		{
			Name:        sendOrderType,
			Title:       "Send purchase order",
			Description: "The supplier is in the supplier registry: send them the purchase order the application drafts.",
			InputSchema: orderInputSchema,
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "documentId": {"type": "string", "minLength": 1},
    "sentTo": {"type": "string", "minLength": 1}
  },
  "required": ["documentId", "sentTo"]
}`),
			DefaultDeadline:   8 * time.Hour,
			DefaultAssignment: hmntsk.CandidatePool{Groups: []string{groupPurchasing}},
			Metadata:          metadata(sendOrderForm),
		},
		{
			Name:        uploadOrderType,
			Title:       "Upload purchase order",
			Description: "The supplier is not in the supplier registry: agree the order with them and upload the signed purchase order.",
			InputSchema: orderInputSchema,
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "documentId": {"type": "string", "minLength": 1},
    "fileName": {"type": "string"}
  },
  "required": ["documentId"]
}`),
			DefaultDeadline:   48 * time.Hour,
			DefaultAssignment: hmntsk.CandidatePool{Groups: []string{groupPurchasing}},
			Metadata:          metadata(uploadOrderForm),
		},
		{
			Name:        reviewInvoiceType,
			Title:       "Review invoice",
			Description: "Check the supplier's invoice against its purchase order.",
			InputSchema: orderInputSchema,
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "matchesOrder": {"type": "boolean"},
    "note": {"type": "string"}
  },
  "required": ["matchesOrder"]
}`),
			DefaultDeadline:   48 * time.Hour,
			DefaultAssignment: hmntsk.CandidatePool{Groups: []string{groupApprovers}},
			Metadata:          metadata(reviewInvoiceForm),
		},
		{
			Name:        approveInvoiceType,
			Title:       "Approve invoice",
			Description: "Approve or reject payment of the invoice.",
			InputSchema: orderInputSchema,
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "approved": {"type": "boolean"},
    "reason": {"type": "string", "enum": ["within-budget", "over-budget", "duplicate", "other"]},
    "note": {"type": "string"}
  },
  "required": ["approved", "reason"]
}`),
			DefaultDeadline:   24 * time.Hour,
			DefaultAssignment: hmntsk.CandidatePool{Groups: []string{groupApprovers}},
			DefaultEscalation: &hmntsk.EscalationPolicy{
				Action:    hmntsk.EscalationWiden,
				AddGroups: []string{groupManagers},
			},
			Metadata: metadata(approveInvoiceForm),
		},
	}
}

// registerTypes registers every task type on the engine.
func registerTypes(engine *hmntsk.Service) error {
	for _, spec := range taskTypes() {
		if err := engine.Register(spec); err != nil {
			return fmt.Errorf("register %s: %w", spec.Name, err)
		}
	}

	return nil
}

// orderInputSchema is every task's input: the order it is about. An invoice
// task also names the invoice.
var orderInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "orderId": {"type": "string"},
    "supplier": {"type": "string"},
    "description": {"type": "string"},
    "amount": {"type": "integer", "minimum": 0},
    "invoiceId": {"type": "string"}
  },
  "required": ["orderId", "supplier", "amount"]
}`)

// taskInput is a task's input for an order.
func taskInput(record order) json.RawMessage {
	input := map[string]any{
		"orderId":     record.ID,
		"supplier":    record.Supplier,
		"description": record.Description,
		"amount":      record.Amount,
	}

	if record.InvoiceID != "" {
		input["invoiceId"] = record.InvoiceID
	}

	raw, _ := json.Marshal(input)

	return raw
}

// supplier is a supplier on the registry: the application can send them a
// purchase order itself.
type supplier struct {
	Name string `json:"name"`
	// Contact is where their purchase orders are sent.
	Contact string `json:"contact"`
}

// supplierRegistry is the suppliers the application trades with directly. An
// order with anyone else needs a purchase order agreed outside the application
// and uploaded to it.
var supplierRegistry = []supplier{
	{Name: "Acme Paper", Contact: "orders@acme-paper.example"},
	{Name: "Globex Cloud", Contact: "procurement@globex.example"},
	{Name: "Hooli Travel", Contact: "corporate@hooli-travel.example"},
	{Name: "Initech Chairs", Contact: "sales@initech.example"},
	{Name: "Umbrella Catering", Contact: "events@umbrella-catering.example"},
}

// registeredSupplier is the registry's entry for a supplier name, matched
// regardless of case and surrounding spaces.
func registeredSupplier(name string) (supplier, bool) {
	name = strings.TrimSpace(name)

	for _, entry := range supplierRegistry {
		if strings.EqualFold(entry.Name, name) {
			return entry, true
		}
	}

	return supplier{}, false
}
