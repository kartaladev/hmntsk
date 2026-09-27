## Why

An audit of the task stores produced twelve confirmed defects. Each one has a failing reproduction test in the scratch module `$V/stores` (see `VERIFIED-FINDINGS.md`, section F). They all break the promise that `task-persistence` makes: the stores should behave identically on every driver and dialect, and should report problems through the engine's error taxonomy rather than as raw driver errors or panics.

- On PostgreSQL and MySQL, two concurrent `Create` calls for the same ID return a raw `23505`/`1062` instead of `ErrConflict`.
- A duplicate candidate breaks the insert on every SQL dialect, while memstore accepts it.
- A 65-character task ID only fails on MySQL.
- MySQL pages mixed-case IDs in a different order from the other dialects.
- A table prefix is substituted into DDL raw, so on SQLite it can drop an unrelated table.
- A nil database handle is accepted and then panics on first use.

Nothing is tagged yet, so the fixes can still change constructor signatures and published schemas for free.

## What Changes

- **Driver error classification (S1, S3).** `sqlkit` gains a classifier that uses only the standard library. It sorts driver errors into classes: unique violation, serialization failure, deadlock, busy/locked and value too long. It recognises them through the driver's own error codes (SQLSTATE, SQLite extended codes, MySQL error numbers), never by parsing messages. Every store maps these classes onto the engine's taxonomy:
  - a unique violation on `Create` becomes a `*ConflictError`;
  - a serialization failure, deadlock or `SQLITE_BUSY`/`SQLITE_BUSY_SNAPSHOT` wraps `ErrConflict`;
  - a value that is too long becomes `ErrValidation`.

  Consumers can replace the classifier with an option.
- **SQLite transaction mode (S3).** Because `SQLITE_BUSY_SNAPSHOT` is classified, the losing writer gets `ErrConflict` even on a DSN without `_txlock=immediate`. `docs/schema.md` publishes the recommended DSN, which includes `_txlock=immediate`, and explains why that setting cannot be detected at construction.
- **Duplicate candidates (S2).** A candidate pool behaves as a set with its first-seen order kept. The engine removes duplicates in `Task.Normalize`, and every store, including memstore, stores and returns the deduplicated pool.
- **Context inside GORM transactions (S4).** `store/gorm` and `sqlkit/gorm` bind the per-call context to the active transaction, so a cancelled call fails with `context.Canceled`.
- **Fresh `ConflictError.Current` (S5).** The version a losing writer is told about is read with a locking read that sees the latest committed row, including under MySQL's REPEATABLE READ.
- **Large candidate pools (S7).** Candidate inserts are split into batches that stay under each dialect's bind-parameter limit. `sqlkit.Dialect` gains `MaxBindParameters()`. **BREAKING** for anyone who implements `Dialect` outside the library.
- **Identifier length limits (S8).** The engine validates identifier lengths against documented named maximums before it writes. These are the published MySQL column widths: task ID 64, task type and owner type 128, and 255 for other identifiers. Too-long values are rejected as `ErrValidation` on every store.
- **MySQL collation (S9).** Identifier columns move from `utf8mb4_0900_as_cs` to `utf8mb4_0900_bin`, which compares byte-wise with NO PAD. The task and notify DDL, the sqlkittest fixture and the upgrade notes change to match. **BREAKING** for existing MySQL schemas, which `VerifySchema` will now flag.
- **Collation verification (S10).** `VerifySchema` checks identifier collations on PostgreSQL and SQLite as well. It treats a missing `COLLATE "C"` on PostgreSQL, or `NOCASE` on SQLite, as a discrepancy.
- **Construction-time wiring checks (S12).** **BREAKING:**
  - `sqlstore.New`, `gormstore.New`, `pgxstore.New` and `sqlcore.New` now return `(…, error)`. They reject a nil handle or dialect with a `*ConfigurationError`.
  - A table prefix must match `[A-Za-z0-9_]*`, and the longest name it produces must fit the dialect's identifier limit (`Dialect.MaxIdentifierLength()`: PostgreSQL 63, MySQL 64).
  - `sqlkit.RenderSchema` now returns `([]string, error)` and refuses an unsafe prefix.
- **ClaimOverdue order (S13).** The `Repository.ClaimOverdue` port states its order, most overdue first (`due_at`, then `id`), and memstore follows it.
- **Out of scope:**
  - S6 (outbox settlement fencing) belongs to `relay-lease-fencing`.
  - S11 is UNCONFIRMED: no supported driver can reach the unclosed-rows path.
  - The SQLite part of S1 was REFUTED: `_txlock=immediate` serialises writers.
  - The PostgreSQL part of S12b was REFUTED: the injected DDL rolls back there. The prefix guard still covers it.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-persistence`:
  - the conflict error taxonomy now covers driver-level conflicts;
  - adds candidate-pool set semantics, identifier length limits, batching for large pools, a stated `ClaimOverdue` order, a fresh conflict version, a per-call context inside transactions, construction-time validation of handles and prefixes, and collation-checked verification.
- `sql-toolkit`:
  - adds driver error classification;
  - adds bind-parameter and identifier-length capabilities;
  - prefixes are validated when a schema is rendered;
  - collations are verified on every dialect;
  - the MySQL identifier collation becomes `utf8mb4_0900_bin`;
  - the GORM executor honours the per-call context inside a transaction.

## Impact

- **Code:**
  - `sqlkit`: `dialect.go`, `schema.go`, `verify.go`, and new files `classify.go` and `prefix.go`;
  - `sqlkit/gorm`;
  - `store/sqlcore`: `builder.go`, the DDL files and `verify.go`;
  - `store/sql`, `store/pgx`, `store/gorm`;
  - `memstore`;
  - in the root package: `task.go` (`CandidatePool` normalisation), `create.go`/`service.go` (length validation) and the `ports.go` godoc;
  - `notify/sqlstore`: DDL and its `RenderSchema` callers.
- **APIs (BREAKING, pre-tag):**
  - store constructors and `sqlcore.New` return an error;
  - `sqlkit.RenderSchema` returns an error;
  - `sqlkit.Dialect` gains `MaxBindParameters` and `MaxIdentifierLength`;
  - the MySQL identifier collation changes.
- **Docs:** `docs/schema.md` (SQLite DSN, MySQL collation upgrade, prefix rules, identifier limits), `notify/docs/schema.md`, `sqlkit/docs/README.md`, `README.md` and the examples that call the constructors.
- **Tests:** each defect's scratch reproduction is ported into `storetest` or `sqlkittest` as the red step. `make store-matrix`, `executor-matrix`, `relay-matrix` and `notify-store-matrix` must stay green.
