package notify

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"time"
)

// State is where a notification is in its life. A notification never returns
// to [StateActive] once it has left it.
type State string

// The three states.
const (
	// StateActive is a notification its recipient has not read and nothing has
	// closed.
	StateActive State = "ACTIVE"
	// StateRead is a notification its recipient has read.
	StateRead State = "READ"
	// StateClosed is a notification whose subject moved on. Its ClosedReason
	// says why.
	StateClosed State = "CLOSED"
)

// Valid reports whether s is one of the three states.
func (s State) Valid() bool {
	switch s {
	case StateActive, StateRead, StateClosed:
		return true
	default:
		return false
	}
}

// The limits a draft is validated against. They are the widths of the SQL
// store's columns, applied to every store so that a draft accepted in memory is
// accepted everywhere.
const (
	// MaxKindBytes is the longest kind.
	MaxKindBytes = 100
	// MaxIdentifierBytes is the longest recipient, source and subject.
	MaxIdentifierBytes = 255
)

// Notification is one notification for one recipient.
//
// Kind, Title, Links and Data belong to the publisher. The library stores them
// and returns them exactly as published, and interprets none of them.
type Notification struct {
	// ID identifies the notification.
	ID string `json:"id"`
	// Recipient is who the notification is for.
	Recipient string `json:"recipient"`
	// SourceID identifies what produced the notification, such as an event.
	// Together with Recipient it makes publishing idempotent.
	SourceID string `json:"sourceId"`
	// Subject is what the notification is about.
	Subject string `json:"subject"`
	// SubjectVersion is the subject's version the notification describes.
	SubjectVersion int64 `json:"subjectVersion"`
	// Kind is the publisher's classification, such as "offer".
	Kind string `json:"kind"`
	// State is where the notification is in its life.
	State State `json:"state"`
	// ClosedReason says why a CLOSED notification was closed.
	ClosedReason string `json:"closedReason,omitempty"`
	// Title is a line a client can show.
	Title string `json:"title,omitempty"`
	// Links are hrefs by relation name, such as "task".
	Links map[string]string `json:"links,omitempty"`
	// Data is the publisher's JSON payload, byte for byte.
	Data json.RawMessage `json:"data,omitempty"`
	// CreatedAt is when the notification was published, UTC at microsecond
	// precision.
	CreatedAt time.Time `json:"createdAt"`
	// ReadAt is when the recipient first read it.
	ReadAt *time.Time `json:"readAt,omitempty"`
	// ClosedAt is when it was closed.
	ClosedAt *time.Time `json:"closedAt,omitempty"`
	// InactiveAt is when it first left ACTIVE, by being read or closed. The age
	// retention bound is measured from it.
	InactiveAt *time.Time `json:"inactiveAt,omitempty"`
}

// Clone returns a copy that shares no links, data or times with n, so that a
// store and its caller cannot change each other's copy.
func (n Notification) Clone() Notification {
	n.Links = maps.Clone(n.Links)
	n.Data = slices.Clone(n.Data)
	n.ReadAt = clonePtr(n.ReadAt)
	n.ClosedAt = clonePtr(n.ClosedAt)
	n.InactiveAt = clonePtr(n.InactiveAt)

	return n
}

// timePtr returns a pointer to a copy of an instant.
func timePtr(instant time.Time) *time.Time { return &instant }

// clonePtr copies an optional instant.
func clonePtr(instant *time.Time) *time.Time {
	if instant == nil {
		return nil
	}

	return timePtr(*instant)
}

// Draft is what a publisher supplies to create a notification. The library
// assigns the identifier, the creation time and the state.
type Draft struct {
	// Recipient is who the notification is for. Required.
	Recipient string
	// SourceID identifies what produced it. Required.
	SourceID string
	// Subject is what it is about. Required.
	Subject string
	// Kind is the publisher's classification. Required.
	Kind string
	// Title is a line a client can show.
	Title string
	// SubjectVersion is the subject's version it describes. It must not be
	// negative.
	SubjectVersion int64
	// Links are hrefs by relation name.
	Links map[string]string
	// Data is a JSON payload, stored byte for byte.
	Data json.RawMessage
	// Coalesce creates nothing when the recipient already has an ACTIVE or READ
	// notification of this kind on this subject.
	Coalesce bool
}

// Validate reports every problem with the draft as a [ValidationError], or nil.
func (d Draft) Validate() error {
	issues := validateContent(content{
		sourceID: d.SourceID, subject: d.Subject, kind: d.Kind,
		subjectVersion: d.SubjectVersion, data: d.Data,
	}, true)

	issues = append(issues, validateIdentifier("/recipient", d.Recipient)...)

	return invalid("draft", issues)
}

// content is what a draft and a close successor have in common.
type content struct {
	sourceID       string
	subject        string
	kind           string
	subjectVersion int64
	data           json.RawMessage
}

// validateContent checks the fields a draft and a successor share. withSubject
// is false for a successor, whose subject comes from the close.
func validateContent(c content, withSubject bool) []ValidationIssue {
	var issues []ValidationIssue

	issues = append(issues, validateIdentifier("/sourceId", c.sourceID)...)

	if withSubject {
		issues = append(issues, validateIdentifier("/subject", c.subject)...)
	}

	switch {
	case c.kind == "":
		issues = append(issues, ValidationIssue{Pointer: "/kind", Detail: "is required"})
	case len(c.kind) > MaxKindBytes:
		issues = append(issues, ValidationIssue{
			Pointer: "/kind", Detail: "is longer than " + strconv.Itoa(MaxKindBytes) + " bytes",
		})
	}

	if c.subjectVersion < 0 {
		issues = append(issues, ValidationIssue{Pointer: "/subjectVersion", Detail: "must not be negative"})
	}

	if len(c.data) > 0 && !json.Valid(c.data) {
		issues = append(issues, ValidationIssue{Pointer: "/data", Detail: "is not valid JSON"})
	}

	return issues
}

// validateIdentifier checks a required identifier and its length.
func validateIdentifier(pointer, value string) []ValidationIssue {
	switch {
	case value == "":
		return []ValidationIssue{{Pointer: pointer, Detail: "is required"}}
	case len(value) > MaxIdentifierBytes:
		return []ValidationIssue{{
			Pointer: pointer, Detail: "is longer than " + strconv.Itoa(MaxIdentifierBytes) + " bytes",
		}}
	default:
		return nil
	}
}
