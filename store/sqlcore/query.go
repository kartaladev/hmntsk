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

// SelectDueEvents renders the relay's candidate selection: events that are due
// for another delivery attempt and not already held by another relay.
//
// Where the dialect offers it the rows are locked with SKIP LOCKED. That is an
// optimisation and nothing more: the exclusive claim is [Builder.ClaimEvent]'s
// conditional update, which works identically on a dialect with no row-level
// locking at all.
//
// Events come back oldest first so that a backlog drains in the order it
// accumulated. That is an ordering of the pass, not a delivery guarantee: a
// failed event retries after events recorded later than it.
func (b *Builder) SelectDueEvents(claim hmntsk.OutboxClaim) Statement {
	s := b.begin()

	s.write("SELECT ", b.dialect.Quote("id"), " FROM ", b.Table(OutboxTable))
	s.write(" WHERE ", strings.Join(b.dueEventConditions(s, claim), " AND "))
	s.write(" ORDER BY ", b.dialect.Quote("occurred_at"), ", ", b.dialect.Quote("id"))

	limit := claim.Limit
	if limit <= 0 {
		limit = hmntsk.DefaultOutboxBatch
	}

	s.write(" LIMIT ", s.bind(int64(limit)))

	if b.dialect.SupportsSkipLocked() {
		s.write(" FOR UPDATE SKIP LOCKED")
	}

	return s.done()
}

// ClaimEvent renders the conditional update that takes a time-bounded lease on
// one due event.
//
// It repeats the whole due predicate rather than trusting the selection that
// found the event, so two relays racing on the same row produce exactly one
// winner even though neither took a lock. A rows-affected count of zero means
// the other relay got there first, which is the mechanism working rather than a
// failure.
func (b *Builder) ClaimEvent(eventID string, claim hmntsk.OutboxClaim) Statement {
	s := b.begin()

	until := hmntsk.NormalizeTime(claim.Now.Add(claim.Duration))

	s.write("UPDATE ", b.Table(OutboxTable), " SET ")
	s.write(b.dialect.Quote("locked_by"), " = ", s.bind(nullString(claim.Owner)), ", ")
	s.write(b.dialect.Quote("locked_until"), " = ", s.bind(b.encodeTime(&until)))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(eventID))
	s.write(" AND ", strings.Join(b.dueEventConditions(s, claim), " AND "))

	return s.done()
}

// dueEventConditions renders the predicate shared by the relay's selection and
// its claim, so the two can never drift apart.
//
// An event is due when it is neither delivered nor dead-lettered, its
// next-attempt time has passed, and no live lease is held on it. A lease whose
// deadline has passed is nobody's, which is what stops a relay that crashed
// mid-pass from stranding the event forever.
func (b *Builder) dueEventConditions(s *stmt, claim hmntsk.OutboxClaim) []string {
	now := hmntsk.NormalizeTime(claim.Now)

	return []string{
		b.dialect.Quote("published_at") + " IS NULL",
		b.dialect.Quote("next_attempt_at") + " IS NOT NULL",
		b.dialect.Quote("next_attempt_at") + " <= " + s.bind(b.encodeTime(&now)),
		"(" + b.dialect.Quote("locked_until") + " IS NULL OR " +
			b.dialect.Quote("locked_until") + " <= " + s.bind(b.encodeTime(&now)) + ")",
	}
}

// RecordAttempt renders the write that follows a failed but retryable attempt:
// the new attempt count, when the event becomes due again, and what went wrong.
func (b *Builder) RecordAttempt(record hmntsk.AttemptRecord) Statement {
	s := b.begin()

	due := hmntsk.NormalizeTime(record.NextAttemptAt)

	s.write("UPDATE ", b.Table(OutboxTable), " SET ")
	s.write(b.dialect.Quote("attempts"), " = ", s.bind(int64(record.Attempts)), ", ")
	s.write(b.dialect.Quote("next_attempt_at"), " = ", s.bind(b.encodeTime(&due)), ", ")
	s.write(b.dialect.Quote("last_error"), " = ", s.bind(nullString(record.LastError)))
	s.write(b.releaseDelivery(s))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(record.EventID))

	return s.done()
}

// MarkAccepted renders the write that records which sinks have taken an event.
//
// The accepted set is replaced rather than added to, so recording the same
// acceptance twice is harmless — which matters, because a relay can crash
// between delivering and recording. The event becomes delivered only once every
// configured sink has accepted it, which is the caller's judgement and arrives
// as a published time.
func (b *Builder) MarkAccepted(acceptance hmntsk.Acceptance) Statement {
	s := b.begin()

	// An event every sink took has no outstanding failure to report, whatever
	// the attempt that finished it had to say about the sinks that refused
	// earlier.
	lastError := acceptance.LastError
	if acceptance.PublishedAt != nil {
		lastError = ""
	}

	s.write("UPDATE ", b.Table(OutboxTable), " SET ")
	s.write(b.dialect.Quote("accepted_sinks"), " = ", s.bind(b.encodeSinks(acceptance.Accepted)), ", ")
	s.write(b.dialect.Quote("attempts"), " = ", s.bind(int64(acceptance.Attempts)), ", ")
	s.write(b.dialect.Quote("last_error"), " = ", s.bind(nullString(lastError)), ", ")
	s.write(b.dialect.Quote("published_at"), " = ", s.bind(b.encodeTime(acceptance.PublishedAt)))

	// A partial acceptance says when the sinks that refused become due again. A
	// delivered event keeps whatever next attempt it already had: the published
	// time is what settles it, and clearing the next attempt as well would make
	// a delivered event indistinguishable from a dead letter to anything
	// reading the row rather than the port.
	if acceptance.PublishedAt == nil && acceptance.NextAttemptAt != nil {
		s.write(", ", b.dialect.Quote("next_attempt_at"))
		s.write(" = ", s.bind(b.encodeTime(acceptance.NextAttemptAt)))
	}

	s.write(b.releaseDelivery(s))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(acceptance.EventID))

	return s.done()
}

// MarkDeadLettered renders the write that stops further attempts on an event.
//
// Clearing the next-attempt time is what marks it dead: a dead letter is the
// entry with no next attempt that was never published. The row stays, with its
// attempt count and its last error, so that "what failed, and why?" is a query
// rather than a log search.
func (b *Builder) MarkDeadLettered(letter hmntsk.DeadLetter) Statement {
	s := b.begin()

	s.write("UPDATE ", b.Table(OutboxTable), " SET ")
	s.write(b.dialect.Quote("attempts"), " = ", s.bind(int64(letter.Attempts)), ", ")
	s.write(b.dialect.Quote("last_error"), " = ", s.bind(nullString(letter.LastError)), ", ")

	// A sink that took the event on this very attempt is recorded too. Nothing
	// will read it to decide a retry — there will not be one — but it is what
	// answers "which destinations got it?" when the dead letter is inspected.
	if letter.Accepted != nil {
		s.write(b.dialect.Quote("accepted_sinks"), " = ",
			s.bind(b.encodeSinks(letter.Accepted)), ", ")
	}

	s.write(b.dialect.Quote("next_attempt_at"), " = ", s.bind(nil))
	s.write(b.releaseDelivery(s))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(letter.EventID))

	return s.done()
}

// SelectOutboxEntry renders the read of one outbox row whole, delivery state
// included. It is how a dead letter is inspected, which is the only thing the
// engine promises about one.
func (b *Builder) SelectOutboxEntry(eventID string) Statement {
	s := b.begin()

	s.write("SELECT ", b.quoteList("", outboxColumns), " FROM ", b.Table(OutboxTable))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(eventID))

	return s.done()
}

// releaseDelivery renders the lease release every settlement ends with. The
// event is not this relay's any more, whatever the outcome was, and a
// settlement that kept the lease would strand the event until it expired.
func (b *Builder) releaseDelivery(s *stmt) string {
	return ", " + b.dialect.Quote("locked_by") + " = " + s.bind(nil) +
		", " + b.dialect.Quote("locked_until") + " = " + s.bind(nil)
}
