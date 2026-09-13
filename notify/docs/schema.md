# The notification store schema

`notify/sqlstore` stores notifications in two tables, on PostgreSQL, MySQL or
SQLite, through any of the three sqlkit executors: `database/sql`
(`sqlkit/stdsql`), pgx (`sqlkit/pgx`) or GORM (`sqlkit/gorm`). One store
implementation serves all of them, proven identical by one conformance suite on
all seven driver-and-dialect combinations.

## Where the DDL lives

The schema is published per dialect, for a host to apply through its own
migration pipeline:

| Dialect | Document |
| --- | --- |
| PostgreSQL | `notify/sqlstore/ddl/postgres.sql` |
| MySQL 8.0+ | `notify/sqlstore/ddl/mysql.sql` |
| SQLite 3.35+ | `notify/sqlstore/ddl/sqlite.sql` |

The documents are the source of truth, and each explains its dialect's choices in
comments. `Store.Schema()` returns the store's document with its table prefix
applied, ready to hand to a migration tool. Every statement is `CREATE ... IF NOT
EXISTS`, so applying it to an existing schema changes nothing.

Nothing in normal operation changes the schema. `Store.Migrate(ctx)` applies it
directly, and exists for tests and development only.

## Tables

### `notify_notifications`

One row per notification.

| Column | Holds | Notes |
| --- | --- | --- |
| `id` | the notification's identifier | primary key; UUIDv7 by default |
| `recipient` | who it is for | identifier |
| `source_id` | what produced it | identifier; unique with `recipient` |
| `subject` | what it is about | identifier |
| `subject_version` | the subject's version it describes | integer |
| `kind` | the publisher's classification | identifier |
| `state` | `ACTIVE`, `READ` or `CLOSED` | identifier |
| `closed_reason` | why it was closed | nullable |
| `title` | a line a client can show | nullable text |
| `links` | hrefs by relation name, as a JSON object | nullable text |
| `data` | the publisher's JSON payload, byte for byte | nullable text |
| `created_at` | when it was published | timestamp |
| `read_at` | when it was first read | nullable timestamp |
| `closed_at` | when it was closed | nullable timestamp |
| `inactive_at` | when it first left `ACTIVE`; the age bound measures from it | nullable timestamp |

### `notify_watermarks`

A subject's close records: for each subject and kind, the highest version its
notifications were closed at, and under kind `*` the highest version every kind
was closed at. A publish below the record for its kind is suppressed, which is
what stops a retried older source reopening a closed subject.

Publishing and closing a subject both lock its `*` row first. On PostgreSQL and
MySQL that row lock, held to commit, serialises every write to one subject
without `SELECT ... FOR UPDATE`; SQLite has one writer.

| Column | Holds |
| --- | --- |
| `subject` | the subject; primary key with `kind` |
| `kind` | the kind, or `*` for every kind |
| `version` | the highest version closed; `-1` for a row only a publish created |
| `updated_at` | when the record last changed; watermark retention measures from it |

## Indexes

Each index serves a statement the store runs. A missing one does not make that
statement fail; it makes it read the whole table.

| Index | Columns | Serves |
| --- | --- | --- |
| `notify_notifications_source_key` (unique) | `source_id`, `recipient` | idempotent publishing |
| `notify_notifications_recipient_idx` | `recipient`, `created_at`, `id` | a recipient's listing and its keyset paging |
| `notify_notifications_state_idx` | `recipient`, `state` | counting active notifications; choosing what the count bound deletes |
| `notify_notifications_subject_idx` | `subject`, `kind`, `state` | closing by subject and kind; coalescing |
| `notify_notifications_inactive_idx` | `state`, `inactive_at` | the age bound |
| `notify_watermarks_updated_idx` | `updated_at` | expiring close records |

## Dialect choices

| | PostgreSQL | MySQL | SQLite |
| --- | --- | --- | --- |
| Identifier columns | `text COLLATE "C"` | `VARCHAR COLLATE utf8mb4_0900_as_cs` | `TEXT COLLATE BINARY` |
| Timestamps | `timestamptz(6)` | `DATETIME(6)` | `TEXT`, RFC 3339, six fractional digits |
| `links`, `data` | `text` | `LONGTEXT` | `TEXT` |
| Indexes declared | `CREATE INDEX IF NOT EXISTS` | inside `CREATE TABLE` | `CREATE INDEX IF NOT EXISTS` |

- **Identifiers compare case-sensitively and sort in byte order** on every
  dialect. MySQL's default collation is case-insensitive, which would deliver
  `alice`'s notifications to `Alice`, and a locale-aware collation can sort
  identifiers so that keyset paging skips or repeats rows.
- **Payloads are text, never a native JSON type.** PostgreSQL's `jsonb` and
  MySQL's `JSON` reorder keys and rewrite number literals, and the store returns a
  payload exactly as it was published.

## Table prefix

`sqlstore.WithTablePrefix(prefix)` prefixes every table and index name, so the
store can share a database whose names would otherwise collide. The default is no
prefix.

- A prefix may contain only letters, digits and underscores; anything else is a
  configuration error from `sqlstore.New`.
- PostgreSQL truncates identifiers at 63 bytes and MySQL refuses names over 64.
  The longest index name is `notify_notifications_recipient_idx`, 34 bytes, so
  keep a prefix to 29 bytes or fewer.

## Verifying the schema at startup

`Store.VerifySchema(ctx)` compares the live database with what the store's
statements require: both tables, every column, the collation of every identifier
column, and every index above. It reports every discrepancy at once, as a
`*sqlkit.SchemaError` matching `sqlkit.ErrSchemaMismatch`, rather than failing on
first use. Call it when the host starts.

```go
executor, err := stdsqlexec.New(db, sqlkit.PostgreSQL)
if err != nil {
    return err
}

store, err := sqlstore.New(executor, sqlstore.WithTablePrefix("app_"))
if err != nil {
    return err
}

if err := store.VerifySchema(ctx); err != nil {
    return err // lists every missing table, column, collation and index
}
```

## Transactions

Every store method runs in a transaction of its own. A notification write never
joins a transaction the caller holds for other data, even one opened through the
same executor: a notification published while the caller's transaction is open
survives that transaction's rollback. Publishers deliver at least once and
publishing is idempotent, so a shared transaction would buy nothing.

On SQLite, a caller holding its own write transaction blocks the store's until
the busy timeout, because SQLite has one writer. Publish after committing.

## Rolling back

The schema is additive. To remove it, stop running the pruner, hub and handlers,
then drop `notify_notifications` and `notify_watermarks`. Nothing else depends on
them.
