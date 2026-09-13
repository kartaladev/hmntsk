Every behaviour task is test-first. Write the case, run it focused (`GOTOOLCHAIN=go1.26.8 go test -run ... -count=1 ./...` in the owning module), and confirm it fails **for the intended reason**, never because a package does not compile. Where a new exported name or port method is needed to compile, add it first as an inert stub. Where a test is written after the code, invert the implementation temporarily and confirm the test notices.

Table tests use the project's `assert` closure form and `t.Context()`. Containers come only from the existing `RunTest*` helpers.

Per `.claude/rules/library-design.md`, each group tests **the default** and **at least one consumer override**. Never run `make tidy` or `go mod tidy`.

## 1. Core query model

- [x] 1.1 Add inert stubs and verify `go build ./...` across the workspace:
  - `hmntsk.Ordering` with `OrderCreated` (zero value), `OrderPriority`, `OrderDue` and `OrderUrgency`;
  - `Query.OrderBy` and `Query.Group`;
  - `Repository.Count(ctx, ResolvedQuery) (int64, error)`, stubbed to return `0, nil` in `memstore`, `store/sql`, `store/pgx` and `store/gorm`;
  - `Service.Count`, `Service.CountBuckets`, `MaxCountBuckets = 32`.
- [x] 1.2 Red: a table test of `Service.Query` and `Service.Count` against memstore. It covers:
  - rejecting an unknown `OrderBy` with a validation error;
  - rejecting `CountBuckets` over `MaxCountBuckets` with a validation error;
  - an empty bucket set returning an empty map;
  - `CountBuckets` resolving each distinct candidate's groups once, asserted with a counting `GroupResolver` double (via `use-mockgen` if a mock is warranted).

  Confirm the validation cases fail because the stubs accept everything.
- [x] 1.3 Green: validate orderings and the bucket limit in `Service`, and implement `Count` and `CountBuckets` by resolving groups as `Query` does (design D4). 1.2 passes.

## 2. Shared store tests (written before any store implements them)

- [x] 2.1 Red: add `storetest/inbox.go` with `runInboxCases`, registered as `t.Run("Inbox", ...)` in `storetest.go`. The seed data has ties in priority and due date, several tasks without a deadline, pooled and held work, and group pools. Cases:
  - each ordering, ascending and descending, returns the exact expected ID sequence, with no-deadline tasks last under due and urgency in both directions;
  - for each ordering and direction, paging with `Limit: 2` concatenates to exactly the unpaged sequence, with no repeats or gaps;
  - a cursor from one ordering reused under another is a validation error;
  - `Group` alone, and `Group` with owner type;
  - `Count` equals the unpaged match count and ignores `Limit` and `Cursor`;
  - `Count` counts a task once when it matches a candidate by both user and group;
  - the default `OrderCreated` sequence is unchanged from today.
- [x] 2.2 Run 2.1 against memstore, and against `store/sql` on SQLite. Confirm the ordering, group and count cases fail because the stores order by ID only, ignore `Group`, and count 0, while the default-order case passes.

## 3. memstore

- [x] 3.1 Green: implement the orderings with the shared key comparison (design D1, D2: the no-deadline flag, the ID tiebreak, and `Descending` applied to the whole key except the no-deadline position), cursors bound to their ordering, `Group`, and `Count`. The storetest inbox cases pass on memstore, and the existing repository cases stay green.

## 4. SQL stores

- [x] 4.1 Red: `store/sqlcore` builder unit tests (`query_test.go`) for each dialect. Each ordering's `ORDER BY` uses the no-deadline flag expression. The keyset predicate is the expanded OR-chain. `Group` becomes the `kind='group'` `EXISTS`. `Count` uses `COUNT(DISTINCT ...)` and ignores order, limit and cursor. Confirm they fail against today's builder.
- [x] 4.2 Green: implement the builder changes and the cursor encoding (base64url JSON of ordering, direction and key values), with a decode that rejects a mismatched or malformed cursor as a validation error. 4.1 passes.
- [x] 4.3 Implement `Count` in `store/sql` (`store.go:409` area), `store/pgx` (`store.go:379`) and `store/gorm` (`store.go:398`) through the builder. Verify the storetest inbox cases on SQLite first, then `GOTOOLCHAIN=go1.26.8 make store-matrix` for all seven driver × dialect combinations.
- [x] 4.4 Red then green: schema.
  - Add `tasks_priority_idx`, `tasks_due_order_idx` and `tasks_urgency_idx` to `ddl/postgres.sql`, `ddl/mysql.sql` and `ddl/sqlite.sql` (design D3).
  - Extend `TestVerifySchema` first, so a database missing an index is reported. Seen failing, then add the indexes to `VerifySchema`.
  - Update `docs/schema.md` (indexes, plus the stated limit on the no-deadline flag) and keep `TestSchemaDocumentationMatchesThePublishedDDL` green.

## 5. Task type metadata

- [x] 5.1 Red: core table tests for `TypeSpec.Metadata`.
  - `Clone` deep-copies it.
  - `Equal` compares it, so the same name with different metadata fails `Registry.Register` as a conflict and identical metadata re-registers fine.
  - JSON round-trips it under `metadata`, omitted when empty.

  Add the constants `MetadataFormKey = "hmntsk.formKey"` and `MetadataRoute = "hmntsk.route"` as stubs first. Confirm the conflict and deep-copy cases fail.
- [x] 5.2 Red: a table test for `ExpandRoute(template, task)`. It covers:
  - `{task.id}`, `{task.type}`, `{correlation.ownerType}`, `{correlation.ownerRef}`, `{correlation.activityKey}` and `{correlation.extra.<key>}`;
  - an unknown placeholder left untouched;
  - a missing extra key left untouched;
  - values inserted raw, not escaped.

  Confirm it fails against an identity stub.
- [x] 5.3 Green: implement 5.1 and 5.2.
- [x] 5.4 Red then green: persistence.
  - Add a `metadata` text column to `task_types` in all three DDL files.
  - Write canonical JSON with sorted keys in `sqlcore.Builder.UpsertType`, and read it back in `ScanTypes`/`typeSpec`.
  - `VerifySchema` requires the column, and `docs/schema.md` documents it.

  First add a round-trip case (metadata in, identical metadata out, `Equal` true) to the existing task-type persistence tests: locate them with gopls references to `sqlcore.ScanTypes`. Also extend `TestVerifySchema` for the column. Verify with `make store-matrix`.

## 6. HTTP API

- [x] 6.1 Red: extend `transporttest/inbox.go` (`runInboxCases`) and `transporttest/tasktypes.go` (`runTaskTypeCases`). Where a case needs a custom policy, first extend `transporttest` so a case can build its API with `transportcore` options.
  - **Default policy** (`SelfOnly`):
    - `candidate=me` returns the actor's available tasks;
    - `candidate=<other actor>` gives 403;
    - `group=` gives 403;
    - a query naming neither candidate nor assignee gives 403;
    - `assignee=me` with no actor gives 403;
    - `GET /tasks/count?candidate=me` returns `{"count": n}` equal to the query's match count;
    - a count refused like a query gives 403.
  - **Ordering:** `orderBy=urgency&direction=asc` returns the expected order in stable pages; `orderBy=bogus` gives 400.
  - **Override:** with `WithQueryAuthorizer` permitting a supervisor, `group=finance-approvers` returns the team queue; a policy refusing everything turns the actor's own query into 403; `AllowAll` restores unrestricted queries.
  - **Metadata:** `GET /task-types/{name}` returns metadata exactly as registered.

  Confirm the cases fail for the intended reasons against today's API.
- [x] 6.2 Green in `transport/core`:
  - parse `orderBy`, `direction` and `group`;
  - resolve `me` against `Request.Actor`;
  - add `QueryAuthorizer`, `QueryAuthorizerFunc`, `SelfOnly` (default), `AllowAll` and `WithQueryAuthorizer` (design D6), with authorization errors mapped to 403;
  - add `GET /tasks/count`;
  - include metadata in task type responses.

  `transporttest` passes on `transport/http`, `transport/gin` and `transport/fiber`.
- [x] 6.3 Regenerate `transport/core/openapi.json` for the new parameters, the count route and type metadata. `TestOpenAPIDocumentIsGeneratedAndCommitted` and `TestOpenAPIDocumentDescribesEveryRoute` pass.

## 7. Documentation

- [x] 7.1 Write `docs/inbox.md`, built around defaults and overrides:
  - buckets as queries (my tasks, available, team, overdue, per domain, per record);
  - orderings, with the stated limits on custom sorts and the no-deadline position;
  - counts and `CountBuckets` (with `MaxCountBuckets` and how to go beyond it);
  - group queues;
  - type metadata with the well-known keys, `ExpandRoute`, and escaping being the host's job;
  - HTTP authorization: the self-only default, `me`, writing a policy that composes `SelfOnly`, and `AllowAll`.

  Add a drift test in `transport/core` asserting the document names `SelfOnly`, `AllowAll`, `WithQueryAuthorizer`, every `Ordering` value, both metadata keys and `MaxCountBuckets`, taken from code. Seen failing before the document exists.
- [x] 7.2 Update README (the HTTP section and a short inbox section linking `docs/inbox.md`, including the unreleased breaking change to query authorization) and the godoc for every new option, port and constant, each naming its default. Verify with `go doc` on core and `transport/core`, and `GOTOOLCHAIN=go1.26.8 make lint`.

## 8. Verification

- [x] 8.1 Run `/simplify` on the touched code (core, memstore, sqlcore, the three adapters, `transport/core`, `storetest`, `transporttest`), then re-run their tests with `-race`.
- [x] 8.2 Run `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln store-matrix relay-matrix`, green across the workspace. Run `openspec validate contextual-task-inbox --strict`, which must pass.
- [ ] 8.3 On the PR, every existing CI check passes, including the store and transport matrices.
