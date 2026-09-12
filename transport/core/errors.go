package transportcore

import (
	"errors"

	"github.com/kartaladev/hmntsk"
)

// HTTP status codes the contract uses. They are named so that the mapping below
// reads as a decision rather than as a table of magic numbers.
const (
	// StatusOK answers a successful read or lifecycle operation.
	StatusOK = 200
	// StatusCreated answers a successful create.
	StatusCreated = 201
	// StatusBadRequest answers a schema or request validation failure, and an
	// unregistered task type.
	StatusBadRequest = 400
	// StatusForbidden answers a failed eligibility or assignee check.
	StatusForbidden = 403
	// StatusNotFound answers an unknown task or route.
	StatusNotFound = 404
	// StatusConflict answers a concurrent-modification conflict and an illegal
	// transition alike.
	StatusConflict = 409
	// StatusInternalServerError answers anything the contract did not
	// anticipate, including a directory that could not be reached.
	StatusInternalServerError = 500
)

// StatusFor maps an engine error to a status code.
//
// The order matters. An unregistered type matches the validation sentinel as
// well as its own, and an illegal transition matches the conflict sentinel as
// well as its own, so the narrower test has to come first in each pair.
//
// A group-resolution failure is deliberately a 500 and never a 403. The engine
// did not decide against the actor; it could not decide at all, and telling a
// user they lack a permission they may well have is worse than telling them the
// server is broken, which it is.
func StatusFor(err error) int {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, hmntsk.ErrGroupResolution):
		return StatusInternalServerError
	case errors.Is(err, hmntsk.ErrNotFound):
		return StatusNotFound
	case errors.Is(err, hmntsk.ErrUnauthorized):
		return StatusForbidden
	case errors.Is(err, hmntsk.ErrConflict):
		return StatusConflict
	case errors.Is(err, hmntsk.ErrValidation):
		return StatusBadRequest
	default:
		return StatusInternalServerError
	}
}

// CodeFor maps an engine error to the contract's error code.
func CodeFor(err error) ErrorCode {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, hmntsk.ErrGroupResolution):
		return CodeInternal
	case errors.Is(err, hmntsk.ErrNotFound):
		return CodeNotFound
	case errors.Is(err, hmntsk.ErrUnauthorized):
		return CodeForbidden
	case errors.Is(err, hmntsk.ErrConflict):
		return CodeConflict
	case errors.Is(err, hmntsk.ErrUnregisteredType):
		return CodeUnregisteredType
	case errors.Is(err, hmntsk.ErrValidation):
		return CodeValidation
	default:
		return CodeInternal
	}
}

// errorDetail renders an engine error for the wire, pulling out whatever detail
// the client needs to act on it.
func errorDetail(err error) ErrorDetail {
	detail := ErrorDetail{Code: CodeFor(err), Message: err.Error()}

	if detail.Code == CodeInternal && !errors.Is(err, hmntsk.ErrGroupResolution) {
		// Anything unanticipated may carry internals a client has no business
		// seeing, so only its classification crosses the boundary.
		detail.Message = "the request could not be completed"
	}

	var conflict *hmntsk.ConflictError
	if errors.As(err, &conflict) {
		current := conflict.Current
		detail.CurrentVersion = &current
	}

	var unregistered *hmntsk.UnregisteredTypeError
	if errors.As(err, &unregistered) {
		detail.TaskType = unregistered.Type
	}

	var validation *hmntsk.ValidationError
	if errors.As(err, &validation) {
		detail.Issues = validation.Issues
	}

	return detail
}

// badRequest builds a 400 for a problem the engine never saw, such as a body
// that is not JSON.
func badRequest(pointer, message string) error {
	return &hmntsk.ValidationError{
		Subject: "request",
		Issues:  []hmntsk.ValidationIssue{{Pointer: pointer, Detail: message}},
	}
}
