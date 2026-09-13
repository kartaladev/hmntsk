# Schema and migrations

The engine publishes the schema it needs, per dialect, and verifies the live one
at startup. It never applies DDL for you in normal operation.

Two reasons, and neither is squeamishness. Shipping a migration runner would
pick a migration tool on your behalf and fight the pipeline you already have.
Running DDL at startup is forbidden outright in plenty of organisations. What is
left — publish the statements, check the result — is the part a library can do
well.

## Getting the statements

```go
import "github.com/kartaladev/hmntsk/store/sqlcore"

// As a list, to feed a migration tool.
statements, err := sqlcore.Migrations(sqlcore.PostgreSQL)

// As one document, to paste into a migration file.
source, err := sqlcore.New(sqlcore.PostgreSQL).MigrationsSource()

// With a table prefix applied.
b := sqlcore.New(sqlcore.MySQL, sqlcore.WithTablePrefix("hmntsk_"))
prefixed, err := b.Migrations()
```

The published DDL lives in [`store/sqlcore/ddl`](../store/sqlcore/ddl):
`postgres.sql`, `mysql.sql` and `sqlite.sql`. They are embedded in the module, so
what you get from `Migrations` is the same text, with `{{PREFIX}}` substituted.

## Verifying it

```go
if err := store.VerifySchema(ctx); err != nil {
    log.Fatal(err)   // reports every discrepancy, not only the first
}
```

Verification checks that every table and column the engine's statements name is
present, and that the identifier columns carry the collation that keeps
comparison case-sensitive. It does not check column types: a dialect has several
spellings for the same storage, a type mismatch that matters shows up as a
failing statement immediately, and a wrong collation shows up months later as
the wrong person claiming a task.

Startup is the one moment the whole schema can be looked at at once, which is
why the error lists everything rather than failing on first use.

## The tables

| Table | What it holds |
| --- | --- |
| `tasks` | One row per task. Every filterable value is a column of its own |
| `task_candidates` | Candidate users, candidate groups and exclusions, one row each |
| `task_history` | One row per accepted lifecycle transition. Append-only |
| `task_outbox` | The durable event record, written in the same transaction as the change, plus the relay's delivery state for it |
| `task_types` | Registered task types, for hosts and inboxes that are not written in Go |

### The outbox carries its own delivery state

`task_outbox` holds the event — `id`, `task_id`, `task_type`, `event_type`,
`occurred_at`, `payload` — and, beside it, six columns the relay reads and
writes as it delivers:

| Column | What it is |
| --- | --- |
| `attempts` | How many delivery attempts have been made |
| `next_attempt_at` | When the event becomes due again. Null once it is dead-lettered |
| `last_error` | What the most recent failure was, for whoever asks why an event never arrived |
| `locked_by` | The relay currently holding the delivery lease |
| `locked_until` | When that lease expires |
| `accepted_sinks` | The sinks that have already taken the event, as a JSON array of names |

A row is born due: `InsertOutbox` writes `attempts` 0 and `next_attempt_at`
equal to `occurred_at`, so an event is deliverable the moment its transaction
commits.

There is no `dead_lettered_at`. The three conditions an event can be in are
distinguished by the two nullable timestamps that have to exist anyway:

| | `published_at` | `next_attempt_at` |
| --- | --- | --- |
| delivered | set | — |
| pending | null | set |
| dead-lettered | null | null |

A dead letter is the row that will never be attempted again and never was
delivered, which is exactly what "no next attempt, never published" says. A
marker column of its own would add a fourth state the other three could
contradict: a row both dead-lettered and due is representable with one and
unrepresentable without it.

`accepted_sinks` exists because a single `published_at` is only correct for a
single destination. With two, a webhook success followed by a broker outage
would either re-POST to the webhook on every retry or mark the event delivered
with the broker never having seen it. A retry targets only the sinks absent from
this list, and `published_at` is set only once every configured sink appears in
it.

Renaming a sink therefore re-delivers every event that sink has already taken,
because the name is what was written. Sink names are part of the schema's
meaning, not a label.

Two indexes serve the table. `task_outbox_unpublished_idx` on `(published_at,
occurred_at, id)` serves reading the undelivered record; `task_outbox_due_idx`
on `(published_at, next_attempt_at, occurred_at, id)` serves the relay's claim,
which filters on due-ness and lease state and drains oldest first.

### Why candidates are a child table

"The tasks Alice may claim" has to be answered from an index. None of PostgreSQL
arrays, MySQL JSON columns and SQLite can be indexed portably for that, and a
child table can be indexed identically on all three. The index that serves it is
`task_candidates_lookup_idx` on `(kind, value, task_id)`.

### Why payloads are text columns

`input`, `progress`, `output` and the callback's reference parameters are stored
as text, not as `jsonb` or MySQL `JSON`. Both native types normalise what they
are given: they reorder object keys, drop insignificant whitespace and rewrite
number literals. The engine promises a payload comes back exactly as it was
supplied — field order, number formatting and fields no schema describes
included — and nothing in the engine ever queries inside a payload, so the
native types were buying nothing to set against that.

### Timestamps

Stored as UTC at microsecond precision.

| | Column type | Note |
| --- | --- | --- |
| PostgreSQL | `timestamptz(6)` | |
| MySQL | `DATETIME(6)` | Not `TIMESTAMP`: it converts through the session zone and runs out of range in 2038 |
| SQLite | `TEXT` | One fixed encoding, `2006-01-02T15:04:05.000000Z`, so lexical order is chronological order |

### Collation

Identifier columns — actor, group, task type, status, task id — carry a pinned
collation in all three schemas, for two different reasons.

On **MySQL** it is about equality. The server default,
`utf8mb4_0900_ai_ci`, is case-insensitive, so without the pin an actor called
`alice` would match a candidate called `Alice`: the set of people who may claim
a task would quietly differ on one dialect out of three. The schema pins
`utf8mb4_0900_as_cs`.

On **PostgreSQL** it is about ordering. Equality is already case-sensitive, but
a locale-aware collation can sort `a-b` before `ab`, and task identifiers contain
hyphens — which would make keyset pagination skip and repeat rows. The schema
pins `C`.

On **SQLite**, `BINARY` is the default and is already right; it is written out
anyway so that the three schemas say the same thing in the same place.

## The table prefix

Every table the engine owns can carry a prefix, so that it can be embedded in a
database that already uses those names.

```go
b := sqlcore.New(sqlcore.PostgreSQL, sqlcore.WithTablePrefix("hmntsk_"))
store := sqlstore.New(db, sqlcore.PostgreSQL, sqlstore.WithTablePrefix("hmntsk_"))
```

The prefix reaches the DDL, every statement, every index name and every foreign
key. It has to match what the schema was created with, and `VerifySchema` says
so when it does not.

It is free to choose on day one and a breaking change afterwards, so choose it
then. The conformance suite runs every case against a real engine under its own
prefix, which is how the option stays as well exercised as the default.

## Per-dialect workflow

### PostgreSQL

```sh
go run github.com/kartaladev/hmntsk/store/sqlcore/cmd/hmntsk-schema \
    -dialect postgres > migrations/0001_hmntsk.up.sql
```

Or, with any migration tool that takes statements from Go, feed it
`sqlcore.Migrations(sqlcore.PostgreSQL)` directly. Requires 9.5 or later.

### MySQL

Requires 8.0 or later, for the `utf8mb4_0900_as_cs` collation. Apply the
statements in order; the `tasks` table has to exist before the tables whose
foreign keys reference it, and `Migrations` already returns them that way.

Set `clientFoundRows=true` on the DSN. Without it MySQL reports how many rows a
write *changed* rather than how many it *matched*, and a conditional update that
matched its row but wrote identical values would look like a conflict.

Set `parseTime=true&loc=UTC` too, so that timestamps come back as instants in
the zone the engine stores them in.

### SQLite

Requires 3.35 or later. Switch foreign keys on explicitly — SQLite leaves them
off by default and the schema relies on them:

```
file:tasks.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)
```

## Development runner

`Migrate` applies the published statements and exists for tests and local
development. It has no versioning, no down direction and no locking, and a
production deployment should never call it.

```go
if err := store.Migrate(ctx); err != nil {   // tests and development only
    log.Fatal(err)
}
```
