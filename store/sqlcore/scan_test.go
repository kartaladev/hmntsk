package sqlcore_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// valueRows replays rows of already-decoded driver values, which is what the
// insert statements produced for them.
type valueRows struct {
	rows  [][]any
	index int
}

func (r *valueRows) Next() bool {
	r.index++

	return r.index <= len(r.rows)
}

func (r *valueRows) Scan(dest ...any) error {
	row := r.rows[r.index-1]
	for i, target := range dest {
		*target.(*any) = row[i] //nolint:errcheck,forcetypeassert // scanners always pass *any
	}

	return nil
}

func (r *valueRows) Err() error { return nil }

// TestTaskRoundTripThroughTheDriverValues feeds the arguments an insert would
// bind straight back through the scanner.
//
// It is the cheapest possible proof that the encoding and the decoding halves
// agree, and it catches the failures that are otherwise only visible against a
// live database: a timestamp written as text and read as a time, a JSON column
// written as a string and read as bytes, an empty string written as NULL and
// read back as something else.
func TestTaskRoundTripThroughTheDriverValues(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		task hmntsk.Task
	}

	cases := []testCase{
		{name: "a fully populated task", task: sampleTask()},
		{
			name: "a pooled task with nothing optional set",
			task: func() hmntsk.Task {
				task := sampleTask()
				task.Status = hmntsk.StatusReady
				task.Assignee = ""
				task.Callback = nil
				task.Escalation = nil
				task.Input = nil
				task.Correlation = hmntsk.CorrelationData{}
				task.DueAt = nil

				return task
			}(),
		},
		{
			name: "a suspended task",
			task: func() hmntsk.Task {
				task := sampleTask()
				task.Status = hmntsk.StatusSuspended
				task.SuspendedFrom = hmntsk.StatusInProgress
				started := reference.Add(-time.Hour)
				task.StartedAt = &started

				return task
			}(),
		},
		{
			name: "a leased, escalated task",
			task: func() hmntsk.Task {
				task := sampleTask()
				escalated := reference
				until := reference.Add(time.Minute)
				task.EscalationCount = 2
				task.EscalatedAt = &escalated
				task.LockedBy = "sweeper-1"
				task.LockedUntil = &until

				return task
			}(),
		},
		{
			name: "a closed task with an output",
			task: func() hmntsk.Task {
				task := sampleTask()
				task.Status = hmntsk.StatusCompleted
				closed := reference.Add(time.Hour)
				task.ClosedAt = &closed
				task.Output = []byte(`{"approved":false}`)
				task.Reason = ""

				return task
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, dialect := range sqlcore.Dialects() {
				t.Run(dialect.Name(), func(t *testing.T) {
					t.Parallel()

					statement := sqlcore.New(dialect).InsertTask(tc.task)

					tasks, err := sqlcore.ScanTasks(&valueRows{rows: [][]any{statement.Args}})
					require.NoError(t, err)
					require.Len(t, tasks, 1)

					got := tasks[0]
					want := tc.task.Normalize()

					// Candidate pools live in a child table and are read
					// separately, so the scanner never populates them.
					want.Candidates = hmntsk.CandidatePool{}

					assert.Equal(t, want.ID, got.ID)
					assert.Equal(t, want.Type, got.Type)
					assert.Equal(t, want.Version, got.Version)
					assert.Equal(t, want.Status, got.Status)
					assert.Equal(t, want.SuspendedFrom, got.SuspendedFrom)
					assert.Equal(t, want.Priority, got.Priority)
					assert.Equal(t, want.Assignee, got.Assignee)
					assert.Equal(t, want.Correlation, got.Correlation)
					assert.Equal(t, want.Callback, got.Callback)
					assert.Equal(t, want.Escalation, got.Escalation)
					assert.Equal(t, string(want.Input), string(got.Input))
					assert.Equal(t, string(want.Output), string(got.Output))
					assert.Equal(t, want.Reason, got.Reason)
					assert.Equal(t, want.CreatedBy, got.CreatedBy)
					assert.Equal(t, want.EscalationCount, got.EscalationCount)
					assert.Equal(t, want.LockedBy, got.LockedBy)

					assertSameInstant(t, "createdAt", &want.CreatedAt, &got.CreatedAt)
					assertSameInstant(t, "dueAt", want.DueAt, got.DueAt)
					assertSameInstant(t, "startedAt", want.StartedAt, got.StartedAt)
					assertSameInstant(t, "closedAt", want.ClosedAt, got.ClosedAt)
					assertSameInstant(t, "escalatedAt", want.EscalatedAt, got.EscalatedAt)
					assertSameInstant(t, "lockedUntil", want.LockedUntil, got.LockedUntil)
				})
			}
		})
	}
}

// assertSameInstant compares two optional instants, absence included.
func assertSameInstant(t *testing.T, field string, want, got *time.Time) {
	t.Helper()

	if want == nil {
		assert.Nilf(t, got, "%s must stay absent", field)

		return
	}

	require.NotNilf(t, got, "%s must survive the round trip", field)
	assert.Truef(t, want.Equal(*got), "%s: wrote %s, read %s", field, want, got)
	assert.Equalf(t, time.UTC, got.Location(), "%s must come back as UTC", field)
}

func TestScanCandidatesGroupsRowsIntoPools(t *testing.T) {
	t.Parallel()

	rows := &valueRows{rows: [][]any{
		{"task-1", "user", "alice"},
		{"task-1", "user", "bob"},
		{"task-1", "group", "finance-approvers"},
		{"task-1", "excluded", "mallory"},
		{"task-2", "group", "managers"},
	}}

	pools, err := sqlcore.ScanCandidates(rows)
	require.NoError(t, err)
	require.Len(t, pools, 2)

	assert.Equal(t, hmntsk.CandidatePool{
		Users:    []string{"alice", "bob"},
		Groups:   []string{"finance-approvers"},
		Excluded: []string{"mallory"},
	}, pools["task-1"])

	assert.Equal(t, hmntsk.CandidatePool{Groups: []string{"managers"}}, pools["task-2"])
}

func TestScanCandidatesRejectsAnUnknownKind(t *testing.T) {
	t.Parallel()

	_, err := sqlcore.ScanCandidates(&valueRows{rows: [][]any{{"task-1", "maybe", "alice"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maybe")
}

func TestHistoryRoundTrip(t *testing.T) {
	t.Parallel()

	record := hmntsk.TransitionRecord{
		TaskID: "task-1", Version: 2, Operation: hmntsk.OpClaim,
		From: hmntsk.StatusReady, To: hmntsk.StatusReserved,
		Actor: "alice", Comment: "mine", At: reference,
	}

	for _, dialect := range sqlcore.Dialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			t.Parallel()

			statement := sqlcore.New(dialect).InsertHistory(record)

			records, err := sqlcore.ScanHistory(&valueRows{rows: [][]any{statement.Args}})
			require.NoError(t, err)
			require.Len(t, records, 1)

			got := records[0]
			assert.Equal(t, record.TaskID, got.TaskID)
			assert.Equal(t, record.Version, got.Version)
			assert.Equal(t, record.Operation, got.Operation)
			assert.Equal(t, record.From, got.From)
			assert.Equal(t, record.To, got.To)
			assert.Equal(t, record.Actor, got.Actor)
			assert.Equal(t, record.Comment, got.Comment)
			assert.True(t, record.At.Equal(got.At))
		})
	}
}

func TestOutboxRoundTrip(t *testing.T) {
	t.Parallel()

	event := hmntsk.Event{
		ID: "event-1", Type: hmntsk.EventTypeCompleted, TaskID: "task-1",
		TaskType: "approval", Status: hmntsk.StatusCompleted, Version: 7,
		Actor: "alice", OccurredAt: reference,
		Correlation: hmntsk.CorrelationData{OwnerType: "process", OwnerRef: "p-1"},
		Output:      []byte(`{"approved":false,"amount":9007199254740993}`),
	}

	rows, err := sqlcore.EventRows([]hmntsk.Event{event})
	require.NoError(t, err)

	for _, dialect := range sqlcore.Dialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			t.Parallel()

			statement := sqlcore.New(dialect).InsertOutbox(rows)
			require.Len(t, statement.Args, 7)

			events, err := sqlcore.ScanOutbox(&valueRows{rows: [][]any{statement.Args}})
			require.NoError(t, err)
			require.Len(t, events, 1)

			got := events[0]
			assert.Equal(t, event.ID, got.ID)
			assert.Equal(t, event.Type, got.Type)
			assert.Equal(t, event.TaskID, got.TaskID)
			assert.Equal(t, event.Correlation, got.Correlation)
			assert.Equal(t, `{"approved":false,"amount":9007199254740993}`, string(got.Output),
				"a payload must survive the outbox unreinterpreted")
			assert.True(t, event.OccurredAt.Equal(got.OccurredAt))
		})
	}
}

func TestTypeRoundTrip(t *testing.T) {
	t.Parallel()

	spec := hmntsk.TypeSpec{
		Name:            "approval",
		Title:           "Approval",
		Description:     "Sign off on spending",
		InputSchema:     []byte(`{"type":"object"}`),
		OutputSchema:    []byte(`{"type":"object"}`),
		DefaultPriority: hmntsk.PriorityHighest,
		DefaultDeadline: 24 * time.Hour,
		DefaultEscalation: &hmntsk.EscalationPolicy{
			Action: hmntsk.EscalationWiden, AddGroups: []string{"managers"},
		},
		DefaultAssignment: hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
	}

	for _, dialect := range sqlcore.Dialects() {
		t.Run(dialect.Name(), func(t *testing.T) {
			t.Parallel()

			statement := sqlcore.New(dialect).UpsertType(spec, reference)

			specs, err := sqlcore.ScanTypes(&valueRows{rows: [][]any{statement.Args}})
			require.NoError(t, err)
			require.Len(t, specs, 1)

			got := specs[0]
			assert.Equal(t, spec.Name, got.Name)
			assert.Equal(t, spec.Title, got.Title)
			assert.Equal(t, spec.Description, got.Description)
			assert.JSONEq(t, string(spec.InputSchema), string(got.InputSchema))
			assert.Equal(t, spec.DefaultPriority, got.DefaultPriority)
			assert.Equal(t, spec.DefaultDeadline, got.DefaultDeadline)
			assert.Equal(t, spec.DefaultEscalation, got.DefaultEscalation)
			assert.Equal(t, spec.DefaultAssignment, got.DefaultAssignment)
			assert.True(t, spec.Equal(got), "a stored type must re-register as identical")
		})
	}
}

func TestDecodeTimeAcceptsWhatDriversActuallyReturn(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		value  any
		assert func(t *testing.T, instant time.Time, err error)
	}

	expected := time.Date(2026, time.March, 1, 12, 0, 0, 123456000, time.UTC)

	cases := []testCase{
		{
			name:  "a native instant",
			value: expected,
			assert: func(t *testing.T, instant time.Time, err error) {
				require.NoError(t, err)
				assert.True(t, expected.Equal(instant))
			},
		},
		{
			name:  "the engine's own text encoding",
			value: "2026-03-01T12:00:00.123456Z",
			assert: func(t *testing.T, instant time.Time, err error) {
				require.NoError(t, err)
				assert.True(t, expected.Equal(instant))
			},
		},
		{
			name:  "a driver's bytes",
			value: []byte("2026-03-01T12:00:00.123456Z"),
			assert: func(t *testing.T, instant time.Time, err error) {
				require.NoError(t, err)
				assert.True(t, expected.Equal(instant))
			},
		},
		{
			name:  "a space-separated encoding",
			value: "2026-03-01 12:00:00.123456",
			assert: func(t *testing.T, instant time.Time, err error) {
				require.NoError(t, err)
				assert.True(t, expected.Equal(instant))
			},
		},
		{
			name:  "NULL",
			value: nil,
			assert: func(t *testing.T, instant time.Time, err error) {
				require.NoError(t, err)
				assert.True(t, instant.IsZero())
			},
		},
		{
			name:  "something that is not a timestamp at all",
			value: 42,
			assert: func(t *testing.T, _ time.Time, err error) {
				require.Error(t, err)
			},
		},
		{
			name:  "text that is not a timestamp",
			value: "yesterday",
			assert: func(t *testing.T, _ time.Time, err error) {
				require.Error(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			instant, err := sqlcore.DecodeTime(tc.value)
			tc.assert(t, instant, err)
		})
	}
}
