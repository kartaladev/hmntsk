## Context

See `proposal.md` for why. This design covers how `store/sqlcore` is cut in two, what the new executor contract looks like, and how the eventual split is protected. The current state that shapes it:

- **`store/sqlcore` mixes generic and task code.** `dialect.go` (231 lines) and `migrations.go` (146) import nothing from hmntsk. `verify.go` uses hmntsk only for `SchemaError.Unwrap` returning `hmntsk.ErrConfiguration`, but its expectations (`identifierColumns`, `expectedColumns`, `expectedIndexes`) are task tables. `encode.go` uses `hmntsk.NormalizeTime`, and `encodeValue` handles `*hmntsk.EscalationPolicy`. `builder.go`, `query.go`, `scan.go` and `types.go` are task statements and scanners.
- **Cross-module use (gopls references).**
  - `sqlcore.Dialect` is used by `store/sql`, `store/gorm` (including `questionMarks`) and their tests; `DialectByName` by `cmd/hmntsk-schema`.
  - `sqlcore.Rows` is used by all three adapters.
  - `sqlcore.Statement` is used by all three adapters and `store/sql/plan_test.go`.
  - `CheckAffected` (task-specific) is used by all three adapters.
  - `Execer`, `Querier`, `DecodeTime`, `TimestampLayout`, `DecodeJSON` and `SchemaError` have no users outside sqlcore.
- **The three adapters each repeat about 190 lines of plumbing:** a private `contextKey{}`, exported `ContextWithTx`/`TxFromContext`, `InTransaction`, and `Do` (join-and-flatten, rollback on error or panic, rollback on cancelled context). The rest is private `handle`, `exec`, `query` plus `ExecStatement`/`QueryStatement`, which make them `sqlcore.Execer`/`Querier`. pgx rolls back on a detached context. GORM refuses `db.Transaction` (it nests with SAVEPOINT) and wraps the dialect in `questionMarks` because GORM rewrites `?` itself.
- **`storetest/testutils.go`** owns the container helpers (`RunTestPostgres`, `RunTestMySQL`, `RunTestSQLite`, pinned `PostgresImage`/`MySQLImage`, returning DSNs). `storetest` imports hmntsk.
- **Tooling:**
  - The `Makefile` has one `MODULES` list, a `RELEASE_ORDER` that `docs_test.go` asserts against `docs/releasing.md`, and `STORE_MATRIX`/`RELAY_MATRIX`.
  - `.golangci.yml` has no `depguard`.
  - Satellite `go.mod` files don't name workspace modules (go.work supplies them).
  - Untagged packages break `go mod tidy`.
- **Nothing is tagged**, so moving public symbols breaks no released consumer.

## Goals / Non-Goals

**Goals:**
- A domain-free `sqlkit` holding everything a second store needs to be portable: dialects, statement writing, the time and JSON codecs, schema rendering, the development migration runner, and verification against a caller-supplied expectation.
- One `Executor` contract with native executors for `database/sql`, pgx and GORM, proven identical by one conformance suite on seven combinations.
- Task stores behave exactly as today: `storetest`, `store-matrix` and `relay-matrix` stay green without changes to their cases.
- Split guardrails that fail the build, not just a convention.

**Non-Goals:**
- Moving `store/sql`, `store/pgx` or `store/gorm` onto the executors. That is a later option; they keep their own plumbing.
- A migration tool: no versioning, no down direction, no locking. That is unchanged from sqlcore.
- Any notification table or query; those belong to `notify-core`.
- Moving `sqlkit` to its own repository. That happens before its first tag (decision 9), not in this change.

## Decisions

### 1. Module layout and package names

| Module path | Package | Contents | Imports |
|---|---|---|---|
| `github.com/kartaladev/hmntsk/sqlkit` | `sqlkit` | dialects, `Statement` and writer, `Rows`, codecs, schema rendering and runner, verification, `Executor`, errors | stdlib only |
| `…/sqlkit/sqlkittest` | `sqlkittest` | executor conformance suite, fixture schema per dialect, container helpers | `sqlkit`, testify, testcontainers |
| `…/sqlkit/stdsql` | `stdsqlexec` | `Executor` over `*sql.DB` | `sqlkit` (+ drivers and `sqlkittest` in tests) |
| `…/sqlkit/pgx` | `pgxexec` | `Executor` over `*pgxpool.Pool` | `sqlkit`, pgx/v5 |
| `…/sqlkit/gorm` | `gormexec` | `Executor` over `*gorm.DB` | `sqlkit`, gorm |

**Package names** follow the repository convention that a package name is its directory plus a role suffix (`store/pgx` is `pgxstore`), which avoids shadowing `pgx` and `gorm`.

**`sqlkittest` is its own module** so `sqlkit` never pulls in testcontainers. This makes five modules, not the four the proposal counted.

- **Default:** hosts use one executor module for their driver.
- **Override:** any type satisfying `sqlkit.Executor` works with every sqlkit-based store.

### 2. The `Executor` contract

```go
package sqlkit

type Statement struct {
    SQL  string
    Args []any
}
func (s Statement) IsZero() bool

type Rows interface {
    Next() bool
    Scan(dest ...any) error
    Err() error
}

type Executor interface {
    // Dialect is the dialect statements for this executor must be built with,
    // bind marker included (GORM's executor reports "?" on every database).
    Dialect() Dialect
    // Exec runs a write and returns the rows it matched. A zero Statement is a
    // no-op returning 0.
    Exec(ctx context.Context, statement Statement) (int64, error)
    // Query runs a read and hands its rows to scan. The executor closes the rows
    // and checks Err after scan returns, on success and failure alike.
    Query(ctx context.Context, statement Statement, scan func(rows Rows) error) error
    // Do runs fn in a transaction: joins one active on ctx, otherwise begins,
    // commits on success and rolls back on error, panic (re-raised) or a
    // cancelled ctx.
    Do(ctx context.Context, fn func(ctx context.Context) error) error
    // InTransaction reports whether ctx carries an active transaction.
    InTransaction(ctx context.Context) bool
}
```

Every executor also implements `sqlkit.Execer` (`ExecStatement(ctx, sql, args...) error`) and `sqlkit.Querier` (`QueryStatement(ctx, sql, args...) (Rows, error)`), so the development runner and verification can drive it.

**Why Query takes a callback.** Alternatives were returning `Rows` without a close method (leaks) or returning a `Rows` with `Close() error`. pgx's `Rows.Close()` returns nothing, so every adapter would wrap it, and every caller would need `defer rows.Close()` plus an `Err` check. The callback makes the release and the `Err` check impossible to forget, matching the "rows released after a failed scan" requirement.

**Why `Dialect()` is on the executor.** Before, the GORM adapter silently swapped in `questionMarks`. Asking the executor for the dialect makes a wrong bind marker unrepresentable: a store builds statements with `executor.Dialect()`, never a dialect it was handed separately.

**Execution errors** are wrapped as `sqlkit: <cause>\nstatement: <sql>`, keeping sqlcore's diagnostic shape.

### 3. Transaction helpers per executor

| Package | Constructor | Helpers |
|---|---|---|
| `stdsqlexec` | `New(db *sql.DB, dialect sqlkit.Dialect) (*Executor, error)` | `ContextWithTx(ctx, *sql.Tx) context.Context`, `TxFromContext(ctx) (*sql.Tx, bool)` |
| `pgxexec` | `New(pool *pgxpool.Pool) (*Executor, error)` (always `sqlkit.PostgreSQL`) | `ContextWithTx(ctx, pgx.Tx)`, `TxFromContext(ctx) (pgx.Tx, bool)` |
| `gormexec` | `New(db *gorm.DB, dialect sqlkit.Dialect) (*Executor, error)` | `ContextWithTx(ctx, *gorm.DB)`, `TxFromContext(ctx) (*gorm.DB, bool)` |

- **Nil arguments:** a nil handle or nil dialect is refused with a configuration error. The constructors return `(*Executor, error)`, unlike `store/*`'s `New`, which predates the construction-time rule.
- **Separate context keys:** each executor keeps its transaction under its own private key, distinct from `store/*`'s.
  - **Default:** a transaction begun by `sqlstore.Store.Do` is not visible to `stdsqlexec`.
  - **Override:** a host that wants one transaction across both reads it with `sqlstore.TxFromContext` and passes it to `stdsqlexec.ContextWithTx`. This is documented.
- **Behaviour carried over:** pgx rolls back on `context.WithoutCancel(ctx)`, and GORM keeps the flattening `Do` that never calls `db.Transaction`.

### 4. What moves and what stays

| Moves to `sqlkit` | Stays in `sqlcore` (task-specific) |
|---|---|
| `Dialect`, `PostgreSQL`, `MySQL`, `SQLite`, `Dialects`, `DialectByName`, `onConflictSuffix`, `quoteWith` | table and column constants, `CandidateKind`, `taskColumns` etc. |
| `Statement` (+`IsZero`); the private `stmt` writer becomes exported `sqlkit.Writer` (`NewWriter(dialect)`, `Write`, `Bind`, `BindAll`, `Done`) | `Builder` and all task, history, outbox, type and query statements |
| `Rows` | `TaskScanner`, `Scan*`, `EventRows`, `CandidateRow` |
| `TimestampLayout`, `DecodeTime`, `DecodeJSON`, `DecodeString`, `DecodeInt`, `EncodeTime` (from `encodeTime`), `EncodeRaw`, `NormalizeTime` | `encodeValue` (`EscalationPolicy`), `encodeSinks`/`decodeSinks` |
| `SplitStatements`, `RenderSchema(document, prefix)`, `PrefixToken`, `ApplySchema(ctx, Execer, dialect, statements)`, `DropTables(ctx, Execer, dialect, tables)`, `Execer`, `Querier` | embedded `ddl/*.sql`, `Builder.Migrations/MigrationsSource/Migrate/Drop` (now thin wrappers), `cmd/hmntsk-schema` |
| `SchemaExpectation`, `TableExpectation{Columns, IdentifierColumns, Indexes}`, `VerifySchema(ctx, Querier, dialect, prefix, expectation) error`, `SchemaIssue`, `SchemaError`, `ErrSchemaMismatch`, `ErrConfiguration`, `ConfigurationError` | `identifierColumns`, `expectedColumns`, `expectedIndexes` (become sqlcore's `SchemaExpectation`) |
| `TrimSQL` | `ConflictError`, `CheckAffected` (they build hmntsk errors) |

**Compatibility inside sqlcore.** `store/*` needs no change beyond what it already imports. sqlcore re-exports with type aliases (`type Dialect = sqlkit.Dialect`, `type Rows = sqlkit.Rows`, `type Statement = sqlkit.Statement`, `type Execer = sqlkit.Execer`, `type Querier = sqlkit.Querier`, `type SchemaIssue = sqlkit.SchemaIssue`), variables (`var PostgreSQL = sqlkit.PostgreSQL`, …) and one-line wrapper functions (`DecodeTime`, `DialectByName`, …).

This is a **deviation from the proposal**, which said the adapters "change imports only". With aliases they don't change at all, which shrinks the diff and the risk. Whether sqlcore keeps the aliases or drops them for direct `sqlkit` imports is decided before the first tag (see Open Questions).

### 5. Error classification across the boundary

- `sqlkit.SchemaError` has `Unwrap() error` returning `sqlkit.ErrSchemaMismatch`, and `ErrSchemaMismatch` itself matches `sqlkit.ErrConfiguration`.
- sqlcore keeps its own `SchemaError`, which embeds `*sqlkit.SchemaError`, keeps its `hmntsk:`-prefixed `Error()` text, and adds `Unwrap() []error { return []error{e.SchemaError, hmntsk.ErrConfiguration} }`.
- So `errors.Is(err, hmntsk.ErrConfiguration)`, `errors.Is(err, sqlkit.ErrConfiguration)` and `errors.As(err, &*sqlkit.SchemaError)` all hold.
- The existing `verify_test.go` pins the hmntsk behaviour.

### 6. Time normalisation equivalence

`sqlkit.NormalizeTime` must produce exactly what `hmntsk.NormalizeTime` produces (UTC, microsecond truncation), or stored values drift from values the engine compares against.
- **Default:** sqlcore continues to call `hmntsk.NormalizeTime` where it encodes task fields.
- **Guard:** a table test in sqlcore asserts the two agree over nanosecond, zoned, zero and pre-epoch instants.

Sharing one function would need sqlkit to import hmntsk or the reverse, which the split forbids. hmntsk may import sqlkit, but core must not gain SQL dependencies either.

### 7. Container helpers move to `sqlkittest`

- **Why:** `storetest` imports hmntsk, so `sqlkittest` cannot call its helpers. Duplicating them would contradict the use-testcontainers rule's "call the existing helper".
- **Move:** the helpers move to `sqlkittest/testutils.go` (`RunTestPostgres`, `RunTestMySQL`, `RunTestSQLite`, `PostgresImage`, `MySQLImage`, `TestOption`, `WithImage`, `WithDatabase`, `WithStartupTimeout`), still returning DSNs.
- **`storetest` keeps its exported names** as one-line delegates, so no store test changes. The allowed direction is hmntsk → sqlkit.
- **Database name:** the default becomes `sqlkit`, and storetest's delegates pass `WithDatabase("hmntsk")` to keep today's name.
- **Fixture schema:** `sqlkittest` ships a tiny per-dialect fixture (two tables, one identifier column, one secondary index, one JSON column, one timestamp column). The suite exercises executors, rendering, the runner and verification without any domain.

### 8. Split guardrails

**Authoritative check: `make split-check`.** For every module in a group it runs `go list -deps -test ./...` with the workspace active, and fails when a dependency path starts with `github.com/kartaladev/hmntsk` but not with an allowed prefix:

| Group | Allowed `github.com/kartaladev/hmntsk…` prefixes |
|---|---|
| `SQLKIT_MODULES` | `…/sqlkit` |
| `NOTIFY_MODULES` | `…/notify`, `…/sqlkit` |
| `HMNTSK_MODULES` | everything (no check) |

The failure lists module, package and offending import. `-test` is included, so a test importing `storetest` from `sqlkit` fails too.

**Editor-time warning: `depguard`.** `depguard` rules in `.golangci.yml` use `list-mode: strict` with allow lists for `**/sqlkit/**` files: `$gostd`, `github.com/kartaladev/hmntsk/sqlkit`, and the specific third-party modules. depguard matches by prefix and cannot express "the root module exactly but not its subdirectories", so it is a fast warning and `split-check` stays authoritative.

**Makefile groups.**
- `SQLKIT_MODULES := sqlkit sqlkit/sqlkittest sqlkit/stdsql sqlkit/pgx sqlkit/gorm`.
- `NOTIFY_MODULES :=` (empty until notify-core).
- `HMNTSK_MODULES :=` today's fifteen.
- `MODULES` is the union.
- A `GROUP ?= all` variable filters every existing target (`make test GROUP=sqlkit`), so a CI job per future repository is one flag.
- A new `EXECUTOR_MATRIX` and `executor-matrix` target lists the seven combinations (`sqlkit/stdsql:TestExecutorOnPostgres` …), mirroring `STORE_MATRIX`.
- `split-check` joins `lint` in the full check.

**Docs** live at `sqlkit/docs/README.md`: defaults, overrides and limits. Root `docs/` gains nothing sqlkit-specific.

### 9. Releasing and the eventual split (P2)

- **`sqlkit` is never tagged from this repository.**
  - Before its first release it moves to `github.com/kartaladev/sqlkit` with `git filter-repo --path sqlkit/`, and imports are rewritten once, before any consumer exists.
  - Consequence: **hmntsk's first tag is gated on that split**, because `store/sqlcore` cannot require an untagged in-repo module.
- **During development:** `go.work` gains the five modules. Their `go.mod` files carry the same comment the satellites use and name no workspace module.
- **`docs/releasing.md` changes:**
  - The module count becomes twenty.
  - The release list gains a preamble step: *0. split sqlkit and tag it in its own repository*.
  - `store/sqlcore` and `storetest` gain "depends on sqlkit" and "sqlkittest".
  - The tidy caveat lists the modules that now import an untagged package (`store/sqlcore`, `storetest`, the three executors and anything importing sqlcore).
  - `RELEASE_ORDER` stays hmntsk-only, so `docs_test.go` keeps matching, with a comment pointing at step 0.
- **After the split:** `store/sqlcore/go.mod` and `storetest/go.mod` gain `require github.com/kartaladev/sqlkit vX` and `…/sqlkittest vX`, and the aliases point at the new path.

## Risks / Trade-offs

- **[Behaviour drift during the move]** → The existing `storetest` suite, `store-matrix` and `relay-matrix` must stay green with no case changes, and `verify_test`, `dialect_test` and `migrations_test` move with the code before it moves (red first in `sqlkit`).
- **[Two normalisation functions diverge]** → The equivalence table test in sqlcore (decision 6).
- **[Aliases hide which package owns a symbol]** → godoc on each alias names `sqlkit` as the owner. The keep-or-drop decision is recorded before the first tag.
- **[hmntsk's first release now waits on a repository split]** → Stated in `docs/releasing.md` step 0. The split is mechanical because of `split-check`.
- **[depguard cannot express the exact rule]** → `split-check` is authoritative and runs in the full check.
- **[Separate context keys surprise a host who expects one transaction across task and notification stores]** → The documented explicit hand-off (decision 3). Notification writes do not need it.
- **[Callback-style Query is less familiar]** → It is the only way to guarantee release on every driver. The executor suite asserts release after a failed scan.
- **[`go mod tidy` breaks for more modules]** → Listed in `docs/releasing.md`. The hand-curated `go.mod` rule is unchanged.

## Migration Plan

This is an internal refactor plus additions. There is no data migration and no schema change.
- **Order:** land `sqlkit` and its tests, flip sqlcore to aliases, move the container helpers, add the executors, then add the guardrails.
- **Rollback:** revert the change; nothing is tagged or persisted.

## Resolved open questions

- **sqlcore's aliases.** Decided: sqlcore keeps the aliases for this change, so `store/*` needs no source change. Before hmntsk's first tag, `store/*` import `sqlkit` directly and the aliases are removed; that step is added to the release plan in `docs/releasing.md` (task 6.2), next to step 0.
- **Two `NormalizeTime` functions.** Decided: accepted, guarded by the equivalence table test in sqlcore (decision 6, task 3.1). Sharing one function would break the split rule in one direction or put SQL into the engine in the other.
