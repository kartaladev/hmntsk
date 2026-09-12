package sqlcore

import (
	"strings"

	"github.com/kartaladev/hmntsk"
)

// workingStatuses are the states a task can be escalated out of: not terminal,
// and not suspended.
var workingStatuses = []hmntsk.Status{
	hmntsk.StatusReady, hmntsk.StatusReserved, hmntsk.StatusInProgress,
}

// QueryTasks renders an inbox or correlation query.
//
// It selects one row beyond the caller's page size, so that the caller can tell
// whether another page exists without a second round trip and without a count.
//
// Eligibility is expressed as an EXISTS over the candidate child table rather
// than by unpacking an array or a JSON column, which is what lets every dialect
// serve it from the same index.
func (b *Builder) QueryTasks(query hmntsk.ResolvedQuery) Statement {
	s := b.begin()

	tasks := b.dialect.Quote("t")
	s.write("SELECT ", b.quoteList(tasks, taskColumns))
	s.write(" FROM ", b.Table(TasksTable), " AS ", tasks)

	conditions := b.queryConditions(s, query)
	if len(conditions) > 0 {
		s.write(" WHERE ", strings.Join(conditions, " AND "))
	}

	order := " ASC"
	if query.Descending {
		order = " DESC"
	}

	s.write(" ORDER BY ", tasks, ".", b.dialect.Quote("id"), order)
	s.write(" LIMIT ", s.bind(int64(query.EffectiveLimit()+1)))

	return s.done()
}

// queryConditions renders every filter the query names.
func (b *Builder) queryConditions(s *stmt, query hmntsk.ResolvedQuery) []string {
	alias := b.dialect.Quote("t") + "."

	conditions := make([]string, 0, 10)

	if query.Assignee != "" {
		conditions = append(conditions,
			alias+b.dialect.Quote("assignee")+" = "+s.bind(query.Assignee))
	}

	if len(query.Statuses) > 0 {
		values := make([]any, 0, len(query.Statuses))
		for _, status := range query.Statuses {
			values = append(values, string(status))
		}

		conditions = append(conditions,
			alias+b.dialect.Quote("status")+" IN ("+s.bindAll(values...)+")")
	}

	if len(query.Types) > 0 {
		values := make([]any, 0, len(query.Types))
		for _, taskType := range query.Types {
			values = append(values, taskType)
		}

		conditions = append(conditions,
			alias+b.dialect.Quote("task_type")+" IN ("+s.bindAll(values...)+")")
	}

	// Fixed order, never a map: with ? placeholders a fragment's position in the
	// statement text is what binds it to its argument, so anything that varies
	// the order of the fragments silently shuffles the arguments with it.
	for _, filter := range []struct {
		column string
		value  string
	}{
		{"owner_type", query.OwnerType},
		{"owner_ref", query.OwnerRef},
		{"activity_key", query.ActivityKey},
	} {
		if filter.value != "" {
			conditions = append(conditions,
				alias+b.dialect.Quote(filter.column)+" = "+s.bind(filter.value))
		}
	}

	if query.DueBefore != nil {
		conditions = append(conditions,
			alias+b.dialect.Quote("due_at")+" IS NOT NULL",
			alias+b.dialect.Quote("due_at")+" < "+s.bind(b.encodeTime(query.DueBefore)))
	}

	if query.Cursor != "" {
		comparison := " > "
		if query.Descending {
			comparison = " < "
		}

		conditions = append(conditions,
			alias+b.dialect.Quote("id")+comparison+s.bind(query.Cursor))
	}

	if candidate := b.candidateCondition(s, query); candidate != "" {
		conditions = append(conditions, candidate)
	}

	return conditions
}

// candidateCondition renders the eligibility filter: work the actor holds, or
// pooled work within their reach, and never work they are excluded from.
func (b *Builder) candidateCondition(s *stmt, query hmntsk.ResolvedQuery) string {
	if query.Candidate == "" {
		return ""
	}

	alias := b.dialect.Quote("t") + "."
	candidates := b.Table(CandidatesTable)
	child := b.dialect.Quote("c")
	excluded := b.dialect.Quote("x")

	// Every fragment is built in the order it will appear in the statement.
	// With ? placeholders a fragment's position in the text is what binds it to
	// its argument, so assembling the pieces in one order and emitting them in
	// another silently shuffles the arguments — and the symptom is an inbox
	// that quietly returns nothing.
	held := "(" + alias + b.dialect.Quote("assignee") + " = " + s.bind(query.Candidate) + ")"

	reach := []string{
		"(" + child + "." + b.dialect.Quote("kind") + " = " + s.bind(string(CandidateUser)) +
			" AND " + child + "." + b.dialect.Quote("value") + " = " + s.bind(query.Candidate) + ")",
	}

	if len(query.CandidateGroups) > 0 {
		groups := make([]any, 0, len(query.CandidateGroups))
		for _, group := range query.CandidateGroups {
			groups = append(groups, group)
		}

		reach = append(reach,
			"("+child+"."+b.dialect.Quote("kind")+" = "+s.bind(string(CandidateGroup))+
				" AND "+child+"."+b.dialect.Quote("value")+" IN ("+s.bindAll(groups...)+"))")
	}

	pooled := "(" + alias + b.dialect.Quote("assignee") + " IS NULL AND EXISTS (" +
		"SELECT 1 FROM " + candidates + " AS " + child +
		" WHERE " + child + "." + b.dialect.Quote("task_id") + " = " + alias + b.dialect.Quote("id") +
		" AND (" + strings.Join(reach, " OR ") + ")))"

	notExcluded := "NOT EXISTS (SELECT 1 FROM " + candidates + " AS " + excluded +
		" WHERE " + excluded + "." + b.dialect.Quote("task_id") + " = " + alias + b.dialect.Quote("id") +
		" AND " + excluded + "." + b.dialect.Quote("kind") + " = " + s.bind(string(CandidateExcluded)) +
		" AND " + excluded + "." + b.dialect.Quote("value") + " = " + s.bind(query.Candidate) + ")"

	return "((" + held + " OR " + pooled + ") AND " + notExcluded + ")"
}

// SelectOverdue renders the sweep's candidate selection: tasks past their
// deadline, still being worked on, and not already leased by another sweeper.
//
// Where the dialect offers it, the rows are locked with SKIP LOCKED. That is an
// optimisation and nothing more: the exclusive claim is [Builder.ClaimLease]'s
// conditional update, which works identically on a dialect with no row-level
// locking at all.
func (b *Builder) SelectOverdue(lease hmntsk.LeaseRequest) Statement {
	s := b.begin()

	s.write("SELECT ", b.dialect.Quote("id"), " FROM ", b.Table(TasksTable))
	s.write(" WHERE ", strings.Join(b.overdueConditions(s, lease), " AND "))
	s.write(" ORDER BY ", b.dialect.Quote("due_at"), ", ", b.dialect.Quote("id"))

	limit := lease.Limit
	if limit <= 0 {
		limit = DefaultLeaseBatch
	}

	s.write(" LIMIT ", s.bind(int64(limit)))

	if b.dialect.SupportsSkipLocked() {
		s.write(" FOR UPDATE SKIP LOCKED")
	}

	return s.done()
}

// DefaultLeaseBatch is how many overdue tasks one sweep claims when the caller
// names no limit.
const DefaultLeaseBatch = 100

// ClaimLease renders the conditional update that takes a time-bounded lease on
// one overdue task.
//
// It repeats the whole overdue predicate rather than trusting the selection that
// found the task, so two sweepers racing on the same row produce exactly one
// winner even though neither took a lock. A rows-affected count of zero means
// the other sweeper got there first.
//
// The lease does not advance the task's version. It is sweeper bookkeeping, not
// a change to the task, and bumping the version would make an actor's in-flight
// claim collide with a sweep that did nothing to their task.
func (b *Builder) ClaimLease(id hmntsk.TaskID, lease hmntsk.LeaseRequest) Statement {
	s := b.begin()

	until := hmntsk.NormalizeTime(lease.Now.Add(lease.Duration))

	s.write("UPDATE ", b.Table(TasksTable), " SET ")
	s.write(b.dialect.Quote("locked_by"), " = ", s.bind(nullString(lease.Owner)), ", ")
	s.write(b.dialect.Quote("locked_until"), " = ", s.bind(b.encodeTime(&until)))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(string(id)))
	s.write(" AND ", strings.Join(b.overdueConditions(s, lease), " AND "))

	return s.done()
}

// ReleaseLease renders the update a sweeper makes when it is done with a task
// it did not otherwise change.
func (b *Builder) ReleaseLease(id hmntsk.TaskID, owner string) Statement {
	s := b.begin()

	s.write("UPDATE ", b.Table(TasksTable), " SET ")
	s.write(b.dialect.Quote("locked_by"), " = ", s.bind(nil), ", ")
	s.write(b.dialect.Quote("locked_until"), " = ", s.bind(nil))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(string(id)))
	s.write(" AND ", b.dialect.Quote("locked_by"), " = ", s.bind(owner))

	return s.done()
}

// overdueConditions renders the predicate shared by the sweep's selection and
// its claim, so the two can never drift apart.
func (b *Builder) overdueConditions(s *stmt, lease hmntsk.LeaseRequest) []string {
	now := hmntsk.NormalizeTime(lease.Now)

	statuses := make([]any, 0, len(workingStatuses))
	for _, status := range workingStatuses {
		statuses = append(statuses, string(status))
	}

	conditions := []string{
		b.dialect.Quote("due_at") + " IS NOT NULL",
		b.dialect.Quote("due_at") + " <= " + s.bind(b.encodeTime(&now)),
		b.dialect.Quote("status") + " IN (" + s.bindAll(statuses...) + ")",
		"(" + b.dialect.Quote("locked_until") + " IS NULL OR " +
			b.dialect.Quote("locked_until") + " <= " + s.bind(b.encodeTime(&now)) + ")",
	}

	if len(lease.Types) > 0 {
		values := make([]any, 0, len(lease.Types))
		for _, taskType := range lease.Types {
			values = append(values, taskType)
		}

		conditions = append(conditions,
			b.dialect.Quote("task_type")+" IN ("+s.bindAll(values...)+")")
	}

	return conditions
}
