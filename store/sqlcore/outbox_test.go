package sqlcore_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// sampleClaim is the claim every due-events case is built from.
func sampleClaim() hmntsk.OutboxClaim {
	return hmntsk.OutboxClaim{Now: reference, Owner: "relay-1", Duration: time.Minute, Limit: 10}
}

// sampleEventRow is one durable event as the engine hands it to the outbox.
func sampleEventRow(id string) sqlcore.EventRow {
	return sqlcore.EventRow{
		ID:         id,
		TaskID:     "task-1",
		TaskType:   "approval",
		EventType:  hmntsk.EventTypeCompleted,
		OccurredAt: reference,
		Payload:    json.RawMessage(`{"id":"` + id + `","type":"task.completed"}`),
	}
}

// TestSelectDueEvents pins the read that replaces the single published-at poll.
//
// An event is due when it is neither delivered nor dead-lettered, its
// next-attempt time has passed, and no live lease is held on it. Every one of
// those has to be in the predicate, because each of them is a way of attempting
// an event that must not be attempted.
func TestSelectDueEvents(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		claim   hmntsk.OutboxClaim
		assert  func(t *testing.T, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name:    "postgres numbers its placeholders and may skip locked rows",
			dialect: sqlcore.PostgreSQL,
			claim:   sampleClaim(),
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `SELECT "id" FROM "task_outbox"`)
				assert.Contains(t, statement.SQL, `"published_at" IS NULL`)
				assert.Contains(t, statement.SQL, `"next_attempt_at" IS NOT NULL`)
				assert.Contains(t, statement.SQL, `"next_attempt_at" <= $1`)
				assert.Contains(t, statement.SQL,
					`("locked_until" IS NULL OR "locked_until" <= $2)`,
					"an expired lease is claimable again, which is what heals a crashed relay")
				assert.Contains(t, statement.SQL, `ORDER BY "occurred_at", "id"`)
				assert.Contains(t, statement.SQL, "LIMIT $3")
				assert.Contains(t, statement.SQL, "FOR UPDATE SKIP LOCKED")
				require.Len(t, statement.Args, 3)
				assert.Equal(t, int64(10), statement.Args[2])
			},
		},
		{
			name:    "mysql binds the same predicate positionally",
			dialect: sqlcore.MySQL,
			claim:   sampleClaim(),
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, "SELECT `id` FROM `task_outbox`")
				assert.Contains(t, statement.SQL, "`next_attempt_at` <= ?")
				assert.Equal(t, 3, strings.Count(statement.SQL, "?"))
				assert.Contains(t, statement.SQL, "FOR UPDATE SKIP LOCKED")
			},
		},
		{
			name:    "sqlite claims without any lock at all",
			dialect: sqlcore.SQLite,
			claim:   sampleClaim(),
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.NotContains(t, statement.SQL, "FOR UPDATE",
					"SQLite has no row-level locking, which is why the lease is the mechanism")
				assert.Equal(t, "2026-03-01T12:00:00.123456Z", statement.Args[0],
					"a dialect with no timestamp type compares the one fixed text encoding")
			},
		},
		{
			name:    "no limit falls back to the relay's default batch",
			dialect: sqlcore.PostgreSQL,
			claim:   hmntsk.OutboxClaim{Now: reference, Owner: "relay-1", Duration: time.Minute},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				require.Len(t, statement.Args, 3)
				assert.Equal(t, int64(hmntsk.DefaultOutboxBatch), statement.Args[2])
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, sqlcore.New(tc.dialect).SelectDueEvents(tc.claim))
		})
	}
}

// TestClaimEventRepeatsTheWholeDuePredicate is the guard on the one thing that
// makes claiming exclusive without a lock.
//
// The conditional update has to re-assert everything the selection asserted. If
// it trusted the selection instead, two relays that both read the same row
// before either wrote would both claim it, and the event would be delivered
// twice — on every dialect, not only the one without locks.
func TestClaimEventRepeatsTheWholeDuePredicate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		dialect sqlcore.Dialect
		assert  func(t *testing.T, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name:    "postgres",
			dialect: sqlcore.PostgreSQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `UPDATE "task_outbox" SET "locked_by" = $1`)
				assert.Contains(t, statement.SQL, `"locked_until" = $2`)
				assert.Contains(t, statement.SQL, `WHERE "id" = $3`)
				require.Len(t, statement.Args, 5,
					"the owner, the deadline, the identifier and the two the predicate compares")
				assert.Equal(t, "relay-1", statement.Args[0])
				assert.Equal(t, "e-1", statement.Args[2])
			},
		},
		{
			name:    "mysql",
			dialect: sqlcore.MySQL,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Equal(t, 5, strings.Count(statement.SQL, "?"))
			},
		},
		{
			name:    "sqlite",
			dialect: sqlcore.SQLite,
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Equal(t, 5, strings.Count(statement.SQL, "?"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := sqlcore.New(tc.dialect)
			statement := b.ClaimEvent("e-1", sampleClaim())

			quote := tc.dialect.Quote

			for _, condition := range []string{
				quote("published_at") + " IS NULL",
				quote("next_attempt_at") + " IS NOT NULL",
				quote("next_attempt_at") + " <=",
				quote("locked_until") + " IS NULL OR",
			} {
				assert.Containsf(t, statement.SQL, condition,
					"the claim must repeat the whole due predicate: %s", statement.SQL)
			}

			assert.NotContains(t, statement.SQL, "RETURNING",
				"a rows-affected count of zero is the whole signal, because MySQL has no RETURNING")

			tc.assert(t, statement)
		})
	}
}

// TestOutboxSettlementStatements pins the three writes that end an attempt.
//
// All three release the lease: whatever the outcome was, the event is not this
// relay's any more, and a settlement that forgot to release it would strand the
// event until the lease expired.
func TestOutboxSettlementStatements(t *testing.T) {
	t.Parallel()

	later := reference.Add(5 * time.Minute)

	type testCase struct {
		name   string
		build  func(b *sqlcore.Builder) sqlcore.Statement
		assert func(t *testing.T, statement sqlcore.Statement)
	}

	cases := []testCase{
		{
			name: "a retryable failure schedules the next attempt",
			build: func(b *sqlcore.Builder) sqlcore.Statement {
				return b.RecordAttempt(hmntsk.AttemptRecord{
					EventID: "e-1", Attempts: 2, NextAttemptAt: later, LastError: "503 from receiver",
				})
			},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `"attempts" = $1`)
				assert.Contains(t, statement.SQL, `"next_attempt_at" = $2`)
				assert.Contains(t, statement.SQL, `"last_error" = $3`)
				require.Len(t, statement.Args, 6)
				assert.Equal(t, int64(2), statement.Args[0])
				assert.Equal(t, later, statement.Args[1])
				assert.Equal(t, "503 from receiver", statement.Args[2])
				assert.Equal(t, "e-1", statement.Args[5])
			},
		},
		{
			name: "acceptance by every sink publishes the event",
			build: func(b *sqlcore.Builder) sqlcore.Statement {
				return b.MarkAccepted(hmntsk.Acceptance{
					EventID: "e-1", Accepted: []string{"webhook", "bus"},
					PublishedAt: &reference, Attempts: 1, LastError: "503 from receiver",
				})
			},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `"accepted_sinks" = $1`)
				assert.Equal(t, `["webhook","bus"]`, statement.Args[0])
				assert.Equal(t, int64(1), statement.Args[1])
				assert.Nil(t, statement.Args[2],
					"an event every sink took has no outstanding failure to report")
				assert.Equal(t, reference, statement.Args[3])
				assert.NotContains(t, statement.SQL, `"next_attempt_at" =`,
					"a delivered event keeps whatever next attempt it had; the published time is what settles it")
			},
		},
		{
			name: "partial acceptance keeps the event claimable",
			build: func(b *sqlcore.Builder) sqlcore.Statement {
				return b.MarkAccepted(hmntsk.Acceptance{
					EventID: "e-1", Accepted: []string{"webhook"},
					NextAttemptAt: &later, Attempts: 1, LastError: "bus unavailable",
				})
			},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Equal(t, `["webhook"]`, statement.Args[0])
				assert.Equal(t, "bus unavailable", statement.Args[2])
				assert.Nil(t, statement.Args[3], "an event one sink has not taken is not published")
				assert.Contains(t, statement.SQL, `"next_attempt_at" = $5`)
				assert.Equal(t, later, statement.Args[4])
			},
		},
		{
			name: "a dead letter has no next attempt and keeps its error",
			build: func(b *sqlcore.Builder) sqlcore.Statement {
				return b.MarkDeadLettered(hmntsk.DeadLetter{
					EventID: "e-1", Attempts: 5, LastError: "400 from receiver",
				})
			},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `"next_attempt_at" = $3`)
				require.Len(t, statement.Args, 6)
				assert.Equal(t, int64(5), statement.Args[0])
				assert.Equal(t, "400 from receiver", statement.Args[1])
				assert.Nil(t, statement.Args[2],
					"no next attempt and never published is what makes an entry a dead letter")
				assert.Equal(t, "e-1", statement.Args[5])
			},
		},
		{
			name: "a dead letter records the sinks that did take it",
			build: func(b *sqlcore.Builder) sqlcore.Statement {
				return b.MarkDeadLettered(hmntsk.DeadLetter{
					EventID: "e-1", Attempts: 5, LastError: "400 from receiver",
					Accepted: []string{"webhook"},
				})
			},
			assert: func(t *testing.T, statement sqlcore.Statement) {
				assert.Contains(t, statement.SQL, `"accepted_sinks" = $3`,
					"the acceptances are written by the same settlement that marks it dead")
				require.Len(t, statement.Args, 7)
				assert.Equal(t, `["webhook"]`, statement.Args[2],
					"a destination that received the event says so on the row")
				assert.Nil(t, statement.Args[3],
					"and the entry is still a dead letter: no next attempt")
				assert.Equal(t, "e-1", statement.Args[6])
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement := tc.build(sqlcore.New(sqlcore.PostgreSQL))

			assert.Contains(t, statement.SQL, `UPDATE "task_outbox" SET`)
			assert.Contains(t, statement.SQL, `"locked_by" = `)
			assert.Contains(t, statement.SQL, `"locked_until" = `)
			assert.Nil(t, statement.Args[len(statement.Args)-3], "every settlement releases the lease")
			assert.Nil(t, statement.Args[len(statement.Args)-2], "every settlement releases the lease")
			assert.Equal(t, "e-1", statement.Args[len(statement.Args)-1])

			tc.assert(t, statement)
		})
	}
}

// TestInsertOutboxWritesARowThatIsAlreadyDue proves the outbox exists so that
// delivery happens after the commit, not later than it.
func TestInsertOutboxWritesARowThatIsAlreadyDue(t *testing.T) {
	t.Parallel()

	statement := sqlcore.New(sqlcore.PostgreSQL).InsertOutbox([]sqlcore.EventRow{sampleEventRow("e-1")})

	require.Len(t, statement.Args, 13, "one argument per outbox column, in column order")
	assert.Equal(t, int64(0), statement.Args[7], "a new row has been attempted zero times")
	assert.Equal(t, statement.Args[4], statement.Args[8],
		"a recorded event is due as soon as it is recorded")
	assert.Nil(t, statement.Args[9], "nothing has failed yet")
	assert.Nil(t, statement.Args[10], "a new row is held by nobody")
	assert.Nil(t, statement.Args[11], "a new row is held by nobody")
	assert.Nil(t, statement.Args[12], "no sink has taken it yet")
}

// TestOutboxEntryRoundTripThroughTheDriverValues feeds the arguments an insert
// binds straight back through the entry scanner, and then the arguments each
// settlement binds over them.
//
// It is the cheapest proof that the encoding and decoding halves agree about
// the tri-state: delivered, pending, and dead-lettered with no column of its
// own.
func TestOutboxEntryRoundTripThroughTheDriverValues(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		row    func(inserted []any) []any
		assert func(t *testing.T, entry hmntsk.OutboxEntry)
	}

	// settle overwrites the columns a settlement statement assigns, so that a
	// case can describe a row as it stands after one.
	settle := func(inserted []any, mutate map[int]any) []any {
		row := append([]any(nil), inserted...)
		for index, value := range mutate {
			row[index] = value
		}

		return row
	}

	published := reference.Add(time.Minute)
	later := reference.Add(5 * time.Minute)

	cases := []testCase{
		{
			name: "a freshly recorded event is pending and due",
			row:  func(inserted []any) []any { return inserted },
			assert: func(t *testing.T, entry hmntsk.OutboxEntry) {
				assert.True(t, entry.Pending())
				assert.False(t, entry.Delivered())
				assert.False(t, entry.DeadLettered())
				assert.Equal(t, "e-1", entry.Event.ID)
				assert.Equal(t, 0, entry.Attempts)
				assert.Empty(t, entry.Accepted)
				require.NotNil(t, entry.NextAttemptAt)
				assert.True(t, reference.Equal(*entry.NextAttemptAt))
				assert.False(t, entry.IsLeased(reference))
			},
		},
		{
			name: "a leased event reports its holder until the lease expires",
			row: func(inserted []any) []any {
				return settle(inserted, map[int]any{10: "relay-1", 11: reference.Add(time.Minute)})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry) {
				assert.Equal(t, "relay-1", entry.LockedBy)
				assert.True(t, entry.IsLeased(reference))
				assert.False(t, entry.IsLeased(reference.Add(2*time.Minute)),
					"an expired lease is nobody's")
			},
		},
		{
			name: "an event every sink took is delivered",
			row: func(inserted []any) []any {
				return settle(inserted, map[int]any{
					5: published, 7: int64(1), 12: `["webhook","bus"]`,
				})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry) {
				assert.True(t, entry.Delivered())
				assert.False(t, entry.DeadLettered())
				assert.Equal(t, []string{"webhook", "bus"}, entry.Accepted)
				assert.True(t, entry.HasAccepted("bus"))
				assert.Equal(t, 1, entry.Attempts)
			},
		},
		{
			name: "an event one sink took is neither delivered nor dead",
			row: func(inserted []any) []any {
				return settle(inserted, map[int]any{
					7: int64(1), 8: later, 9: "bus unavailable", 12: `["webhook"]`,
				})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry) {
				assert.True(t, entry.Pending())
				assert.Equal(t, []string{"webhook"}, entry.Accepted)
				assert.False(t, entry.HasAccepted("bus"))
				assert.Equal(t, "bus unavailable", entry.LastError)
			},
		},
		{
			name: "an event with no next attempt and no publication is a dead letter",
			row: func(inserted []any) []any {
				return settle(inserted, map[int]any{7: int64(5), 8: nil, 9: "400 from receiver"})
			},
			assert: func(t *testing.T, entry hmntsk.OutboxEntry) {
				assert.True(t, entry.DeadLettered())
				assert.False(t, entry.Pending())
				assert.False(t, entry.Delivered())
				assert.Equal(t, 5, entry.Attempts, "a dead letter is retained with its attempt count")
				assert.Equal(t, "400 from receiver", entry.LastError)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inserted := sqlcore.New(sqlcore.PostgreSQL).
				InsertOutbox([]sqlcore.EventRow{sampleEventRow("e-1")}).Args

			entries, err := sqlcore.ScanOutboxEntries(&valueRows{rows: [][]any{tc.row(inserted)}})
			require.NoError(t, err)
			require.Len(t, entries, 1)

			tc.assert(t, entries[0])
		})
	}
}

// TestSelectOutboxEntryReadsOneRowWholeso that a dead letter is inspectable,
// which is the only thing the engine promises about one.
func TestSelectOutboxEntryReadsOneRowWhole(t *testing.T) {
	t.Parallel()

	b := sqlcore.New(sqlcore.PostgreSQL)
	statement := b.SelectOutboxEntry("e-1")

	for _, column := range b.OutboxColumns() {
		assert.Containsf(t, statement.SQL, `"`+column+`"`,
			"the entry read must carry %s, which the relay's view of the row includes", column)
	}

	assert.Contains(t, statement.SQL, `WHERE "id" = $1`)
	assert.Equal(t, []any{"e-1"}, statement.Args)
}
