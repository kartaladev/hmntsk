package hmntsk

import (
	"encoding/json"
	"time"
)

// CreateRequest describes the task a caller wants. Every field that a
// [TypeSpec] also carries a default for is a pointer, so that "not supplied"
// is distinguishable from a deliberate zero: a caller asking for
// [PriorityHighest] means it, and would otherwise be silently given the type's
// default instead.
type CreateRequest struct {
	// Type names the registered task type. It is required.
	Type string
	// Actor is who is creating the task. It is recorded as the creator and on
	// the creation event.
	Actor string
	// ID lets a caller supply its own identifier, for an idempotent create.
	// Empty means the engine mints one through its [IDGenerator].
	ID TaskID
	// Input is the task's payload, validated in full against the type's input
	// schema.
	Input json.RawMessage
	// Priority overrides the type's default priority.
	Priority *Priority
	// DueAt overrides every deadline default with an explicit instant.
	DueAt *time.Time
	// Deadline overrides the type's default deadline with an interval measured
	// from creation. DueAt wins over it.
	Deadline *time.Duration
	// Candidates overrides the type's default assignment.
	Candidates *CandidatePool
	// Escalation overrides the type's default escalation policy.
	Escalation *EscalationPolicy
	// Correlation ties the task back to the work that asked for it.
	Correlation CorrelationData
	// Callback is where the host wants notifications about this task
	// delivered. It is optional.
	Callback *CallbackTarget
}

// NewTask builds the CREATED task a request asks for, applying the type's
// defaults wherever the request supplied nothing. It does not validate the
// payload and does not resolve assignment; the [Service] does both around it.
//
// Precedence, from strongest: an explicit due date, an explicit deadline
// interval, the type's default deadline interval, no deadline at all.
func (s TypeSpec) NewTask(id TaskID, req CreateRequest, now time.Time) Task {
	at := NormalizeTime(now)

	task := Task{
		ID:          id,
		Type:        s.Name,
		Version:     0,
		Status:      StatusCreated,
		Priority:    s.DefaultPriority,
		Candidates:  s.DefaultAssignment.Clone(),
		Correlation: req.Correlation.Clone(),
		Escalation:  s.DefaultEscalation.Clone(),
		Input:       cloneRaw(req.Input),
		CreatedBy:   req.Actor,
		CreatedAt:   at,
		UpdatedAt:   at,
	}

	if req.Priority != nil {
		task.Priority = *req.Priority
	}

	if req.Candidates != nil {
		task.Candidates = req.Candidates.Clone()
	}

	if req.Escalation != nil {
		task.Escalation = req.Escalation.Clone()
	}

	if req.Callback != nil {
		callback := req.Callback.Clone()
		task.Callback = &callback
	}

	task.DueAt = resolveDueAt(s, req, at)

	return task
}

// resolveDueAt applies the deadline precedence rules.
func resolveDueAt(spec TypeSpec, req CreateRequest, createdAt time.Time) *time.Time {
	switch {
	case req.DueAt != nil:
		return normalizeTimePtr(req.DueAt)
	case req.Deadline != nil && *req.Deadline > 0:
		return timePtr(createdAt.Add(*req.Deadline))
	case spec.DefaultDeadline > 0:
		return timePtr(createdAt.Add(spec.DefaultDeadline))
	default:
		return nil
	}
}
