package transportcore

import (
	"encoding/json"
	"time"

	"github.com/kartaladev/hmntsk"
)

// CreateTaskRequest is the body of a create.
//
// Payloads are json.RawMessage throughout. They are never decoded into a map
// and re-encoded on the way past: that would sort the keys, round a 64-bit
// amount through a float64 and drop fields the schema does not describe, all
// of which the contract promises not to do.
type CreateTaskRequest struct {
	// Type names the registered task type. It is required.
	Type string `json:"type"`
	// ID lets a caller supply its own identifier, for an idempotent create.
	ID string `json:"id,omitempty"`
	// Input is the task's payload.
	Input json.RawMessage `json:"input,omitempty"`
	// Priority overrides the type's default priority.
	Priority *int `json:"priority,omitempty"`
	// DueAt overrides every deadline default with an explicit instant.
	DueAt *time.Time `json:"dueAt,omitempty"`
	// DeadlineSeconds overrides the type's default deadline with an interval
	// measured from creation. DueAt wins over it.
	DeadlineSeconds *int64 `json:"deadlineSeconds,omitempty"`
	// Candidates overrides the type's default assignment.
	Candidates *hmntsk.CandidatePool `json:"candidates,omitempty"`
	// Escalation overrides the type's default escalation policy.
	Escalation *hmntsk.EscalationPolicy `json:"escalation,omitempty"`
	// Correlation ties the task back to the work that asked for it.
	Correlation hmntsk.CorrelationData `json:"correlation,omitzero"`
	// Callback is where the host wants notifications delivered.
	Callback *hmntsk.CallbackTarget `json:"callback,omitempty"`
}

// OperationRequest is the body every lifecycle operation accepts. Each
// operation uses the fields that mean something to it and ignores the rest.
type OperationRequest struct {
	// Version is the version the caller last observed. When set, the operation
	// is refused with 409 if the task has moved on.
	Version *int64 `json:"version,omitempty"`
	// Comment is free text recorded in history: a failure reason, a note on a
	// delegation, why something was cancelled.
	Comment string `json:"comment,omitempty"`
	// Output is the completion payload, used by complete.
	Output json.RawMessage `json:"output,omitempty"`
	// Delegate is the actor to hand the task to, used by delegate.
	Delegate string `json:"delegate,omitempty"`
	// Patch is an RFC 6902 JSON Patch, used by save-progress.
	Patch json.RawMessage `json:"patch,omitempty"`
}

// TaskResponse is a task as the API returns it.
//
// It is [hmntsk.Task] verbatim rather than a projection of it. A projection
// would be a second place to remember when a field is added, and the aggregate
// is already the published contract.
type TaskResponse = hmntsk.Task

// PageResponse is a page of tasks.
type PageResponse struct {
	// Tasks are the matching tasks, in query order.
	Tasks []hmntsk.Task `json:"tasks"`
	// NextCursor continues the query, empty on the last page.
	NextCursor string `json:"nextCursor,omitempty"`
}

// CountResponse is how many tasks a query matches.
type CountResponse struct {
	// Count is the number of matching tasks, each counted once.
	Count int64 `json:"count"`
}

// HistoryResponse is a task's transition log.
type HistoryResponse struct {
	// Records are the transitions, oldest first.
	Records []hmntsk.TransitionRecord `json:"records"`
}

// TaskTypeResponse describes a registered task type, so that a client can
// render a form for a kind of work it has never seen.
type TaskTypeResponse struct {
	// Name is the type identifier.
	Name string `json:"name"`
	// Title is a human-readable label.
	Title string `json:"title,omitempty"`
	// Description explains the work.
	Description string `json:"description,omitempty"`
	// InputSchema is the JSON Schema for the task's input, as registered.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// OutputSchema is the JSON Schema an output must satisfy to complete the
	// task, as registered.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// DefaultPriority is the priority a task of this type carries by default.
	DefaultPriority int `json:"defaultPriority"`
	// DefaultDeadlineSeconds is the deadline interval, zero when there is none.
	DefaultDeadlineSeconds int64 `json:"defaultDeadlineSeconds,omitempty"`
	// Metadata is the type's metadata, exactly as registered, such as the
	// well-known hmntsk.formKey and hmntsk.route keys a client links a task to
	// its business form with.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// TaskTypeListResponse is every registered type.
type TaskTypeListResponse struct {
	// Types are the registered types, by name.
	Types []TaskTypeResponse `json:"types"`
}

// ErrorResponse is the body of every failure.
type ErrorResponse struct {
	// Error carries the detail.
	Error ErrorDetail `json:"error"`
}

// ErrorDetail says what went wrong, in terms a client can act on.
type ErrorDetail struct {
	// Code is a stable, machine-readable classification.
	Code ErrorCode `json:"code"`
	// Message is a human-readable explanation.
	Message string `json:"message"`
	// CurrentVersion is the task's current version, present on a conflict so
	// that a client can re-read, merge and retry without a second request.
	CurrentVersion *int64 `json:"currentVersion,omitempty"`
	// TaskType names the offending type when a request used one that is not
	// registered.
	TaskType string `json:"taskType,omitempty"`
	// Issues lists every problem with a payload or a request, so that a whole
	// form's worth of errors can be shown at once.
	Issues []hmntsk.ValidationIssue `json:"issues,omitempty"`
}

// ErrorCode classifies a failure.
type ErrorCode string

// The error codes the contract publishes.
const (
	// CodeConflict is a concurrent modification or an illegal transition.
	CodeConflict ErrorCode = "conflict"
	// CodeForbidden is a failed eligibility or assignee check.
	CodeForbidden ErrorCode = "forbidden"
	// CodeNotFound is an unknown task or route.
	CodeNotFound ErrorCode = "not_found"
	// CodeValidation is a schema or request validation failure.
	CodeValidation ErrorCode = "validation_failed"
	// CodeUnregisteredType is a task type that was never registered.
	CodeUnregisteredType ErrorCode = "unregistered_type"
	// CodeInternal is anything the contract did not anticipate.
	CodeInternal ErrorCode = "internal"
)

// taskTypeResponse renders a registered type for the wire.
func taskTypeResponse(spec hmntsk.TypeSpec) TaskTypeResponse {
	return TaskTypeResponse{
		Name:                   spec.Name,
		Title:                  spec.Title,
		Description:            spec.Description,
		InputSchema:            spec.InputSchema,
		OutputSchema:           spec.OutputSchema,
		DefaultPriority:        int(spec.DefaultPriority),
		DefaultDeadlineSeconds: int64(spec.DefaultDeadline / time.Second),
		Metadata:               spec.Metadata,
	}
}
