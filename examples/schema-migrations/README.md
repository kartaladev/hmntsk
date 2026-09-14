# schema-migrations

How the engine's tables get into your database, and how you know they are right.
hmntsk never runs DDL for you in normal operation: it publishes the statements,
your migration step applies them, and a startup check tells you about anything
that does not match.

## What it shows

| Section | What you see |
| --- | --- |
| default | the published SQLite statements for the engine's five tables, applied by the host (not by `Store.Migrate`), then `VerifySchema` passing |
| default | transaction scopes nesting: two engine operations inside one `Store.Do` scope commit together, and a failure inside the scope rolls both back |
| override | `WithTablePrefix("acme_")`: the same schema under prefixed names, for a database that already has a `tasks` table, with a task created on it |
| override | a missing index, as a half-applied migration might leave: `VerifySchema` returns a `*sqlkit.SchemaError` listing every issue, matching `sqlkit.ErrSchemaMismatch` |

## Context

Every example uses the same made-up business: invoices that need reviewing and
approving, correlated to the invoice they are about. This one cares only about
where the tasks are stored: a SQLite file in a temporary directory, opened with
foreign keys, write-ahead logging and a busy timeout, as
[docs/schema.md](../../docs/schema.md#sqlite) requires.

In a real application you would write the published statements into your
migration tool's files once, per dialect, and call `VerifySchema` when the
application starts. The scenario applies them at run time only so that it is
self-contained.

## Left out

- PostgreSQL and MySQL, and the pgx and GORM stores: see
  [`store-drivers`](../store-drivers). The statements differ per dialect; the
  workflow does not.
- Upgrading an existing schema between engine versions: see
  [Upgrading an existing schema](../../docs/schema.md#upgrading-an-existing-schema).
- A host transaction that also writes its own rows: see
  [`correlated-tasks`](../correlated-tasks).

## Run it

From the `examples` directory:

```sh
go run ./schema-migrations
go test ./schema-migrations
```

No database server is needed: SQLite is embedded.

## Read more

- [Schema and migrations](../../docs/schema.md): the tables, the table prefix and the per-dialect workflow
- [The transaction contract](../../README.md#the-transaction-contract) in the repository README
