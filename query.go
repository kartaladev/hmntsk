package hmntsk

import (
	"slices"
	"time"
)

// DefaultQueryLimit is the page size a [Query] uses when it names none.
const DefaultQueryLimit = 50

// MaxQueryLimit is the largest page a [Query] may ask for.
const MaxQueryLimit = 500

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
	// DueBefore restricts results to tasks due strictly before this instant.
	DueBefore *time.Time
	// Limit is the page size. Zero means [DefaultQueryLimit]; anything above
	// [MaxQueryLimit] is capped.
	Limit int
	// Cursor continues a previous page. It is the [Page.NextCursor] value the
	// previous page returned, and is opaque to callers.
	Cursor string
	// Descending reverses the ordering, which is otherwise by task identifier
	// ascending.
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
// Paging is a keyset over the task identifier, which is time-ordered by
// construction, so a task already returned on an earlier page is never
// returned again on a later one however many tasks are created in between.
type Page struct {
	// Tasks are the matching tasks, in query order.
	Tasks []Task `json:"tasks"`
	// NextCursor continues the query. It is empty when the page is the last
	// one.
	NextCursor string `json:"nextCursor,omitempty"`
}
