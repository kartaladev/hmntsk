## Context

See `proposal.md` for why. The evidence is `VERIFIED-FINDINGS.md` section F. Its reproductions live in the scratch module `$V/stores`, where `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`:
- `findings_test.go`, `TestFindings/<dialect>/<Sn>`;
- `modernc/sqlite_test.go`, `TestSQLiteDocsDSNRace`.

The current state that shapes the approach:

- **No driver error is classified anywhere.**
  - `store/sql/store.go:250`, `store/pgx/store.go:238` and `store/gorm/store.go:247` look for an existing row before inserting. They return whatever the insert returns.
  - Under concurrency, the pre-check misses the uncommitted row. PostgreSQL then reports `23505` and MySQL `1062`.
  - `sqlkit` imports only the standard library, and its executors wrap driver errors with `%w`. `store/sql` and `sqlkit/stdsql` pull in lib/pq, go-sql-driver/mysql and modernc only as test dependencies.
- **Driver error types differ.** pgconn's `*PgError` and lib/pq's `*Error` both expose `SQLState() string`. modernc's `*sqlite.Error`, which glebarez also uses, exposes `Code() int` with extended codes. go-sql-driver's `*MySQLError` exposes only the exported fields `Number uint16` and `SQLState [5]byte`, with no methods.
- **`ConflictError.Current`** is re-read with a plain `SELECT` (`store/sql/store.go:351`). Under MySQL REPEATABLE READ, that read comes from the transaction's snapshot, so it reports the stale version.
- **Candidate inserts.** `sqlcore.Builder.InsertCandidates` (`builder.go:310`) renders one multi-row `INSERT` with four binds per row. The primary key is `(task_id, kind, value)`, so a repeated value is a PK violation. At 9,000 rows (SQLite) or 17,000 rows (PostgreSQL/MySQL) the statement exceeds the bind limit. memstore stores pools verbatim.
- **Identifier widths.** On MySQL, `id`/`task_id` are `VARCHAR(64)`, type and owner type are `VARCHAR(128)`, and the other identifiers are `VARCHAR(255)`. PostgreSQL and SQLite use unbounded `text`/`TEXT`. The engine does no length validation.
- **Collations.**
  - MySQL: `sqlkit.MySQL.IdentifierCollation()` is `utf8mb4_0900_as_cs`, which is accent-sensitive and case-sensitive but orders `a-1 A-1 b-1 …`.
  - PostgreSQL: `information_schema.columns.collation_name` is NULL for a column that uses the default collation.
  - SQLite: `pragma_table_info` has no collation at all.
  - `sqlkit/verify.go:207` treats `""` as correct, so both of these slip through.
- **Prefixes.** `sqlkit.RenderSchema` (`schema.go:17`) substitutes `{{PREFIX}}` raw. Store constructors return `*Store` without an error and accept nil handles. `sqlcore.New` returns `*Builder`.
- **GORM and context.** `store/gorm` `handle` (`store.go:155`) and `sqlkit/gorm` `handle` (`executor.go:136`) return the transaction's `*gorm.DB` without `WithContext(ctx)`, so the per-call context is lost.
- **memstore.** `ClaimOverdue` sorts by `id` (`memstore.go:602`). The SQL stores order by `due_at, id`. The port's godoc states no order.
- **Suites.**
  - `storetest.RunSuite` runs on memstore and on ten adapter-dialect combinations.
  - `Harness` has `SupportsConcurrentTransactions`, but no hook for waiting on lock waiters.
  - `sqlkittest.RunExecutorSuite` runs on seven combinations.
  - `notify/sqlstore` shares the MySQL collation and calls `RenderSchema`.
- **Nothing is tagged.** Signature and schema changes are free, but they are recorded here (library-design rule 7).

## Goals / Non-Goals

**Goals:**
- Every confirmed S-finding in scope is fixed. Its scratch reproduction becomes a permanent case in `storetest` or `sqlkittest`, and that case runs on every combination.
- Every new behaviour has a default that needs no configuration. Where a consumer can override it, the override is covered by a test.

**Non-Goals:**
- S6 outbox settlement fencing, which belongs to `relay-lease-fencing`.
- S11 (`readText` not closing rows on a scan error). It is UNCONFIRMED: no supported driver can make `Scan` fail on the introspection rows, so no reproduction was possible, and error-reproducible.md forbids fixing an unproven defect. It can be revisited if a driver that reaches it is added.
- SQLite S1 is REFUTED, because `_txlock=immediate` serialises writers. PostgreSQL S12b is REFUTED, because the injected DDL rolls back. Neither needs its own fix. The classifier and the prefix guard cover both anyway.
- Retrying conflicts inside the store. Retries belong to the caller: the relay's attempt budget and the sweeper's skip-on-conflict (`sweep.go:181`) already rely on `ErrConflict` surfacing.
- Moving the task stores onto `sqlkit` executors.

## Decisions

### 1. A code-based classifier in `sqlkit`, replaceable per store and executor

`sqlkit` gains:

```go
type ErrorClass int
const (
    Unclassified ErrorClass = iota
    UniqueViolation
    SerializationFailure
    Deadlock
    Busy
    ValueTooLong
)
type Classifier func(err error) ErrorClass
func Classify(err error) ErrorClass // the default
```

`Classify` walks the chain with `errors.As` against small local interfaces:
- `interface{ SQLState() string }` covers pgx and lib/pq. It maps `23505`, `40001`, `40P01`, `55P03` (lock not available, which counts as Busy) and `22001`.
- `interface{ Code() int }` covers modernc and glebarez. It maps the primary code 19 with extended codes 1555 and 2067 to UniqueViolation, and 5, 517 (`BUSY_SNAPSHOT`) and 6 to Busy.
- For MySQL, it uses reflection over a struct whose type is named `MySQLError` and which has an exported `Number uint16` field. It maps 1062 to UniqueViolation, 1213 to Deadlock, 1205 to Busy, and 1406 to ValueTooLong.

Messages are never parsed.

- **Default:** every store and executor uses `sqlkit.Classify`.
- **Override:**
  - `sqlstore.WithErrorClassifier`, `pgxstore.WithErrorClassifier` and `gormstore.WithErrorClassifier` each take a `sqlkit.Classifier`.
  - The same option exists on each `sqlkit` executor constructor, where the classifier is exposed through `Executor.Classify(err)` for stores built on the executors.
  - A consumer on an unlisted driver, or one who wants `Busy` treated as fatal, passes its own classifier. A consumer can wrap `sqlkit.Classify` to extend it.
- **Alternatives considered:**
  - Importing the drivers in `sqlkit`. Rejected: it breaks the stdlib-only rule and the split guardrail (`depguard`), and every consumer would pull in all three drivers.
  - A per-driver sub-module (`sqlkit/mysqlerr`). Rejected for now: it adds a module to release for one struct field. It stays the fallback if reflection proves brittle; see Open Questions.
  - GORM's `TranslateError`. Rejected: it covers GORM only and loses the SQLSTATE.

### 2. How stores map classes onto the engine taxonomy

This lives in `sqlcore` as a helper that all three adapters share: `sqlcore.MapDriverError(classify, err, taskID) error`.

| Class | Engine error |
|---|---|
| UniqueViolation during `Create` | `&hmntsk.ConflictError{TaskID: id}`, with `Current` 0 because the aborted PostgreSQL transaction cannot re-read it. The driver error is joined so it stays reachable. |
| SerializationFailure, Deadlock, Busy | `fmt.Errorf("%w: …: %w", hmntsk.ErrConflict, err)` |
| ValueTooLong | a `*hmntsk.ValidationError` wrapping the driver error |
| Unclassified | returned unchanged, keeping the statement text |

The existing check-before-insert stays. It gives a meaningful `Current` in the common case with no race. The classifier only catches the race.

- **Default:** as in the table.
- **Override:** through the classifier from decision 1. The mapping table itself is fixed, because it is the engine's documented error contract and changing it would break `transport/core`'s error-to-status mapping.

### 3. SQLite DSN: classify, document, do not detect (S3)

Detecting `_txlock` at construction is not possible:
- `*sql.DB` does not expose its DSN;
- SQLite has no pragma that reports the transaction mode;
- a probe would need two connections and would depend on `busy_timeout`.

Decision 1 makes the documented-DSN race return `ErrConflict`, because the losing writer's error is 517 `SQLITE_BUSY_SNAPSHOT`. `docs/schema.md` changes its SQLite DSN to add `&_txlock=immediate`, and explains why: writers queue behind `busy_timeout` instead of failing fast. The explanation also says that without the setting, losing writers get `ErrConflict` more often.

- **Default:** the recommended DSN serialises writers, and any DSN gets correct error classes.
- **Override:** the host owns the DSN. Deferred transactions remain supported.

### 4. Candidate pools are sets (S2)

`CandidatePool.Normalize()` removes duplicates from each of the three lists, keeping first-seen order. `Task.Normalize()` calls it, so every path through the engine normalises. `sqlcore.Builder.InsertCandidates` normalises again, and so does memstore's `Create`/`Update`, so direct port use agrees across stores.

Rejecting duplicates as `ErrValidation` was the other option. It was rejected because memstore already accepts them, because a directory-resolved or escalation-widened pool can produce overlaps no host wrote, and because a repeat has no meaning.

- **Default:** deduplicate.
- **Override:** none, deliberately. A repeat cannot change eligibility, so there is no policy to choose (spec: "Candidate pools have set semantics"). A host that wants to reject repeats can validate its `CreateRequest` before calling.

### 5. A fresh conflict version (S5)

`Builder.SelectTaskVersion` gains a locking variant. Where the dialect has row locking (`SupportsSkipLocked()`, which is true on PostgreSQL and MySQL), it appends `FOR SHARE`. On MySQL, a locking read returns the latest committed row regardless of the snapshot. SQLite serialises writers and has no stale-snapshot case. The adapters use the locking variant only on the conflict path in `Update`.

A new `SupportsLockingReads` capability was considered and rejected. Every dialect with skip-locked also has `FOR SHARE`, and a separate method would be one more member on the interface.

- **Default:** the locking re-read.
- **Override:** none needed. It changes no successful path. Under a host-led PostgreSQL REPEATABLE READ transaction, `FOR SHARE` may itself raise `40001`, which decision 2 maps to a conflict with `Current` 0, as the spec allows.

### 6. Batched candidate inserts (S7)

`sqlkit.Dialect` gains `MaxBindParameters() int`, which returns 65535 on PostgreSQL and MySQL and 32766 on SQLite. `InsertCandidates` becomes `InsertCandidates(id, pool) []Statement`, chunked at `MaxBindParameters()/4` rows. The adapters execute every chunk in the same transaction; `Create` and `Update` already run inside `Do`.

Reads are unaffected. `SelectCandidates(ids…)` binds one parameter per task, and the task list is bounded by the page limit.

- **Default:** no cap on pool size. Pools are batched.
- **Override:** a host that wants a cap enforces it in its `AssignmentStrategy` or before `Create`. A documented engine limit was rejected because it would invent a number the databases do not impose.

### 7. Identifier length limits in the engine (S8)

The root package gains named constants: `MaxTaskIDLength = 64`, `MaxTypeNameLength = 128`, `MaxOwnerTypeLength = 128`, and `MaxIdentifierLength = 255` for owner ref, activity key, candidate values, assignee and actor. It also gains `CheckIdentifierLimits(Task) error`, which counts runes because MySQL `VARCHAR(n)` counts characters.

These are called from:
- `Service.Create` (ID, type, correlation, pool);
- `Registry.Register` (type name);
- every operation that sets an assignee or records an actor;
- each store's `Create`/`Update`, so direct port use is uniform (spec).

Widening the MySQL columns was rejected. It was the alternative the lead offered, but widening only moves the line, and PostgreSQL and SQLite would still accept values MySQL rejects. A named, enforced limit is the stated line (rule 4).

- **Default:** the limits above.
- **Override:** none. A longer identifier cannot be stored portably. A host needing more can map its long keys to short IDs and keep the long key in `correlation_extra` or the payload.

### 8. MySQL collation `utf8mb4_0900_bin` (S9)

`utf8mb4_bin` was rejected because it is a PAD SPACE collation: `'alice' = 'alice '` holds there, which would make eligibility and primary keys ignore trailing spaces. `utf8mb4_0900_bin` is NO PAD and orders by code point, which is UTF-8 byte order. It needs MySQL 8.0.17 or later, so the supported minimum moves from 8.0 to 8.0.17.

The change touches:
- `sqlkit` (the dialect and the fixture);
- `store/sqlcore/ddl/mysql.sql`;
- `notify/sqlstore/ddl/*.sql` and its testdata;
- the three schema docs.

- **Default:** `utf8mb4_0900_bin`.
- **Override:** none. The collation is the portability guarantee itself. A different collation is reported by `VerifySchema` and not accepted silently.

### 9. Collation verification everywhere (S10)

- **PostgreSQL:** the column query reports `COALESCE(collation_name, '<default>')`. Only `C` matches, so a missing `COLLATE "C"` becomes an issue. The `"C"` versus `C` spelling is normalised.
- **SQLite:** the column query joins `sqlite_master.sql`. A small tokenizer in `sqlkit` extracts the `COLLATE <name>` from each column's definition. A column with no clause is `BINARY`, the SQLite default, which is correct. The DDL text SQLite stores is the original statement, so this is reliable for tables the published schema created.
- The rule "an unreported collation is correct" is removed.

`pragma_index_xinfo` was considered and rejected: it reports collation only for indexed columns.

- **Default:** verify every identifier column.
- **Override:** the caller's `SchemaExpectation.IdentifierColumns` already chooses which columns are checked.

### 10. Construction refuses miswiring (S12)

- `sqlstore.New`, `gormstore.New`, `pgxstore.New` and `sqlcore.New` return `(…, error)`. A nil handle or dialect gives a `*hmntsk.ConfigurationError`, and so does an invalid prefix.
- `sqlkit` gains `Dialect.MaxIdentifierLength() int`: 63 on PostgreSQL, 64 on MySQL, 0 meaning unlimited on SQLite. It also gains `ValidatePrefix(dialect, prefix string, names []string) error`, which requires the pattern `^[A-Za-z0-9_]*$` and checks that `len(prefix+name) <= max` for every created name.
- `RenderSchema(document, prefix)` becomes `RenderSchema(dialect, document, prefix) ([]string, error)`. It derives the names from the document's `{{PREFIX}}<name>` occurrences, so a new index cannot slip past the check.

Quoting the prefix was the alternative. It was rejected because the prefix lands inside an already-quoted identifier, where the substitution itself is the vulnerability.

- **Default:** no prefix. Validation always on.
- **Override:** any prefix within the character set and length. A host needing other characters is refused, and the refusal names the rule. No override is possible, because relaxing this is the injection line (rule 4).

### 11. `ClaimOverdue` order is part of the port (S13)

The `Repository.ClaimOverdue` godoc states the order: ascending `due_at`, then byte-wise `id`. memstore sorts `(DueAt, ID)` to match.

- **Default:** most overdue first.
- **Override:** none on the port. A host that wants a different escalation priority filters by `Types` or runs several sweeps.

### 12. GORM per-call context (S4)

Both `handle` functions return `tx.WithContext(ctx)`. The same case is added to `sqlkittest`, so pgx and stdsql, which already pass, stay honest.

### 13. Suite hooks for deterministic races

`storetest.Harness` gains `LockWaiters func(ctx) (int, error)`. Each SQL adapter's test implements it from `pg_locks WHERE NOT granted` or from `performance_schema.data_lock_waits`. It is nil for memstore and SQLite, and cases that need it skip when it is nil.

This is how the ported S1 and S5 cases wait for a parked insert, without sleeps, as error-reproducible.md rule 3 requires. The deadlock case uses two explicit transactions and the same hook.

## Risks / Trade-offs

- [Reflection on `MySQLError` could break if the driver renames the field] → The executor and store matrices assert the MySQL classes against the real driver, so a rename fails CI. The fallback is the `mysqlerr` sub-module (Open Questions).
- [The SQLite `COLLATE` tokenizer could misread a hand-written DDL] → It handles only the forms the published schema uses. On an unparseable column definition it reports the collation as unknown, which is an issue, never a silent pass.
- [Existing MySQL deployments fail `VerifySchema` after the collation change] → `docs/schema.md` "Upgrading" gets the `ALTER TABLE … MODIFY … COLLATE utf8mb4_0900_bin` statements. Pre-tag, this is recorded, not versioned.
- [Classifying Busy as a conflict could hide a mis-tuned `busy_timeout`] → The driver error stays reachable, and a host can override the classifier to treat Busy as fatal.
- [Batching makes one `Create` many statements] → All of them run in one transaction, and the batch size keeps a normal pool at one statement.

## Migration Plan

Nothing is tagged. All changes ship together, and CHANGELOG and docs note each breaking change:
- the constructor signatures;
- `RenderSchema`;
- the two new `Dialect` methods;
- the MySQL collation and the 8.0.17 minimum.

For rollback, revert the change. No data migration is required except the optional MySQL collation `ALTER`.

## Assumptions (recorded instead of asking)

- The engine removes duplicates rather than rejecting them (S2).
- The engine enforces identifier length limits; MySQL columns are not widened (S8).
- `utf8mb4_0900_bin` is used rather than the suggested `utf8mb4_bin`, because of the PAD SPACE semantics (S9).
- S3 is solved by classification plus docs. Construction-time detection is infeasible (decision 3).
- Classifying deadlock, serialization failure and `SQLITE_BUSY` has no confirmed defect behind it: it was not attempted in the audit. It is added as new behaviour, with its own red-first tests, not as a bug fix.

## Open Questions

- If reflection over `MySQLError` fails review, should MySQL classification move to a `sqlkit/mysqlerr` sub-module that consumers opt into? This does not change the spec or the task breakdown: only where one mapping lives.
