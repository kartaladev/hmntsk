package sqlcore

import (
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// Table names, before the host's prefix is applied.
const (
	// TasksTable holds one row per task. Everything filterable lives in a
	// column of its own: nothing the engine queries on is buried in a payload.
	TasksTable = "tasks"
	// CandidatesTable holds a task's candidate users, candidate groups and
	// exclusions, one row each. They are a child table rather than an array or
	// a JSON column because none of PostgreSQL arrays, MySQL JSON and SQLite
	// can be indexed portably for "the tasks Alice may claim".
	CandidatesTable = "task_candidates"
	// HistoryTable holds one row per accepted lifecycle transition.
	HistoryTable = "task_history"
	// OutboxTable holds the durable event record, written inside the same
	// transaction as the state change that produced it.
	OutboxTable = "task_outbox"
	// TypesTable holds registered task types, so that a host that is not
	// written in Go, or an inbox rendering forms from JSON Schema, can read
	// them from the database.
	TypesTable = "task_types"
)

// CandidateKind distinguishes the three kinds of row in the candidates table.
type CandidateKind string

// The candidate row kinds.
const (
	// CandidateUser names an actor eligible outright.
	CandidateUser CandidateKind = "user"
	// CandidateGroup names a group whose members are eligible.
	CandidateGroup CandidateKind = "group"
	// CandidateExcluded names an actor who is never eligible.
	CandidateExcluded CandidateKind = "excluded"
)

// taskColumns is the column list of the tasks table, in the order every
// statement in this package reads and writes it.
var taskColumns = []string{
	"id", "task_type", "version", "status", "suspended_from", "priority", "assignee",
	"owner_type", "owner_ref", "activity_key", "correlation_extra",
	"callback_address", "callback_params", "escalation",
	"input", "progress", "output", "reason", "created_by", "escalation_count",
	"created_at", "updated_at", "due_at", "started_at", "closed_at", "escalated_at",
	"locked_by", "locked_until",
}

// historyColumns is the column list of the history table.
var historyColumns = []string{
	"task_id", "version", "operation", "from_status", "to_status", "actor", "comment", "at",
}

// outboxColumns is the column list of the outbox table.
//
// The delivery state follows the event rather than being interleaved with it,
// because the payload's position is fixed: everything that reads an outbox row
// finds the whole event at one index and the relay's bookkeeping after it.
//
// There is no dead-letter column, and that is deliberate. An entry is delivered
// when it has a published time, pending when it has a next attempt and no
// published time, and dead-lettered when it has neither — three states from two
// columns, with no way to write a row that is two of them at once.
var outboxColumns = []string{
	"id", "task_id", "task_type", "event_type", "occurred_at", "published_at", "payload",
	"attempts", "next_attempt_at", "last_error", "locked_by", "locked_until", "accepted_sinks",
}

// typeColumns is the column list of the task types table.
var typeColumns = []string{
	"name", "title", "description", "input_schema", "output_schema",
	"default_priority", "default_deadline_ms", "default_escalation", "default_assignment",
	"updated_at",
}

// Statement is SQL and its arguments. Nothing here executes it.
type Statement struct {
	// SQL is the statement text, with the dialect's placeholder style already
	// applied.
	SQL string
	// Args are the bind values, in placeholder order.
	Args []any
}

// IsZero reports whether the statement is empty, which builders return when
// there is nothing to do — inserting an empty candidate pool, for instance.
func (s Statement) IsZero() bool { return s.SQL == "" }

// Builder produces the engine's statements for one dialect and one table
// prefix.
//
// It is safe for concurrent use and holds no connection, no state and no
// opinion about how the statements are run.
type Builder struct {
	dialect Dialect
	prefix  string
}

// Option configures a [Builder].
type Option func(*Builder)

// WithTablePrefix prefixes every table the engine owns, so that it can be
// embedded in a database that already uses those names. It is free to choose on
// day one and a breaking change afterwards.
func WithTablePrefix(prefix string) Option {
	return func(b *Builder) { b.prefix = prefix }
}

// New returns a builder for a dialect.
func New(dialect Dialect, opts ...Option) *Builder {
	b := &Builder{dialect: dialect}

	for _, opt := range opts {
		opt(b)
	}

	return b
}

// Dialect returns the dialect this builder targets.
func (b *Builder) Dialect() Dialect { return b.dialect }

// Prefix returns the configured table prefix.
func (b *Builder) Prefix() string { return b.prefix }

// TableName returns a table's unquoted name with the prefix applied.
func (b *Builder) TableName(name string) string { return b.prefix + name }

// Table returns a table's quoted, prefixed name, ready to interpolate.
func (b *Builder) Table(name string) string { return b.dialect.Quote(b.TableName(name)) }

// Tables returns every table the engine owns, prefixed and unquoted, in
// creation order.
func (b *Builder) Tables() []string {
	return []string{
		b.TableName(TasksTable),
		b.TableName(CandidatesTable),
		b.TableName(HistoryTable),
		b.TableName(OutboxTable),
		b.TableName(TypesTable),
	}
}

// TaskColumns returns the tasks table's columns in the order [Builder.SelectTask]
// reads them, which is the order a [TaskScanner] expects.
func (b *Builder) TaskColumns() []string { return append([]string(nil), taskColumns...) }

// HistoryColumns returns the history table's columns in read order.
func (b *Builder) HistoryColumns() []string { return append([]string(nil), historyColumns...) }

// OutboxColumns returns the outbox table's columns in the order
// [Builder.SelectOutboxEntry] reads them, which is the order [ScanOutboxEntries]
// expects.
func (b *Builder) OutboxColumns() []string { return append([]string(nil), outboxColumns...) }

// stmt accumulates SQL and its arguments, handing out placeholders in the
// dialect's style as it goes.
type stmt struct {
	builder *Builder
	sql     strings.Builder
	args    []any
}

// begin starts a statement.
func (b *Builder) begin() *stmt { return &stmt{builder: b} }

// write appends literal SQL.
func (s *stmt) write(parts ...string) {
	for _, part := range parts {
		s.sql.WriteString(part)
	}
}

// bind appends an argument and returns its placeholder.
func (s *stmt) bind(value any) string {
	s.args = append(s.args, value)

	return s.builder.dialect.Placeholder(len(s.args))
}

// bindAll appends several arguments and returns their placeholders, comma
// separated.
func (s *stmt) bindAll(values ...any) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, s.bind(value))
	}

	return strings.Join(out, ", ")
}

// done renders the accumulated statement.
func (s *stmt) done() Statement {
	return Statement{SQL: s.sql.String(), Args: s.args}
}

// quoteList renders a column list, quoted and comma separated, optionally
// prefixed with an already-quoted table alias.
func (b *Builder) quoteList(alias string, columns []string) string {
	out := make([]string, 0, len(columns))

	for _, column := range columns {
		quoted := b.dialect.Quote(column)
		if alias != "" {
			quoted = alias + "." + quoted
		}

		out = append(out, quoted)
	}

	return strings.Join(out, ", ")
}

// InsertTask renders the insert for a task row. Candidate rows are a separate
// statement, because they are a separate table.
func (b *Builder) InsertTask(task hmntsk.Task) Statement {
	s := b.begin()

	s.write("INSERT INTO ", b.Table(TasksTable), " (", b.quoteList("", taskColumns), ") VALUES (")
	s.write(s.bindAll(b.taskArgs(task)...))
	s.write(")")

	return s.done()
}

// taskArgs renders a task as arguments in taskColumns order.
func (b *Builder) taskArgs(task hmntsk.Task) []any {
	task = task.Normalize()

	return []any{
		string(task.ID),
		task.Type,
		task.Version,
		string(task.Status),
		nullString(string(task.SuspendedFrom)),
		int64(task.Priority),
		nullString(task.Assignee),
		nullString(task.Correlation.OwnerType),
		nullString(task.Correlation.OwnerRef),
		nullString(task.Correlation.ActivityKey),
		b.encodeValue(task.Correlation.Extra),
		nullString(callbackAddress(task.Callback)),
		b.encodeValue(callbackParameters(task.Callback)),
		b.encodeValue(task.Escalation),
		b.encodeRaw(task.Input),
		b.encodeRaw(task.Progress),
		b.encodeRaw(task.Output),
		nullString(task.Reason),
		nullString(task.CreatedBy),
		int64(task.EscalationCount),
		b.encodeTime(&task.CreatedAt),
		b.encodeTime(&task.UpdatedAt),
		b.encodeTime(task.DueAt),
		b.encodeTime(task.StartedAt),
		b.encodeTime(task.ClosedAt),
		b.encodeTime(task.EscalatedAt),
		nullString(task.LockedBy),
		b.encodeTime(task.LockedUntil),
	}
}

// UpdateTask renders the conditional update that is the engine's only
// concurrency mechanism.
//
// The version predicate is not optional and there is no unconditional variant:
// a write whose rows-affected count comes back zero is a conflict, which is
// also why nothing here needs RETURNING — MySQL does not have it.
func (b *Builder) UpdateTask(task hmntsk.Task, expectedVersion int64) Statement {
	s := b.begin()

	args := b.taskArgs(task)

	s.write("UPDATE ", b.Table(TasksTable), " SET ")

	assignments := make([]string, 0, len(taskColumns)-1)

	for i, column := range taskColumns {
		if column == "id" {
			continue
		}

		assignments = append(assignments, b.dialect.Quote(column)+" = "+s.bind(args[i]))
	}

	s.write(strings.Join(assignments, ", "))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(string(task.ID)))
	s.write(" AND ", b.dialect.Quote("version"), " = ", s.bind(expectedVersion))

	return s.done()
}

// SelectTask renders the read of one task row.
func (b *Builder) SelectTask(id hmntsk.TaskID) Statement {
	s := b.begin()

	s.write("SELECT ", b.quoteList("", taskColumns), " FROM ", b.Table(TasksTable))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(string(id)))

	return s.done()
}

// SelectTaskVersion renders the read of a task's current version, used to tell
// a losing writer what to re-read.
func (b *Builder) SelectTaskVersion(id hmntsk.TaskID) Statement {
	s := b.begin()

	s.write("SELECT ", b.dialect.Quote("version"), " FROM ", b.Table(TasksTable))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(string(id)))

	return s.done()
}

// DeleteTask renders the removal of a task row. Candidate, history and outbox
// rows are removed by the schema's foreign keys.
func (b *Builder) DeleteTask(id hmntsk.TaskID) Statement {
	s := b.begin()

	s.write("DELETE FROM ", b.Table(TasksTable))
	s.write(" WHERE ", b.dialect.Quote("id"), " = ", s.bind(string(id)))

	return s.done()
}

// InsertCandidates renders the insert of a task's candidate rows. It returns a
// zero statement when the pool names nobody, because an insert with no values
// is a syntax error on every dialect.
func (b *Builder) InsertCandidates(id hmntsk.TaskID, pool hmntsk.CandidatePool) Statement {
	type entry struct {
		kind  CandidateKind
		value string
	}

	entries := make([]entry, 0, len(pool.Users)+len(pool.Groups)+len(pool.Excluded))

	for _, user := range pool.Users {
		entries = append(entries, entry{CandidateUser, user})
	}

	for _, group := range pool.Groups {
		entries = append(entries, entry{CandidateGroup, group})
	}

	for _, excluded := range pool.Excluded {
		entries = append(entries, entry{CandidateExcluded, excluded})
	}

	if len(entries) == 0 {
		return Statement{}
	}

	s := b.begin()

	s.write("INSERT INTO ", b.Table(CandidatesTable), " (")
	s.write(b.quoteList("", []string{"task_id", "kind", "value", "ordinal"}))
	s.write(") VALUES ")

	rows := make([]string, 0, len(entries))

	for i, e := range entries {
		rows = append(rows, "("+s.bindAll(string(id), string(e.kind), e.value, int64(i))+")")
	}

	s.write(strings.Join(rows, ", "))

	return s.done()
}

// DeleteCandidates renders the removal of a task's candidate rows, which
// precedes rewriting them when a pool is widened.
func (b *Builder) DeleteCandidates(id hmntsk.TaskID) Statement {
	s := b.begin()

	s.write("DELETE FROM ", b.Table(CandidatesTable))
	s.write(" WHERE ", b.dialect.Quote("task_id"), " = ", s.bind(string(id)))

	return s.done()
}

// SelectCandidates renders the read of the candidate rows of one or more tasks,
// ordered so that a pool reads back in the order it was written.
func (b *Builder) SelectCandidates(ids ...hmntsk.TaskID) Statement {
	if len(ids) == 0 {
		return Statement{}
	}

	s := b.begin()

	values := make([]any, 0, len(ids))
	for _, id := range ids {
		values = append(values, string(id))
	}

	s.write("SELECT ", b.quoteList("", []string{"task_id", "kind", "value"}))
	s.write(" FROM ", b.Table(CandidatesTable))
	s.write(" WHERE ", b.dialect.Quote("task_id"), " IN (", s.bindAll(values...), ")")
	s.write(" ORDER BY ", b.dialect.Quote("task_id"), ", ", b.dialect.Quote("kind"))
	s.write(", ", b.dialect.Quote("ordinal"))

	return s.done()
}

// InsertHistory renders the append of transition records. History is
// append-only: there is no update or delete statement for it anywhere in this
// package.
func (b *Builder) InsertHistory(records ...hmntsk.TransitionRecord) Statement {
	if len(records) == 0 {
		return Statement{}
	}

	s := b.begin()

	s.write("INSERT INTO ", b.Table(HistoryTable), " (", b.quoteList("", historyColumns), ") VALUES ")

	rows := make([]string, 0, len(records))

	for _, record := range records {
		at := hmntsk.NormalizeTime(record.At)

		rows = append(rows, "("+s.bindAll(
			string(record.TaskID),
			record.Version,
			string(record.Operation),
			string(record.From),
			string(record.To),
			nullString(record.Actor),
			nullString(record.Comment),
			b.encodeTime(&at),
		)+")")
	}

	s.write(strings.Join(rows, ", "))

	return s.done()
}

// SelectHistory renders the read of a task's transition records, oldest first.
func (b *Builder) SelectHistory(id hmntsk.TaskID) Statement {
	s := b.begin()

	s.write("SELECT ", b.quoteList("", historyColumns), " FROM ", b.Table(HistoryTable))
	s.write(" WHERE ", b.dialect.Quote("task_id"), " = ", s.bind(string(id)))
	s.write(" ORDER BY ", b.dialect.Quote("version"), ", ", b.dialect.Quote("at"))

	return s.done()
}

// InsertOutbox renders the durable event record. The whole event is stored as
// JSON alongside the few columns a relay filters on, because a consumer reads
// events whole and nothing in the engine queries inside one.
//
// The row is born due: zero attempts, a next-attempt time equal to the instant
// the event occurred, no sink having accepted it and no lease held. The outbox
// exists so that delivery can happen after the commit, not later than it, and a
// row that had to be scheduled by somebody else would sit there until they did.
func (b *Builder) InsertOutbox(payloads []EventRow) Statement {
	if len(payloads) == 0 {
		return Statement{}
	}

	s := b.begin()

	s.write("INSERT INTO ", b.Table(OutboxTable), " (", b.quoteList("", outboxColumns), ") VALUES ")

	rows := make([]string, 0, len(payloads))

	for _, row := range payloads {
		occurred := hmntsk.NormalizeTime(row.OccurredAt)

		rows = append(rows, "("+s.bindAll(
			row.ID,
			string(row.TaskID),
			row.TaskType,
			string(row.EventType),
			b.encodeTime(&occurred),
			nil,
			b.encodeRaw(row.Payload),
			int64(0),
			b.encodeTime(&occurred),
			nil,
			nil,
			nil,
			nil,
		)+")")
	}

	s.write(strings.Join(rows, ", "))

	return s.done()
}

// EventRow is one durable event, ready to write. The engine's [hmntsk.Event] is
// encoded into Payload; the other fields are what a relay filters on.
type EventRow struct {
	// ID identifies the event.
	ID string
	// TaskID is the task the event is about.
	TaskID hmntsk.TaskID
	// TaskType is the task's registered type.
	TaskType string
	// EventType is the catalogue member.
	EventType hmntsk.EventType
	// OccurredAt is when the transition happened.
	OccurredAt time.Time
	// Payload is the whole event, encoded as JSON.
	Payload []byte
}

// SelectOutbox renders the read of undelivered events, oldest first. It is what
// a relay polls, and what the conformance suite reads to prove a rollback left
// nothing behind.
func (b *Builder) SelectOutbox(limit int) Statement {
	s := b.begin()

	s.write("SELECT ", b.quoteList("", outboxColumns), " FROM ", b.Table(OutboxTable))
	s.write(" WHERE ", b.dialect.Quote("published_at"), " IS NULL")
	s.write(" ORDER BY ", b.dialect.Quote("occurred_at"), ", ", b.dialect.Quote("id"))

	if limit > 0 {
		s.write(" LIMIT ", s.bind(int64(limit)))
	}

	return s.done()
}

// MarkOutboxPublished renders the acknowledgement a relay writes once an event
// has been delivered.
func (b *Builder) MarkOutboxPublished(at time.Time, ids ...string) Statement {
	if len(ids) == 0 {
		return Statement{}
	}

	s := b.begin()

	published := hmntsk.NormalizeTime(at)

	values := make([]any, 0, len(ids))
	for _, id := range ids {
		values = append(values, id)
	}

	s.write("UPDATE ", b.Table(OutboxTable), " SET ", b.dialect.Quote("published_at"))
	s.write(" = ", s.bind(b.encodeTime(&published)))
	s.write(" WHERE ", b.dialect.Quote("id"), " IN (", s.bindAll(values...), ")")

	return s.done()
}

// nullString maps the empty string to NULL, so that "no assignee" is absence
// rather than a sentinel value an index has to carry.
func nullString(value string) any {
	if value == "" {
		return nil
	}

	return value
}

// callbackAddress returns a callback target's address, or the empty string.
func callbackAddress(target *hmntsk.CallbackTarget) string {
	if target == nil {
		return ""
	}

	return target.Address
}

// callbackParameters returns a callback target's reference parameters, or nil.
func callbackParameters(target *hmntsk.CallbackTarget) any {
	if target == nil || len(target.ReferenceParameters) == 0 {
		return nil
	}

	return target.ReferenceParameters
}
