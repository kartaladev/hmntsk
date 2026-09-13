// The storage port and the requests and results that cross it.
//
//go:generate mockgen -source=store.go -package=notify -destination=store_mock_test.go -typed

package notify

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Store is where notifications live. The default is [NewMemoryStore];
// notify/sqlstore stores them in PostgreSQL, MySQL or SQLite; a host may supply
// its own, provided it passes the notify/notifytest conformance suite.
//
// Every method is its own transaction, and never joins a transaction the caller
// holds for other data. Callers are the [Service] and the [Pruner]: requests
// reach a store already validated, and notifications reach Insert already
// stamped.
type Store interface {
	// Insert publishes notifications that all share one subject, applying, in
	// the subject's serialised write: watermark suppression, coalescing, and
	// idempotency on (SourceID, Recipient).
	Insert(ctx context.Context, subject string, insertions []Insertion) (InsertResult, error)
	// Close closes a subject's notifications by kind and version, raises the
	// subject's watermark, and publishes req.Successor to the recipients it
	// closed, all in one transaction. ids stamps each successor.
	Close(ctx context.Context, req CloseRequest, at time.Time, ids IDGenerator) (CloseResult, error)
	// Get reads one of a recipient's notifications. Another recipient's
	// notification is reported as [ErrNotFound], exactly like a missing one.
	Get(ctx context.Context, recipient, id string) (Notification, error)
	// List returns a page of a recipient's notifications, newest first.
	List(ctx context.Context, q ListQuery) (Page, error)
	// CountActive counts a recipient's ACTIVE notifications.
	CountActive(ctx context.Context, recipient string) (int64, error)
	// MarkRead marks a recipient's notifications read at an instant. Any id
	// that is not the recipient's is [ErrNotFound], and nothing is marked.
	MarkRead(ctx context.Context, recipient string, ids []string, at time.Time) (MarkResult, error)
	// MarkAllRead marks every ACTIVE notification of a recipient created at or
	// before through as read at an instant.
	MarkAllRead(ctx context.Context, recipient string, through, at time.Time) (MarkResult, error)
	// Prune applies retention bounds and expires watermarks.
	Prune(ctx context.Context, req PruneRequest) (PruneResult, error)
}

// Insertion is one stamped notification to insert.
type Insertion struct {
	// Notification is stamped with its ID, CreatedAt and ACTIVE state.
	Notification Notification
	// Coalesce creates nothing when the recipient already has an ACTIVE or READ
	// notification of this kind on this subject.
	Coalesce bool
}

// InsertResult is what an insert did.
type InsertResult struct {
	// Created lists the notifications created, as stored.
	Created []Notification
	// Duplicates counts notifications whose source and recipient already had
	// one.
	Duplicates int
	// Suppressed counts notifications below the subject's watermark.
	Suppressed int
	// Coalesced counts coalescing notifications an open one absorbed.
	Coalesced int
}

// add accumulates another result into r.
func (r *InsertResult) add(other InsertResult) {
	r.Created = append(r.Created, other.Created...)
	r.Duplicates += other.Duplicates
	r.Suppressed += other.Suppressed
	r.Coalesced += other.Coalesced
}

// Successor is a notification a close publishes, in the same transaction, to
// each recipient it closed. Its subject is the close's.
type Successor struct {
	// SourceID identifies what produced it. Required.
	SourceID string
	// Kind is the publisher's classification. Required.
	Kind string
	// Title is a line a client can show.
	Title string
	// SubjectVersion is the subject's version it describes.
	SubjectVersion int64
	// Links are hrefs by relation name.
	Links map[string]string
	// Data is a JSON payload, stored byte for byte.
	Data json.RawMessage
}

// CloseRequest closes a subject's notifications.
type CloseRequest struct {
	// Subject is the subject to close. Required.
	Subject string
	// Kinds restricts the close to these kinds. Empty means every kind.
	Kinds []string
	// Version closes notifications at or below it, and becomes the watermark
	// below which a later publish of those kinds is suppressed.
	Version int64
	// Reason is recorded on every notification closed.
	Reason string
	// Except spares one recipient.
	Except string
	// Successor, when set, is published to each recipient this close closed.
	Successor *Successor
	// SuccessorSkip lists recipients who get no successor.
	SuccessorSkip []string
}

// Validate reports every problem with the request as a [ValidationError], or
// nil.
func (r CloseRequest) Validate() error {
	issues := validateIdentifier("/subject", r.Subject)

	if r.Version < 0 {
		issues = append(issues, ValidationIssue{Pointer: "/version", Detail: "must not be negative"})
	}

	for i, kind := range r.Kinds {
		if kind == "" || len(kind) > MaxKindBytes {
			issues = append(issues, ValidationIssue{
				Pointer: "/kinds/" + strconv.Itoa(i),
				Detail:  "must be between 1 and " + strconv.Itoa(MaxKindBytes) + " bytes",
			})
		}
	}

	if len(r.Reason) > MaxIdentifierBytes {
		issues = append(issues, ValidationIssue{
			Pointer: "/reason", Detail: "is longer than " + strconv.Itoa(MaxIdentifierBytes) + " bytes",
		})
	}

	if s := r.Successor; s != nil {
		for _, issue := range validateContent(content{
			sourceID: s.SourceID, kind: s.Kind, subjectVersion: s.SubjectVersion, data: s.Data,
		}, false) {
			issue.Pointer = "/successor" + issue.Pointer
			issues = append(issues, issue)
		}
	}

	return invalid("close", issues)
}

// SuccessorInsertions stamps the request's successor for each recipient a close
// closed, skipping SuccessorSkip, in the order recipients are given. A store
// calls it inside its close transaction, once the recipients are known, and
// inserts the result through its ordinary insert path. It returns nothing when
// the request names no successor.
func (r CloseRequest) SuccessorInsertions(recipients []string, at time.Time, ids IDGenerator) ([]Insertion, error) {
	if r.Successor == nil {
		return nil, nil
	}

	successor := r.Successor
	out := make([]Insertion, 0, len(recipients))

	for _, recipient := range recipients {
		if slices.Contains(r.SuccessorSkip, recipient) {
			continue
		}

		id, err := ids.NewID()
		if err != nil {
			return nil, err
		}

		out = append(out, Insertion{Notification: Notification{
			ID: id, Recipient: recipient, SourceID: successor.SourceID, Subject: r.Subject,
			SubjectVersion: successor.SubjectVersion, Kind: successor.Kind, State: StateActive,
			Title: successor.Title, Links: successor.Links, Data: successor.Data, CreatedAt: at,
		}})
	}

	return out, nil
}

// CloseResult is what a close did.
type CloseResult struct {
	// Closed counts the notifications moved to CLOSED.
	Closed int64
	// Recipients lists, sorted, the recipients whose notifications were closed.
	Recipients []string
	// Successors lists the successor notifications created.
	Successors []Notification
	// SuccessorsSuppressed counts successors below a newer watermark.
	SuccessorsSuppressed int
}

// MarkResult is what a mark-read did.
type MarkResult struct {
	// Marked counts notifications moved from ACTIVE to READ.
	Marked int64
}

// The list page size limits.
const (
	// DefaultListLimit is the page size when a query names none.
	DefaultListLimit = 50
	// MaxListLimit is the largest page size a query may ask for.
	MaxListLimit = 500
)

// ListQuery asks for a page of one recipient's notifications, newest first.
type ListQuery struct {
	// Recipient is whose notifications to list. Required.
	Recipient string
	// States filters by state. Empty means every state.
	States []State
	// Kinds filters by kind. Empty means every kind.
	Kinds []string
	// Subject filters by subject. Empty means every subject.
	Subject string
	// Limit is the page size: [DefaultListLimit] when zero, at most
	// [MaxListLimit].
	Limit int
	// Cursor continues from the previous page's NextCursor. A cursor is bound to
	// the recipient and filters that produced it.
	Cursor string
}

// Validate reports every problem with the query as a [ValidationError], or nil.
func (q ListQuery) Validate() error {
	issues := validateIdentifier("/recipient", q.Recipient)

	if q.Limit < 0 || q.Limit > MaxListLimit {
		issues = append(issues, ValidationIssue{
			Pointer: "/limit", Detail: "must be between 1 and " + strconv.Itoa(MaxListLimit),
		})
	}

	for i, state := range q.States {
		if !state.Valid() {
			issues = append(issues, ValidationIssue{
				Pointer: "/states/" + strconv.Itoa(i), Detail: "is not ACTIVE, READ or CLOSED",
			})
		}
	}

	if len(issues) == 0 {
		if _, _, err := DecodeCursor(q); err != nil {
			return err
		}
	}

	return invalid("request", issues)
}

// EffectiveLimit is the page size the query asks for, defaulted.
func (q ListQuery) EffectiveLimit() int {
	if q.Limit <= 0 {
		return DefaultListLimit
	}

	return q.Limit
}

// Page is one page of notifications.
type Page struct {
	// Notifications are newest first.
	Notifications []Notification
	// NextCursor continues the listing, and is empty on the last page.
	NextCursor string
}

// CursorPosition is where a page ended: the last notification's creation time
// and identifier, which together order a listing exactly.
type CursorPosition struct {
	// CreatedAt is the last notification's creation time.
	CreatedAt time.Time
	// ID is the last notification's identifier.
	ID string
}

// cursorPayload is a cursor's encoded form. Filter binds it to the query that
// produced it, so that it cannot continue a different listing.
type cursorPayload struct {
	CreatedAt string `json:"t"`
	ID        string `json:"i"`
	Filter    string `json:"f"`
}

// EncodeCursor renders a position as an opaque cursor bound to q's recipient and
// filters. Stores call it to build [Page.NextCursor].
func EncodeCursor(q ListQuery, position CursorPosition) string {
	encoded, _ := json.Marshal(cursorPayload{
		CreatedAt: normalizeTime(position.CreatedAt).Format(time.RFC3339Nano),
		ID:        position.ID,
		Filter:    q.fingerprint(),
	})

	return base64.RawURLEncoding.EncodeToString(encoded)
}

// DecodeCursor reads q's cursor. It reports false when q has no cursor, and a
// [ValidationError] when the cursor is malformed or belongs to another
// recipient or filter set.
func DecodeCursor(q ListQuery) (CursorPosition, bool, error) {
	if q.Cursor == "" {
		return CursorPosition{}, false, nil
	}

	refused := func(detail string) error {
		return &ValidationError{Subject: "request", Issues: []ValidationIssue{{Pointer: "/cursor", Detail: detail}}}
	}

	raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil {
		return CursorPosition{}, false, refused("is not a cursor")
	}

	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.ID == "" {
		return CursorPosition{}, false, refused("is not a cursor")
	}

	createdAt, err := time.Parse(time.RFC3339Nano, payload.CreatedAt)
	if err != nil {
		return CursorPosition{}, false, refused("is not a cursor")
	}

	if payload.Filter != q.fingerprint() {
		return CursorPosition{}, false, refused("belongs to another recipient or filter set")
	}

	return CursorPosition{CreatedAt: normalizeTime(createdAt), ID: payload.ID}, true, nil
}

// fingerprint identifies a query's recipient and filters, ignoring the order
// the filters are listed in.
func (q ListQuery) fingerprint() string {
	states := make([]string, 0, len(q.States))
	for _, state := range q.States {
		states = append(states, string(state))
	}

	slices.Sort(states)

	kinds := slices.Clone(q.Kinds)
	slices.Sort(kinds)

	sum := sha256.Sum256([]byte(strings.Join([]string{
		q.Recipient, strings.Join(states, ","), strings.Join(kinds, ","), q.Subject,
	}, "\x00")))

	return hex.EncodeToString(sum[:8])
}

// RetentionStrategy decides whether the count bound may evict ACTIVE
// notifications.
type RetentionStrategy string

// The retention strategies.
const (
	// EvictOldestActive is the default. To bring a recipient within the count
	// bound it deletes the oldest inactive notifications first, then, only when
	// none are left, the oldest ACTIVE ones. Unread notifications can be lost.
	EvictOldestActive RetentionStrategy = "EVICT_OLDEST_ACTIVE"
	// RetainActive never deletes an ACTIVE notification. A recipient whose
	// ACTIVE notifications alone exceed the bound keeps all of them.
	RetainActive RetentionStrategy = "RETAIN_ACTIVE"
)

// Valid reports whether s is one of the defined strategies.
func (s RetentionStrategy) Valid() bool {
	return s == EvictOldestActive || s == RetainActive
}

// PruneRequest is one pruning pass's bounds, as the [Pruner] resolved them.
type PruneRequest struct {
	// Now is the instant the pass measures ages from.
	Now time.Time
	// MaxPerRecipient bounds each recipient's notifications. Zero means no
	// count bound.
	MaxPerRecipient int
	// MaxAge bounds how long a notification stays after it became inactive.
	// Zero means no age bound.
	MaxAge time.Duration
	// Strategy decides whether the count bound may evict ACTIVE notifications.
	Strategy RetentionStrategy
	// WatermarkRetention is how long a subject's close record outlives its last
	// notification. Zero means close records never expire.
	WatermarkRetention time.Duration
	// Batch caps the rows one statement deletes.
	Batch int
}

// PruneResult is what a pruning pass removed.
type PruneResult struct {
	// DeletedForAge counts inactive notifications deleted for their age.
	DeletedForAge int64
	// DeletedForCount counts inactive notifications deleted for the count bound.
	DeletedForCount int64
	// EvictedActive counts ACTIVE notifications evicted for the count bound.
	EvictedActive int64
	// WatermarksDeleted counts expired close records deleted.
	WatermarksDeleted int64
	// Recipients lists, sorted, the recipients who had ACTIVE notifications
	// evicted.
	Recipients []string
}
