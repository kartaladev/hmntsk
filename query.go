package hmntsk

import (
	"fmt"
	"slices"
	"time"
)

// DefaultQueryLimit is the page size a [Query] uses when it names none.
const DefaultQueryLimit = 50

// MaxQueryLimit is the largest page a [Query] may ask for.
const MaxQueryLimit = 500

// Ordering selects the key a [Query] orders its results by. The set is closed:
// each ordering is a key every store can page through exactly, served by an
// index on every supported dialect. An ordering outside the set is a
// validation error, never a silent fallback.
//
// Every ordering ends with the task identifier as its final tiebreak, which is
// creation order, so an ordering is total. [Query.Descending] reverses the
// whole key, except that tasks without a deadline sort last under [OrderDue]
// and [OrderUrgency] in both directions.
type Ordering string

const (
	// OrderCreated orders by creation. It is the zero value, and the default.
	OrderCreated Ordering = ""
	// OrderPriority orders by priority, most urgent first, then by creation.
	OrderPriority Ordering = "priority"
	// OrderDue orders by due date, earliest first, then by creation. Tasks
	// without a deadline come last.
	OrderDue Ordering = "due"
	// OrderUrgency orders by priority, then by due date with tasks without a
	// deadline last, then by creation.
	OrderUrgency Ordering = "urgency"
)

// Valid reports whether o is one of the supported orderings.
func (o Ordering) Valid() bool {
	switch o {
	case OrderCreated, OrderPriority, OrderDue, OrderUrgency:
		return true
	default:
		return false
	}
}

// Query selects tasks. Every field it filters on is a stored column: nothing
// here is answered by reading a payload, so that correlation lookups and inbox
// queries do not depend on three incompatible JSON query languages.
//
// An empty Query matches every task.
type Query struct {
	// Assignee restricts results to tasks reserved for this actor.
	Assignee string
	// Candidate restricts results to tasks this actor may act on: reserved for
	// them, or pooled and within their reach. Group membership is resolved
	// live, so this reflects the directory as it stands now.
	Candidate string
	// Statuses restricts results to these lifecycle states.
	Statuses []Status
	// Types restricts results to these task types.
	Types []string
	// OwnerType, OwnerRef and ActivityKey filter on correlation.
	OwnerType   string
	OwnerRef    string
	ActivityKey string
	// Group restricts results to tasks whose candidate pool names this group.
	// Membership is not resolved and exclusions are not evaluated: it is the
	// group's queue as configured, not as any one member sees it. Empty means
	// no group filter.
	Group string
	// DueBefore restricts results to tasks due strictly before this instant.
	DueBefore *time.Time
	// OrderBy selects the ordering. The zero value is [OrderCreated].
	OrderBy Ordering
	// Limit is the page size. Zero means [DefaultQueryLimit]; anything above
	// [MaxQueryLimit] is capped.
	Limit int
	// Cursor continues a previous page. It is the [Page.NextCursor] value the
	// previous page returned, and is opaque to callers.
	Cursor string
	// Descending reverses the ordering [Query.OrderBy] selects, tiebreak
	// included. Tasks without a deadline still sort last under [OrderDue] and
	// [OrderUrgency].
	Descending bool
}

// EffectiveLimit returns the page size to use, with the default and cap
// applied.
func (q Query) EffectiveLimit() int {
	switch {
	case q.Limit <= 0:
		return DefaultQueryLimit
	case q.Limit > MaxQueryLimit:
		return MaxQueryLimit
	default:
		return q.Limit
	}
}

// validate rejects what no store can answer. It checks only what the query
// says about itself; a cursor is checked by the store that decodes it.
func (q Query) validate() error {
	if !q.OrderBy.Valid() {
		return &ValidationError{Subject: "request", Issues: []ValidationIssue{{
			Detail: fmt.Sprintf("ordering %q is not supported", string(q.OrderBy)),
		}}}
	}

	return nil
}

// Clone returns a deep copy.
func (q Query) Clone() Query {
	out := q
	out.Statuses = slices.Clone(q.Statuses)
	out.Types = slices.Clone(q.Types)
	out.DueBefore = cloneTime(q.DueBefore)

	return out
}

// ResolvedQuery is a [Query] with the candidate's group membership already
// resolved. It is what a [Repository] receives, so that no store adapter ever
// needs a [GroupResolver] of its own.
type ResolvedQuery struct {
	Query
	// CandidateGroups are the groups [Query.Candidate] belongs to, as the
	// [Service] resolved them at the moment of the query. It is empty when no
	// candidate was named.
	CandidateGroups []string
}

// Page is one page of query results.
//
// Paging is a keyset over the ordering's whole key, which ends with the task
// identifier, so a task already returned on an earlier page is never returned
// again on a later one however many tasks are created in between.
type Page struct {
	// Tasks are the matching tasks, in query order.
	Tasks []Task `json:"tasks"`
	// NextCursor continues the query. It is empty when the page is the last
	// one. Its encoding is the store's own and may change. It is bound to the
	// ordering and direction that produced it: continuing a different one, or
	// passing a value the store did not issue, is a validation error.
	NextCursor string `json:"nextCursor,omitempty"`
}
