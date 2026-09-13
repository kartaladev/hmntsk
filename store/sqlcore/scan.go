package sqlcore

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kartaladev/hmntsk"
)

// Rows is the little that this package needs of a driver's result set. Every
// supported driver satisfies it, which is what lets one scanner serve all
// three.
type Rows interface {
	// Next advances to the next row.
	Next() bool
	// Scan reads the current row into dest.
	Scan(dest ...any) error
	// Err reports any error that ended the iteration.
	Err() error
}

// TaskScanner reads task rows.
//
// It scans every column into an any and converts afterwards, rather than into
// typed destinations. That is deliberate: the three drivers disagree about what
// a timestamp, a JSON column and an integer come back as — time.Time or text,
// string or bytes, int64 or a decimal string — and a scanner that accommodates
// all of it in one place is one place, instead of one per adapter.
type TaskScanner struct {
	values []any
	dest   []any
}

// NewTaskScanner returns a scanner for the column order [Builder.TaskColumns]
// declares.
func NewTaskScanner() *TaskScanner {
	s := &TaskScanner{
		values: make([]any, len(taskColumns)),
		dest:   make([]any, len(taskColumns)),
	}

	for i := range s.values {
		s.dest[i] = &s.values[i]
	}

	return s
}

// Dest returns the scan destinations, to be passed to a driver's Scan.
func (s *TaskScanner) Dest() []any { return s.dest }

// ScanTasks reads every row of a result set into tasks. Candidate pools are not
// populated: they live in a child table and are read separately.
func ScanTasks(rows Rows) ([]hmntsk.Task, error) {
	scanner := NewTaskScanner()

	var tasks []hmntsk.Task

	for rows.Next() {
		if err := rows.Scan(scanner.Dest()...); err != nil {
			return nil, fmt.Errorf("sqlcore: scan task row: %w", err)
		}

		task, err := scanner.Task()
		if err != nil {
			return nil, err
		}

		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlcore: read task rows: %w", err)
	}

	return tasks, nil
}

// Task converts the most recently scanned row into a task.
func (s *TaskScanner) Task() (hmntsk.Task, error) {
	var (
		task hmntsk.Task
		err  error
	)

	read := func(index int, into func(value any) error) {
		if err != nil {
			return
		}

		if scanErr := into(s.values[index]); scanErr != nil {
			err = fmt.Errorf("sqlcore: column %s: %w", taskColumns[index], scanErr)
		}
	}

	readString := func(index int, target *string) {
		read(index, func(value any) error {
			decoded, decodeErr := DecodeString(value)
			*target = decoded

			return decodeErr
		})
	}

	readInt := func(index int, target *int64) {
		read(index, func(value any) error {
			decoded, decodeErr := DecodeInt(value)
			*target = decoded

			return decodeErr
		})
	}

	readRaw := func(index int, target *json.RawMessage) {
		read(index, func(value any) error {
			decoded, decodeErr := DecodeJSON(value)
			*target = decoded

			return decodeErr
		})
	}

	readTime := func(index int, target *time.Time) {
		read(index, func(value any) error {
			decoded, decodeErr := DecodeTime(value)
			*target = decoded

			return decodeErr
		})
	}

	var (
		id, taskType, status, suspendedFrom, assignee string
		ownerType, ownerRef, activityKey              string
		callbackAddr, reason, createdBy, lockedBy     string
		version, priority, escalationCount            int64
		correlationExtra, callbackParams, escalation  json.RawMessage
		due, started, closed, escalated, lockedUntil  time.Time
	)

	readString(0, &id)
	readString(1, &taskType)
	readInt(2, &version)
	readString(3, &status)
	readString(4, &suspendedFrom)
	readInt(5, &priority)
	readString(6, &assignee)
	readString(7, &ownerType)
	readString(8, &ownerRef)
	readString(9, &activityKey)
	readRaw(10, &correlationExtra)
	readString(11, &callbackAddr)
	readRaw(12, &callbackParams)
	readRaw(13, &escalation)
	readRaw(14, &task.Input)
	readRaw(15, &task.Progress)
	readRaw(16, &task.Output)
	readString(17, &reason)
	readString(18, &createdBy)
	readInt(19, &escalationCount)
	readTime(20, &task.CreatedAt)
	readTime(21, &task.UpdatedAt)
	readTime(22, &due)
	readTime(23, &started)
	readTime(24, &closed)
	readTime(25, &escalated)
	readString(26, &lockedBy)
	readTime(27, &lockedUntil)

	if err != nil {
		return hmntsk.Task{}, err
	}

	task.ID = hmntsk.TaskID(id)
	task.Type = taskType
	task.Version = version
	task.Status = hmntsk.Status(status)
	task.SuspendedFrom = hmntsk.Status(suspendedFrom)
	task.Priority = hmntsk.Priority(priority)
	task.Assignee = assignee
	task.Reason = reason
	task.CreatedBy = createdBy
	task.EscalationCount = int(escalationCount)
	task.LockedBy = lockedBy

	task.Correlation = hmntsk.CorrelationData{
		OwnerType: ownerType, OwnerRef: ownerRef, ActivityKey: activityKey,
	}

	if len(correlationExtra) > 0 {
		if unmarshalErr := json.Unmarshal(correlationExtra, &task.Correlation.Extra); unmarshalErr != nil {
			return hmntsk.Task{}, fmt.Errorf("sqlcore: column correlation_extra: %w", unmarshalErr)
		}
	}

	if callbackAddr != "" || len(callbackParams) > 0 {
		task.Callback = &hmntsk.CallbackTarget{
			Address: callbackAddr, ReferenceParameters: callbackParams,
		}
	}

	if len(escalation) > 0 {
		policy := &hmntsk.EscalationPolicy{}
		if unmarshalErr := json.Unmarshal(escalation, policy); unmarshalErr != nil {
			return hmntsk.Task{}, fmt.Errorf("sqlcore: column escalation: %w", unmarshalErr)
		}

		task.Escalation = policy
	}

	task.DueAt = optionalTime(due)
	task.StartedAt = optionalTime(started)
	task.ClosedAt = optionalTime(closed)
	task.EscalatedAt = optionalTime(escalated)
	task.LockedUntil = optionalTime(lockedUntil)

	return task, nil
}

// optionalTime maps the zero instant back to absence.
func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}

	out := value

	return &out
}

// CandidateRow is one row of the candidates child table.
type CandidateRow struct {
	// TaskID is the task the row belongs to.
	TaskID hmntsk.TaskID
	// Kind says whether the row names a candidate user, a candidate group or
	// an exclusion.
	Kind CandidateKind
	// Value is the actor or group identifier.
	Value string
}

// ScanCandidates reads candidate rows and groups them into pools by task.
func ScanCandidates(rows Rows) (map[hmntsk.TaskID]hmntsk.CandidatePool, error) {
	pools := make(map[hmntsk.TaskID]hmntsk.CandidatePool)

	var taskID, kind, value any

	for rows.Next() {
		if err := rows.Scan(&taskID, &kind, &value); err != nil {
			return nil, fmt.Errorf("sqlcore: scan candidate row: %w", err)
		}

		id, err := DecodeString(taskID)
		if err != nil {
			return nil, err
		}

		rowKind, err := DecodeString(kind)
		if err != nil {
			return nil, err
		}

		rowValue, err := DecodeString(value)
		if err != nil {
			return nil, err
		}

		pool := pools[hmntsk.TaskID(id)]

		switch CandidateKind(rowKind) {
		case CandidateUser:
			pool.Users = append(pool.Users, rowValue)
		case CandidateGroup:
			pool.Groups = append(pool.Groups, rowValue)
		case CandidateExcluded:
			pool.Excluded = append(pool.Excluded, rowValue)
		default:
			return nil, fmt.Errorf("sqlcore: unknown candidate kind %q", rowKind)
		}

		pools[hmntsk.TaskID(id)] = pool
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlcore: read candidate rows: %w", err)
	}

	return pools, nil
}

// ScanHistory reads transition records, in the order the statement returned
// them.
func ScanHistory(rows Rows) ([]hmntsk.TransitionRecord, error) {
	var records []hmntsk.TransitionRecord

	values := make([]any, len(historyColumns))
	dest := make([]any, len(historyColumns))

	for i := range values {
		dest[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("sqlcore: scan history row: %w", err)
		}

		record, err := historyRecord(values)
		if err != nil {
			return nil, err
		}

		records = append(records, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlcore: read history rows: %w", err)
	}

	return records, nil
}

// historyRecord converts one scanned history row.
func historyRecord(values []any) (hmntsk.TransitionRecord, error) {
	taskID, err := DecodeString(values[0])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	version, err := DecodeInt(values[1])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	operation, err := DecodeString(values[2])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	from, err := DecodeString(values[3])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	to, err := DecodeString(values[4])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	actor, err := DecodeString(values[5])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	comment, err := DecodeString(values[6])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	at, err := DecodeTime(values[7])
	if err != nil {
		return hmntsk.TransitionRecord{}, err
	}

	return hmntsk.TransitionRecord{
		TaskID:    hmntsk.TaskID(taskID),
		Version:   version,
		Operation: hmntsk.Operation(operation),
		From:      hmntsk.Status(from),
		To:        hmntsk.Status(to),
		Actor:     actor,
		Comment:   comment,
		At:        at,
	}, nil
}

// ScanOutbox reads durable event rows and decodes the events they carry.
func ScanOutbox(rows Rows) ([]hmntsk.Event, error) {
	var events []hmntsk.Event

	values := make([]any, len(outboxColumns))
	dest := make([]any, len(outboxColumns))

	for i := range values {
		dest[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("sqlcore: scan outbox row: %w", err)
		}

		payload, err := DecodeJSON(values[6])
		if err != nil {
			return nil, err
		}

		var event hmntsk.Event

		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("sqlcore: decode outbox payload: %w", err)
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlcore: read outbox rows: %w", err)
	}

	return events, nil
}

// EventRows encodes events for the outbox.
func EventRows(events []hmntsk.Event) ([]EventRow, error) {
	rows := make([]EventRow, 0, len(events))

	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("sqlcore: encode event %s: %w", event.ID, err)
		}

		rows = append(rows, EventRow{
			ID:         event.ID,
			TaskID:     event.TaskID,
			TaskType:   event.TaskType,
			EventType:  event.Type,
			OccurredAt: event.OccurredAt,
			Payload:    payload,
		})
	}

	return rows, nil
}

// ScanOutboxEntries reads outbox rows whole: the event, and the delivery state
// the relay reads and writes around it.
//
// It is the relay's counterpart to [ScanOutbox], which reads the events alone
// and is what a host polling the durable record wants.
func ScanOutboxEntries(rows Rows) ([]hmntsk.OutboxEntry, error) {
	var entries []hmntsk.OutboxEntry

	values := make([]any, len(outboxColumns))
	dest := make([]any, len(outboxColumns))

	for i := range values {
		dest[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("sqlcore: scan outbox row: %w", err)
		}

		entry, err := outboxEntry(values)
		if err != nil {
			return nil, err
		}

		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlcore: read outbox rows: %w", err)
	}

	return entries, nil
}

// outboxEntry converts one scanned outbox row into the relay's view of it.
func outboxEntry(values []any) (hmntsk.OutboxEntry, error) {
	var (
		entry hmntsk.OutboxEntry
		err   error
	)

	read := func(index int, into func(value any) error) {
		if err != nil {
			return
		}

		if scanErr := into(values[index]); scanErr != nil {
			err = fmt.Errorf("sqlcore: column %s: %w", outboxColumns[index], scanErr)
		}
	}

	var published, next, lockedUntil time.Time

	readTime := func(index int, target *time.Time) {
		read(index, func(value any) error {
			decoded, decodeErr := DecodeTime(value)
			*target = decoded

			return decodeErr
		})
	}

	readTime(5, &published)

	read(6, func(value any) error {
		payload, decodeErr := DecodeJSON(value)
		if decodeErr != nil {
			return decodeErr
		}

		if unmarshalErr := json.Unmarshal(payload, &entry.Event); unmarshalErr != nil {
			return fmt.Errorf("decode outbox payload: %w", unmarshalErr)
		}

		return nil
	})

	read(7, func(value any) error {
		attempts, decodeErr := DecodeInt(value)
		entry.Attempts = int(attempts)

		return decodeErr
	})

	readTime(8, &next)

	read(9, func(value any) error {
		decoded, decodeErr := DecodeString(value)
		entry.LastError = decoded

		return decodeErr
	})

	read(10, func(value any) error {
		decoded, decodeErr := DecodeString(value)
		entry.LockedBy = decoded

		return decodeErr
	})

	readTime(11, &lockedUntil)

	read(12, func(value any) error {
		decoded, decodeErr := decodeSinks(value)
		entry.Accepted = decoded

		return decodeErr
	})

	if err != nil {
		return hmntsk.OutboxEntry{}, err
	}

	entry.PublishedAt = optionalTime(published)
	entry.NextAttemptAt = optionalTime(next)
	entry.LockedUntil = optionalTime(lockedUntil)

	return entry, nil
}
