# sqlkit

sqlkit is SQL support that knows nothing about any domain. A store written once
against it runs natively on PostgreSQL, MySQL and SQLite, through database/sql,
pgx or GORM, and behaves identically on all seven combinations.

It owns what varies by database and nothing that varies by application:

- the three dialects and the questions a store asks of them;
- statements, their bind markers and a writer that numbers them;
- the codecs that make every driver read back the same values;
- schema rendering, a development runner and verification against an
  expectation the store supplies;
- the `Executor` contract, with one executor per driver.

The tables and statements belong to the store built on it. sqlkit never names
one.

## Modules

| Module | Package | What it holds |
| --- | --- | --- |
| `sqlkit` | `sqlkit` | dialects, `Statement` and `Writer`, `Rows`, codecs, schema rendering and runner, verification, `Executor`, errors; standard library only |
| `sqlkit/stdsql` | `stdsqlexec` | `Executor` over `*sql.DB` |
| `sqlkit/pgx` | `pgxexec` | `Executor` over `*pgxpool.Pool` |
| `sqlkit/gorm` | `gormexec` | `Executor` over `*gorm.DB` |
| `sqlkit/sqlkittest` | `sqlkittest` | the executor conformance suite, a fixture schema per dialect, and container helpers |

`sqlkittest` is its own module so that `sqlkit` never pulls in testcontainers.

## Executors

Pick the module for the driver you already use.

```go
db, _ := sql.Open("pgx", dsn)
executor, err := stdsqlexec.New(db, sqlkit.PostgreSQL)
```

```go
executor, err := pgxexec.New(pool) // always PostgreSQL
```

```go
executor, err := gormexec.New(gormDB, sqlkit.MySQL)
```

A nil handle or dialect is a configuration error from `New`, matching
`sqlkit.ErrConfiguration`. Nothing connects at construction.

Build every statement with the executor's own dialect:

```go
w := sqlkit.NewWriter(executor.Dialect())
w.Write("SELECT id FROM widgets WHERE owner = ", w.Bind(owner))

err := executor.Query(ctx, w.Done(), func(rows sqlkit.Rows) error {
    for rows.Next() {
        // scan into any, then decode with sqlkit.DecodeString and friends
    }
    return nil
})
```

**Default:** a store uses one of the three executors.
**Override:** any type satisfying `sqlkit.Executor`, `sqlkit.Execer` and
`sqlkit.Querier` works with every sqlkit-based store. Prove it with
`sqlkittest.RunExecutorSuite`.

## Transactions

`Executor.Do` runs a function in a transaction, with the same rules on every
driver:

- **Whoever begins, commits.** When the context already carries a transaction
  for this executor, `Do` joins it and neither commits nor rolls back.
- **Nested scopes join and flatten**, never a savepoint. An inner failure aborts
  the whole scope once the error reaches the outermost `Do`. The GORM executor
  never calls `db.Transaction`, whose savepoints would break this.
- **An error, a panic or a cancelled context rolls back.** A panic is re-raised
  unchanged.

A caller that began a transaction itself hands it over with the executor
package's `ContextWithTx`, and reads it back with `TxFromContext`.

**Limit, stated:** each executor keeps its transaction under its own context
key, separate from any other library's. A transaction a task store carries is
not visible to a sqlkit executor. A host that wants one transaction across both
reads it from the store's context and passes it on explicitly:

```go
if tx, ok := sqlstore.TxFromContext(ctx); ok {
    ctx = stdsqlexec.ContextWithTx(ctx, tx)
}
```

## Queries take a callback

`Executor.Query` hands rows to a function and releases them afterwards, checking
their error, whether the function succeeded or not. A store cannot leak a
connection by forgetting `rows.Close()`, which returning rows would allow on
every driver.

**Limit, stated:** a query cannot hand its rows out past the callback. Collect
what you need inside it.

## Values

Every value a store writes reads back the same on every combination:

- **Instants** are UTC, truncated to the microsecond (`sqlkit.NormalizeTime`).
  `EncodeTime` writes a native timestamp where the dialect has one, and the
  fixed-width `sqlkit.TimestampLayout` text where it does not, so ordering by
  the column is chronological on SQLite too.
- **JSON payloads** are text columns, never `jsonb` or MySQL `JSON`, which
  reorder keys and rewrite numbers. `EncodeRaw` and `DecodeJSON` keep them byte
  for byte.
- **Identifiers** compare case-sensitively, because every dialect pins an
  identifier collation (`C`, `utf8mb4_0900_as_cs`, `BINARY`). A store's schema
  applies it to its identifier columns.

## Schemas

A store publishes one schema document per dialect, carrying
`sqlkit.PrefixToken` wherever a table prefix goes.

- `RenderSchema(document, prefix)` applies a prefix and splits the document
  into statements.
- `ApplySchema` runs them in order, stopping at the first failure and naming it.
- `DropTables` removes tables in reverse order, cascading on PostgreSQL.
- `VerifySchema(ctx, querier, dialect, prefix, expectation)` compares the live
  schema against a `SchemaExpectation` and reports every missing table, missing
  column, wrong identifier collation and missing index in one `*SchemaError`,
  which matches `sqlkit.ErrSchemaMismatch` and `sqlkit.ErrConfiguration`.

**Limit, stated:** `ApplySchema` and `DropTables` are for tests and development.
There is no migration tool: no versioning, no down direction and no locking.
Production applies the published schema through its own pipeline, and runs
`VerifySchema` at startup.

**Limit, stated:** verification checks tables, columns, identifier collations
and indexes by name. It does not check column types.

## Testing a store or an executor

`sqlkittest.RunTestPostgres`, `RunTestMySQL` and `RunTestSQLite` provision a
database and return a DSN, which every driver can use. Images are pinned.

`sqlkittest.RunExecutorSuite(t, factory)` runs the conformance suite against an
executor: statements and row release, transactions and nesting, rollback on
error, panic and cancellation, value round trips, and schema verification on a
live database. One database serves the whole suite, with a fresh table prefix
per case.

## Where sqlkit is going

sqlkit is developed inside the hmntsk repository and moves to
`github.com/kartaladev/sqlkit` before its first tag. Nothing under `sqlkit/`
imports anything outside it, and `make split-check` fails the build if that
ever changes.
