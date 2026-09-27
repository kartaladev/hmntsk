Every defect task follows red → green → refactor (golang-tdd.md, error-reproducible.md):
1. **Port** the named scratch reproduction from `$V/stores/findings_test.go` (or `$V/stores/modernc/sqlite_test.go`) into the repo:
   - into `storetest`, so memstore and store/sql, pgx and gorm on sqlite, postgres and mysql all run it;
   - or into `sqlkit/sqlkittest`, so all seven executor combinations run it.

   `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`.
2. **Red:** run the ported test focused on the named dialect, and confirm it fails with the assertion message stating the defect, not with a compile or fixture error.
3. **Green:** make the smallest fix.
4. **Refactor:** consider `/simplify`, then re-run.

Tests use `table-test` form (assert closures, `t.Context()`). Containers come only from the existing `RunTestX` helpers. Doubles come from `use-mockgen`. Never `go mod tidy`.

## 1. Suite plumbing for deterministic races

- [ ] 1.1 Add `LockWaiters func(ctx context.Context) (int, error)` to `storetest.Harness` (design 13), with a `waitForLockWaiters(t, h, n)` helper that polls and skips when the hook is nil. Implement it in the store/sql, pgx and gorm test factories:
  - postgres: `pg_locks WHERE NOT granted`;
  - mysql: `performance_schema.data_lock_waits`.

  Port the helper from `$V/stores/helpers_test.go` `waitForLockWaiter`. Verify `make store-matrix` is still green; no case uses the hook yet.

## 2. Driver error classification (S1, S3; deadlock/serialization are new behaviour)

- [ ] 2.1 Write `sqlkit` table tests for `Classify` over fake errors implementing `SQLState()`, `Code()`, and a local struct type named `MySQLError` with `Number uint16`. Cover each code in design 1, a wrapped error, a nil error, and a message-only "duplicate key" error that must stay `Unclassified`. Watch them fail (the symbol is missing), then implement `ErrorClass`, `Classifier` and `Classify`. Verify `go test ./...` in `sqlkit` and `make split-check`: no driver import.
- [ ] 2.2 Add a `sqlkittest` case "a primary-key collision is classified as a unique violation" through each executor. Also add a `WithErrorClassifier` override case: a caller classifier is used and the default is not consulted. Watch both fail on the seven combinations, then add `WithErrorClassifier` and `Executor.Classify` to stdsqlexec, pgxexec and gormexec. Verify `make executor-matrix`.
- [ ] 2.3 **Port `S1_concurrent_create_same_id`** into `storetest/concurrency.go`, including its MySQL snapshot-first variant, as "concurrent Create of the same ID reports ErrConflict". Use the `LockWaiters` hook. Red: `go test -run 'TestStoreOnPostgres/Concurrency' -count=1` in store/sql, store/pgx and store/gorm, then the MySQL equivalents in store/sql and store/gorm. Each must fail with `expected ErrConflict, got … 23505/1062`. Green: add `sqlcore.MapDriverError` (design 2) and call it on the `Create` insert paths in all three adapters, plus `WithErrorClassifier` on each store. Verify `-count=20` passes on postgres and mysql, and memstore/SQLite still pass.
- [ ] 2.4 Add a `storetest` case "a host-supplied classifier replaces the default". It needs a harness option to build the store with a classifier that marks every error unclassified, and asserts that the raw driver error surfaces for the S1 race. Watch it fail, then wire the option through the factories. Verify on postgres.
- [ ] 2.5 Add `storetest` cases for deadlock and serialization failure. Deadlock: two explicit transactions update tasks A and B in opposite order, using the `LockWaiters` hook. Serialization: a host-led PostgreSQL `SERIALIZABLE` transaction via `HostTx`. Both assert `ErrConflict` with the driver error reachable through `errors.As`. There is no scratch test to port (these were UNCONFIRMED in the audit and are new behaviour). Watch them fail on postgres and mysql. Green comes from 2.3's mapping. Verify `-count=20`.
- [ ] 2.6 **Port `modernc/TestSQLiteDocsDSNRace`** (S3) into `store/sql/store_test.go` as a SQLite-only case that opens the DSN without `_txlock=immediate`. Red: `go test -run 'TestSQLiteDocsDSNRace' -count=1 ./...` in store/sql fails with `database is locked (517)`. Green: `SQLITE_BUSY_SNAPSHOT` maps to `ErrConflict` through 2.3's mapping on the `Update` path too. Then update `docs/schema.md`: the SQLite DSN gains `&_txlock=immediate`, with the rationale from design 3. Verify `-count=20`, and verify `store/sqlcore/docs_test.go` if it pins the DSN.

## 3. Candidate pools as sets (S2)

- [ ] 3.1 Add root-package table tests for `CandidatePool.Normalize`: repeats removed per list, first-seen order kept, nil and empty preserved. Also test `Task.Normalize` calling it. Watch them fail, then implement. Verify `go test ./...` at the root.
- [ ] 3.2 **Port `S2_duplicate_candidates`** into `storetest/repository.go`. Keep the `Service.Create` variant and add a direct-port variant: `Create` and `Update` with repeated groups read back deduplicated. Red: `go test -run 'TestStoreOnSQLite/Repository' -count=1` in store/sql must fail with a PK violation, and memstore must fail the direct-port read-back. Green: deduplicate in `sqlcore.Builder.InsertCandidates` and in memstore `Create`/`Update`. Verify `make store-matrix`.

## 4. GORM per-call context (S4)

- [ ] 4.1 **Port `S4_cancelled_ctx_inside_tx`**:
  - the `sqlkit_*exec` subcases go into the `sqlkittest` transaction cases;
  - the `store_*` subcases go into `storetest/cancellation.go`.

  Red: `go test -run 'TestExecutorOnSQLite' -count=1` in sqlkit/gorm, and `-run 'TestStoreOnPostgres/Cancellation'` in store/gorm, must fail with `expected context.Canceled`. Green: `handle` returns `tx.WithContext(ctx)` in `store/gorm/store.go` and `sqlkit/gorm/executor.go`. Verify `make executor-matrix store-matrix`.

## 5. Fresh ConflictError.Current (S5)

- [ ] 5.1 **Port `S5_conflict_current_stale`** into `storetest/concurrency.go` as "the loser is told the committed version under a snapshot read". Red: `go test -run 'TestStoreOnMySQL/Concurrency' -count=1` in store/sql and store/gorm must fail with `Current must be … 2, got 1`. Green: add a `Builder.SelectTaskVersionLocked` that appends `FOR SHARE` when `SupportsSkipLocked()` is true, and use it on the `Update` conflict path in all three adapters. Add a sqlcore builder table test for the rendered SQL per dialect. Verify `-count=20` on mysql and postgres.

## 6. Large candidate pools (S7)

- [ ] 6.1 Add `sqlkit` table tests for `Dialect.MaxBindParameters()` (65535, 65535, 32766). Watch them fail, then implement. Verify `go test` in sqlkit.
- [ ] 6.2 Add a sqlcore builder test: a pool of `MaxBindParameters()/4 + 1` entries yields two statements whose bind counts are each within the limit, and an empty pool yields none. Watch it fail, then change `InsertCandidates` to return `[]Statement` and update the three adapters' `Create`/`Update` loops. Verify `go test ./...` in store/sqlcore.
- [ ] 6.3 **Port `S7_large_candidate_pool`** into `storetest/repository.go`. Size the pool from the harness dialect: 17,000 users, or 9,000 on SQLite. Also add the rollback case "failure part-way leaves no partial pool". Red: this must fail on each dialect before 6.2 lands, so run it on a branch point before 6.2, or temporarily revert 6.2's adapter loop. Verify `make store-matrix`.

## 7. Identifier length limits (S8)

- [ ] 7.1 Add root-package table tests for `CheckIdentifierLimits`:
  - each field at its limit and at limit+1;
  - runes counted, not bytes: a 64-rune multi-byte ID passes;
  - the `ValidationError` names the field and the limit.

  Watch them fail, then add the named constants and the function (design 7). Verify root `go test`.
- [ ] 7.2 **Port `S8_long_task_id`** into `storetest/repository.go`, with 64-char acceptance and 65-char rejection through both the repository port and `Service.Create`. Red: on mysql, store/sql fails with `Error 1406`; on memstore, postgres and sqlite, the 65-char case fails because it is accepted. Green: call `CheckIdentifierLimits` in `Service.Create`, `Registry.Register`, the actor and assignee paths, every store's `Create`/`Update` (memstore included), and map `ValueTooLong` in `MapDriverError`. Verify `make store-matrix` and root `go test`.

## 8. MySQL byte-wise collation (S9)

- [ ] 8.1 **Port `S9_identifier_paging_order`** into `storetest/portability.go`, with the trailing-space eligibility scenario alongside. Red: `go test -run 'TestStoreOnMySQL/Portability' -count=1` in store/sql fails with the `a-1 A-1 …` order. Green:
  - change `sqlkit.MySQL.IdentifierCollation()` to `utf8mb4_0900_bin`;
  - update `store/sqlcore/ddl/mysql.sql` and `sqlkit/sqlkittest/fixture.go`;
  - update `notify/sqlstore/ddl/mysql.sql`, `notify/sqlstore/ddl/email/mysql.sql` and `notify/sqlstore/testdata/schema/*mysql*.sql`;
  - update the comments in `storetest/testutils.go` and `sqlkittest/testutils.go`, and the golden and dialect tests.

  Verify `make store-matrix executor-matrix notify-store-matrix`.
- [ ] 8.2 Update `docs/schema.md`, `notify/docs/schema.md` and `sqlkit/docs/README.md`: minimum MySQL 8.0.17, the collation rationale (why not `utf8mb4_bin`), and the "Upgrading" `ALTER TABLE … MODIFY … COLLATE utf8mb4_0900_bin` statements. Verify `store/sqlcore/docs_test.go` passes.

## 9. Collation verification on PostgreSQL and SQLite (S10)

- [ ] 9.1 Add `sqlkit` table tests for the SQLite column-definition `COLLATE` extractor: none means `BINARY`, `NOCASE`, quoted names, and an unparseable definition means unknown. Also test the PostgreSQL default-collation mapping. Watch them fail, then implement them (design 9) and remove the "unreported collation is correct" rule. Verify `go test` in sqlkit.
- [ ] 9.2 **Port `S10_verify_schema_collation`** into `sqlkittest` schema cases, as "a broken identifier collation is reported" on every dialect. Also add a `store/sqlcore` verify test for the task schema. Red: `go test -run 'TestExecutorOnSQLite|TestExecutorOnPostgres' -count=1` in sqlkit/stdsql returns nil instead of `ErrSchemaMismatch`. Green: the 9.1 code. Verify `make executor-matrix store-matrix notify-store-matrix`, so the correct schemas still pass.

## 10. Construction-time wiring checks (S12)

- [ ] 10.1 Add `sqlkit` tests:
  - `Dialect.MaxIdentifierLength()` returns 63, 64 and 0;
  - `ValidatePrefix` rejects a quote, a semicolon, a space and non-ASCII, and accepts the empty prefix and `acme_`;
  - a PostgreSQL prefix making `task_outbox_unpublished_idx` exceed 63 bytes names the identifier;
  - `RenderSchema(dialect, doc, prefix)` returns a configuration error and no statements for a bad prefix.

  Watch them fail, then implement (design 10). Update the callers: sqlcore `migrations.go`, `cmd/hmntsk-schema`, `notify/sqlstore/store.go`, `notify/sqlstore/email.go`, and the sqlkittest suite. Verify `go test` in each touched module.
- [ ] 10.2 **Port `S12_nil_handles`** into the store/sql, store/pgx and store/gorm constructor tests (table form), and add the same for `sqlcore.New(nil)`. Red: a nil-pointer panic at first use. Green: the constructors return `(…, error)` with `*hmntsk.ConfigurationError`. Update every caller:
  - the store tests and factories;
  - `examples/store-drivers`, `examples/contextual-ui`, `examples/schema-migrations` and `examples/correlated-tasks`;
  - `README.md` and `docs/schema.md` snippets.

  Verify `go build ./...` and `go vet ./...` in every module.
- [ ] 10.3 **Port `S12_prefix_injection`** (sqlite; the postgres variant is kept as a regression guard) into the store/sql constructor tests. Assert that construction fails with `ErrConfiguration` and that the victim table still exists. Red on the pre-10.2 code: the victim table is dropped on SQLite. Verify it passes.
- [ ] 10.4 **Port `S12_long_prefix`** (postgres, prefix lengths 48 and 54) into the store/sql constructor tests, asserting `ErrConfiguration` at construction. Red: `42P07 already exists` from `Migrate`. Verify it passes, and that a 40-byte prefix still migrates and verifies.

## 11. ClaimOverdue order (S13)

- [ ] 11.1 **Port `S13_claim_overdue_order`** into `storetest/repository.go`, with the equal-due-date tie-break case added. Red: `go test -run 'TestSuiteAgainstTheInMemoryStore' -count=1 ./...` in storetest (memstore) fails, claiming `s13-a`. Green: memstore sorts by `(DueAt, ID)`, and the `Repository.ClaimOverdue` godoc in `ports.go` states the order (design 11). Verify `make store-matrix`.

## 12. Docs and final checks

- [ ] 12.1 Update the godoc on every new option and port, naming its default:
  - `WithErrorClassifier`, `Classify`, `MaxBindParameters`, `MaxIdentifierLength`, `ValidatePrefix`, the identifier limit constants and `CandidatePool.Normalize`.

  Update `docs/schema.md`: prefix rules, identifier limits, DSNs. Add CHANGELOG entries for each BREAKING change in the proposal. Verify `make lint`, including revive `exported`.
- [ ] 12.2 Run `/simplify` over the touched packages and re-run the unit tests. Verify they pass.
- [ ] 12.3 Final check: `GOTOOLCHAIN=go1.26.8 make lint split-check test test-race` and `make store-matrix relay-matrix executor-matrix notify-store-matrix`. Verify every target exits 0. Re-run the ported S1/S5 cases with `-count=20` on postgres and mysql.
