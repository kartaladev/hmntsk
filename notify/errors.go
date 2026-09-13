package notify

import (
	"errors"
	"strings"
)

// The sentinels every notify error matches. Match them with [errors.Is]; the
// concrete types below carry the detail.
var (
	// ErrNotFound reports a notification that does not exist, or that belongs to
	// another recipient. The two are indistinguishable on purpose, so that
	// identifiers cannot be probed.
	ErrNotFound = errors.New("notify: not found")
	// ErrValidation reports a request or draft that is not valid.
	ErrValidation = errors.New("notify: not valid")
	// ErrUnauthorized reports an acting user who may not do what they asked,
	// or no acting user at all.
	ErrUnauthorized = errors.New("notify: unauthorized")
	// ErrConfiguration reports a wiring mistake, found at construction.
	ErrConfiguration = errors.New("notify: invalid configuration")
	// ErrTooManyStreams reports a recipient over the per-instance stream cap.
	ErrTooManyStreams = errors.New("notify: too many streams")
	// ErrUnavailable reports a realtime stream requested while the hub is not
	// receiving signals.
	ErrUnavailable = errors.New("notify: unavailable")
)

// ConfigurationError reports a contradictory or incomplete configuration. It is
// returned by constructors, before any traffic.
type ConfigurationError struct {
	// Detail says what is wrong and, where there is one, how to fix it.
	Detail string
}

// Error implements the error interface.
func (e *ConfigurationError) Error() string { return "notify: " + e.Detail }

// Unwrap makes the error match [ErrConfiguration].
func (e *ConfigurationError) Unwrap() error { return ErrConfiguration }

// ValidationIssue is one problem with a request or draft.
type ValidationIssue struct {
	// Pointer is an RFC 6901 JSON Pointer to the offending field, empty when the
	// problem is the whole value.
	Pointer string `json:"pointer,omitempty"`
	// Detail describes what is wrong, in terms a client can show a user.
	Detail string `json:"detail"`
}

// String implements [fmt.Stringer].
func (i ValidationIssue) String() string {
	if i.Pointer == "" {
		return i.Detail
	}

	return i.Pointer + ": " + i.Detail
}

// ValidationError reports a request or draft that is not valid. It carries
// every problem found rather than only the first.
type ValidationError struct {
	// Subject names what was validated, such as "draft" or "request".
	Subject string
	// Issues lists every problem found.
	Issues []ValidationIssue
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	subject := e.Subject
	if subject == "" {
		subject = "value"
	}

	if len(e.Issues) == 0 {
		return "notify: " + subject + " is not valid"
	}

	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.String())
	}

	return "notify: " + subject + " is not valid: " + strings.Join(parts, "; ")
}

// Unwrap makes the error match [ErrValidation].
func (e *ValidationError) Unwrap() error { return ErrValidation }

// invalid builds a [ValidationError] from collected issues, or returns nil when
// there are none.
func invalid(subject string, issues []ValidationIssue) error {
	if len(issues) == 0 {
		return nil
	}

	return &ValidationError{Subject: subject, Issues: issues}
}
