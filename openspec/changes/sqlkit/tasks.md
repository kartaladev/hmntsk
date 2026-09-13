Every task is test-first: write or move the test, run it and watch it fail for the intended reason (a missing symbol or wrong behaviour, not a broken fixture), then make it pass. Use `GOTOOLCHAIN=go1.26.8` for every command. Never run `make tidy` or `go mod tidy`: curate `go.mod` and `go.sum` by hand, following `store/sql`'s shape. Table tests follow the `table-test` skill (assert closures, `ctx` modifier, `t.Context()`). Containers come only from `sqlkittest`'s `RunTestX` helpers.

## 1. Workspace skeleton and guardrails

- [ ] 1.1 Create modules `sqlkit`, `sqlkit/sqlkittest`, `sqlkit/stdsql`, `sqlkit/pgx` and `sqlkit/gorm` (package names `sqlkit`, `sqlkittest`, `stdsqlexec`, `pgxexec`, `gormexec`), each with a doc comment and the satellite `go.mod` comment. Add them to `go.work`. Verify `go build ./...` succeeds in each.
- [ ] 1.2 Split `Makefile` `MODULES` into `SQLKIT_MODULES`, `NOTIFY_MODULES` (empty) and `HMNTSK_MODULES`, add the `GROUP ?= all` filter to every existing target, and add `EXECUTOR_MATRIX` with the `executor-matrix` target. Verify `make test GROUP=sqlkit` iterates only the five modules and `make release-order` output is unchanged (`docs_test.go` passes).
- [ ] 1.3 Add `make split-check` (`go list -deps -test` per group, allowed prefixes per design decision 8). Red: temporarily import `github.com/kartaladev/hmntsk` from a `sqlkit` test and confirm the target fails naming module, package and import; then remove it and confirm the target passes.
- [ ] 1.4 Add the `depguard` strict allow-list rules for `**/sqlkit/**` to `.golangci.yml`. Red: the same temporary import is flagged by `make lint GROUP=sqlkit`; then remove it and lint passes.

## 2. sqlkit: dialects, statements and codecs

- [ ] 2.1 Move `dialect_test.go` cases to `sqlkit` first (red: symbols missing), then move `Dialect`, the three dialects, `Dialects`, `DialectByName`, `onConflictSuffix` and `quoteWith`. Verify `go test ./...` in `sqlkit`, including the unknown-name case.
- [ ] 2.2 Write table tests for `Statement.IsZero` and `sqlkit.Writer` (bind markers per dialect, `BindAll`, identifier quoting), watch them fail, then implement `Writer` from sqlcore's private `stmt`. Verify the tests pass.
- [ ] 2.3 Write table tests for `TimestampLayout`, `EncodeTime`, `DecodeTime`, `DecodeJSON`, `DecodeString`, `DecodeInt`, `EncodeRaw`, `NormalizeTime` and `TrimSQL` (nil, string, bytes, zoned, nanosecond, pre-epoch, malformed), watch them fail, then move the codecs. Verify the tests pass.
- [ ] 2.4 Write table tests for `SplitStatements`, `RenderSchema` (prefix token, comments, blank lines, trailing statement without semicolon), `ApplySchema` (stops and names the failing statement, using a fake `Execer`) and `DropTables` (reverse order, PostgreSQL `CASCADE`). Watch them fail, then implement. Verify the tests pass.
- [ ] 2.5 Write table tests for `VerifySchema` against a scripted `Querier`: missing table reported once without its columns or indexes, missing column, wrong identifier collation, missing index, SQLite unreported collation treated as default, several issues in one error, `errors.Is(err, ErrSchemaMismatch)` and `errors.Is(err, ErrConfiguration)`. Watch them fail, then implement `SchemaExpectation`, `TableExpectation`, `SchemaIssue`, `SchemaError`, the introspection queries per dialect and the comparison. Verify the tests pass.
- [ ] 2.6 Define `Rows`, `Execer`, `Querier` and `Executor` exactly as design decision 2 states, with godoc naming the defaults. Verify `go vet ./...` passes in `sqlkit`.

## 3. sqlcore on top of sqlkit (no behaviour change)

- [ ] 3.1 Add a table test in `store/sqlcore` asserting `sqlkit.NormalizeTime` equals `hmntsk.NormalizeTime` over nanosecond, zoned, zero and pre-epoch instants, and watch it fail to compile until `sqlkit` is imported. Verify it passes.
- [ ] 3.2 Replace sqlcore's moved code with aliases, variables and one-line wrappers (design decision 4). Rebuild `identifierColumns`, `expectedColumns` and `expectedIndexes` as a `sqlkit.SchemaExpectation` used by `Builder.VerifySchema`, and turn `Builder.Migrations`, `MigrationsSource`, `Migrate` and `Drop` into wrappers over `sqlkit`. Verify all existing sqlcore tests (`builder_test`, `dialect_test`, `migrations_test`, `verify_test`, `scan_test`, `query_test`, `outbox_test`, `docs_test`) pass unchanged.
- [ ] 3.3 Wrap `*sqlkit.SchemaError` in sqlcore's `SchemaError`, keeping its `hmntsk:` message and `Unwrap() []error`. Add a test asserting `errors.Is` for both `hmntsk.ErrConfiguration` and `sqlkit.ErrConfiguration`, plus `errors.As` to `*sqlkit.SchemaError`; watch it fail, then implement. Verify it passes.
- [ ] 3.4 Verify `store/sql`, `store/pgx`, `store/gorm` and `cmd/hmntsk-schema` build with no source change, and that `make store-matrix relay-matrix` is green on all seven combinations.

## 4. sqlkittest: helpers, fixture and conformance suite

- [ ] 4.1 Move the container helpers and pinned images from `storetest/testutils.go` to `sqlkittest/testutils.go` (default database `sqlkit`), leaving `storetest`'s exported names as one-line delegates passing `WithDatabase("hmntsk")`. Verify `storetest`'s in-memory suite and `make store-matrix` still pass.
- [ ] 4.2 Add the per-dialect fixture schema and its `SchemaExpectation` to `sqlkittest`. Verify `RenderSchema` of each fixture passes the `sqlkit` rendering tests.
- [ ] 4.3 Write `sqlkittest.RunExecutorSuite(t, factory)` as table-driven cases covering every `sql-toolkit` scenario:
  - rows matched, empty statement, rows released after a failed scan, statement text in errors, driver-correct bind markers;
  - executor-led commit, caller-led join left open, `InTransaction`;
  - nested join and flatten, including an inner failure the outer scope handles;
  - panic rollback re-raised, cancelled context rollback;
  - payload byte-exact, instant to the microsecond, textual timestamp ordering, case-sensitive identifier comparison;
  - fixture render, apply and verify, including a deliberately broken schema reporting every discrepancy.

  Verify the suite compiles and `split-check` passes for `sqlkittest`.

## 5. Executors (red with the suite, then implement)

- [ ] 5.1 `stdsqlexec`: add `TestExecutorOnSQLite` calling `RunExecutorSuite` and watch it fail (no executor). Implement `New` (nil handle or dialect is a configuration error), `ContextWithTx`, `TxFromContext`, `Dialect`, `Exec`, `Query`, `Do`, `InTransaction`, `ExecStatement` and `QueryStatement`, ported from `store/sql`. Verify SQLite passes, then add `TestExecutorOnPostgres` and `TestExecutorOnMySQL` and verify both pass.
- [ ] 5.2 `pgxexec`: add `TestExecutorOnPostgres` and watch it fail. Implement over `*pgxpool.Pool`, keeping the detached-context rollback and wrapping pgx rows for the callback. Verify it passes.
- [ ] 5.3 `gormexec`: add `TestExecutorOnSQLite`, and a case where an inner scope fails, the outer scope handles the error and returns nil, and no write is durable. Watch both fail. Implement with the `?` dialect wrapper reported by `Dialect()` and the flattening `Do` that never calls `db.Transaction`. Verify SQLite, then add and verify PostgreSQL and MySQL.
- [ ] 5.4 Add constructor table tests in each executor for nil arguments returning a configuration error, watch them fail, and make them pass. Verify `make executor-matrix` is green on all seven combinations.

## 6. Docs, release notes and full check

- [ ] 6.1 Write `sqlkit/docs/README.md`: the default and override per decision, the stated limits (no migration tool, separate context keys and the explicit hand-off, callback queries, pgx PostgreSQL-only) and a short example per executor. Add godoc to every exported symbol naming its default. Verify `go doc` renders each package without missing comments (revive `exported` passes).
- [ ] 6.2 Update `docs/releasing.md`: twenty modules, step 0 (split `sqlkit` into its own repository and tag it before any hmntsk tag), a pre-release step "`store/*` import `sqlkit` directly, and sqlcore's aliases are removed" (design, resolved open questions), sqlcore and storetest dependencies, and the expanded tidy caveat. Verify `docs_test.go` still passes.
- [ ] 6.3 Run `/simplify` on the touched code and re-run the unit tests. Verify they pass.
- [ ] 6.4 Run the full check `GOTOOLCHAIN=go1.26.8 make lint split-check test test-race test-integration vuln` and `make store-matrix relay-matrix executor-matrix`. Verify every target exits 0.
