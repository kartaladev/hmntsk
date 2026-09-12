## 1. Repository and module scaffolding

- [x] 1.1 Create the core module `go.mod` at the repository root with the Go baseline from design D19; verify `go build ./...` succeeds on an empty package
- [x] 1.2 Create `go.work` and the ten module directories (`store/sqlcore`, `store/sql`, `store/pgx`, `store/gorm`, `transport/core`, `transport/http`, `transport/gin`, `transport/fiber`, `storetest`, `transporttest`), each with its own `go.mod`; verify `go work sync` succeeds and `go build ./...` passes in every module
- [x] 1.3 Add `.golangci.yml` and a `Makefile` with `lint`, `test` and `test-integration` targets; verify `make lint` passes on the empty tree
- [x] 1.4 Add a dependency-direction test asserting the core module imports no driver or web-framework package; verify it fails when a forbidden import is added

## 2. Core domain types

- [ ] 2.1 Define `TaskID`, `Status` (the ten states of spec `task-lifecycle`), `Priority`, and the terminal-state predicate; verify a table test covers every state's terminality
- [ ] 2.2 Define `CorrelationData` and `CallbackTarget` with JSON tags; verify a round-trip test preserves reference parameters verbatim
- [ ] 2.3 Define the `Task` aggregate including `Type`, `Version`, `SuspendedFrom`, lease fields, timestamps, and `json.RawMessage` payloads; verify JSON round-trip preserves large integers and unknown fields per spec `task-types`
- [ ] 2.4 Define the typed error set (conflict, illegal transition, not found, validation, authorisation, unregistered type) with `errors.Is` support; verify each is distinguishable by a table test
- [ ] 2.5 Define the ID generator port with a UUIDv7 default implementation; verify generated IDs sort in creation order

## 3. State machine

- [ ] 3.1 Implement the transition table from spec `task-lifecycle` as data, plus a `CanTransition` lookup; verify a table test asserts every legal and illegal pair
- [ ] 3.2 Implement pure transition functions (`Claim`, `Release`, `Start`, `Complete`, `Fail`, `Delegate`, `Suspend`, `Resume`, `Escalate`, `Cancel`, `Obsolete`) returning `(next Task, events []Event, err error)`; verify the receiver is never mutated on success or failure
- [ ] 3.3 Implement suspend/resume restoring `SuspendedFrom`; verify resume from each suspendable state returns to that exact state
- [ ] 3.4 Implement the transition history record type; verify every successful transition produces exactly one record with actor, times and comment
- [ ] 3.5 Verify with a table test that completion with a negative output yields `COMPLETED`, and that `FAILED` and `ERROR` are produced only by their respective paths (design D16)

## 4. Task type registry

- [ ] 4.1 Define `TypeSpec` (input/output schema, default priority, default deadline, escalation policy, assignment defaults) and the in-memory registry; verify lookup of an unregistered type returns the unregistered-type error
- [ ] 4.2 Implement `Register` with conflict detection; verify identical re-registration succeeds and a differing re-registration fails per spec `task-types`
- [ ] 4.3 Integrate a JSON Schema validator behind an internal interface; verify shape-only validation accepts an incomplete payload and rejects a mis-typed field
- [ ] 4.4 Implement full validation used at completion; verify a payload missing a required field is rejected
- [ ] 4.5 Implement default application and per-task override at creation; verify an explicit due date wins over the type default

## 5. Assignment

- [ ] 5.1 Define the `GroupResolver` and `AssignmentStrategy` ports plus a static in-memory implementation for tests; verify the static implementation satisfies both
- [ ] 5.2 Generate mocks for the ports with `mockgen` following the repository's mock conventions; verify generated mocks compile and are excluded from the production build
- [ ] 5.3 Implement eligibility evaluation with live group resolution and exclusion precedence (design D18); verify a table test covers candidate user, group member, excluded, and both-listed cases
- [ ] 5.4 Implement single-candidate auto-reservation and the empty-pool `ERROR` outcome; verify both paths per spec `task-assignment`
- [ ] 5.5 Implement per-operation authorisation (eligibility for claim and delegate, assignee identity for the rest); verify each operation refuses the wrong actor
- [ ] 5.6 Distinguish resolver failure from eligibility denial in returned errors; verify a failing resolver produces a fault, not a denial

## 6. Ports, service operations and events

- [ ] 6.1 Define the `Repository`, `Transactor`, `Clock` and `EventSink` ports, and the combined `Store` interface of design D5; verify a compile-time assertion that a test double satisfies `Store` whole
- [ ] 6.2 Define the closed event catalogue of spec `task-events` with correlation data on every event; verify a test asserts one event type exists per lifecycle transition
- [ ] 6.3 Implement the `Service` with all lifecycle operations wrapping transitions in `Transactor.Do`; verify each operation's happy path against an in-memory store
- [ ] 6.4 Implement the in-transaction sink append plus after-commit dispatch using `context.WithoutCancel`; verify dispatch still runs when the request context is cancelled immediately after commit
- [ ] 6.5 Reject a non-transactional sink at construction; verify `New` returns a configuration error for that wiring
- [ ] 6.6 Implement the pending-dispatch return path for host-led transactions; verify events are withheld until the host invokes it
- [ ] 6.7 Implement `SaveProgress` with RFC 6902 patch application, shape validation and implicit `RESERVED`→`IN_PROGRESS`; verify the first save transitions and later saves do not
- [ ] 6.8 Verify no event is produced by a progress save, and none by any refused operation
- [ ] 6.9 Implement `Query` with filters for assignee, eligibility, status, type and correlation, plus keyset pagination; verify paging is stable while new tasks are inserted
- [ ] 6.10 Implement the in-memory store adapter used for core tests; verify it passes the core service tests

## 7. Typed facade

- [ ] 7.1 Implement `Kind[In, Out]` with `Create`, `Complete`, `Get` and `OnCompleted`; verify typed and untyped creation produce identical stored payload bytes
- [ ] 7.2 Implement `Define[In, Out]` as registration plus handle, deriving schemas where possible; verify a conflicting `Define` fails at registration
- [ ] 7.3 Verify heterogeneous queries remain available through the untyped path while typed handles are in use

## 8. Storage conformance suite

- [ ] 8.1 Build `storetest` exporting `RunSuite(t, factory)` covering every requirement of spec `task-persistence`; verify it passes against the in-memory store
- [ ] 8.2 Add transaction cases: engine-led commit, host-led non-commit, nested `Do` joining without savepoints, inner failure aborting the whole scope; verify each fails when the behaviour is inverted
- [ ] 8.3 Add rollback cases: rollback on error, rollback on panic with the panic re-raised, no events delivered on rollback, no after-commit hooks on rollback
- [ ] 8.4 Add concurrency cases: concurrent claim yielding exactly one winner, stale-version write rejected with the current version reported
- [ ] 8.5 Add portability cases: case-sensitive identifier comparison, microsecond timestamp round trip, repository and transactor sharing one connection
- [ ] 8.6 Add a cancellation case asserting a context cancelled mid-`Do` rolls back

## 9. sqlcore and dialects

- [ ] 9.1 Define the `Dialect` interface (placeholders, quoting, column types, lock-clause support, returning support) with PostgreSQL, MySQL and SQLite implementations; verify a table test of generated fragments per dialect
- [ ] 9.2 Implement the statement builders for all task reads and writes, depending on no `RETURNING`; verify generated SQL and argument order per dialect
- [ ] 9.3 Implement conditional-update statements carrying the version predicate for every mutation; verify a rows-affected value of zero maps to the conflict error
- [ ] 9.4 Implement the candidate child-table statements for inbox and eligibility queries; verify the generated query plans use the intended index on each dialect
- [ ] 9.5 Implement keyset pagination statements; verify ordering is deterministic across dialects
- [ ] 9.6 Author per-dialect DDL for `tasks`, `task_candidates`, `task_history`, `task_outbox` and the type table, with collation pinned on identifier columns and `DATETIME(6)` on MySQL; verify the DDL applies cleanly on all three engines
- [ ] 9.7 Implement the configurable table prefix across DDL and all statements; verify a prefixed schema passes the same suite
- [ ] 9.8 Implement `Migrations(dialect)` embedding the DDL, plus a test-only runner; verify the accessor returns the complete statement set
- [ ] 9.9 Implement `VerifySchema` including collation checks; verify it names a missing table and a wrong collation, and passes on a correct schema

## 10. Store adapters

- [ ] 10.1 Implement `store/sql` over `database/sql` with `Store`, transaction join/flatten and context-carried handle helpers; verify it passes `storetest` on PostgreSQL, MySQL and SQLite
- [ ] 10.2 Wire testcontainers-backed PostgreSQL and MySQL helpers following the repository's testcontainers conventions; verify the helpers are reused rather than duplicated per adapter
- [ ] 10.3 Implement `store/pgx` over `pgxpool` with `pgx.Tx` participation; verify it passes `storetest` on PostgreSQL
- [ ] 10.4 Implement `store/gorm` executing `sqlcore` statements through `*gorm.DB`, with no GORM models and no `AutoMigrate`; verify it passes `storetest` on all three dialects
- [ ] 10.5 Force explicit flattening in `store/gorm` rather than GORM's default savepoint nesting; verify the nested-transaction conformance case passes
- [ ] 10.6 Implement `ContextWithTx` and `TxFromContext` for each adapter; verify host-led and engine-led transaction cases pass for all seven driver × dialect combinations

## 11. Escalation

- [ ] 11.1 Implement overdue discovery with lease claiming by conditional update; verify two concurrent sweeps escalate each task exactly once
- [ ] 11.2 Implement lease expiry recovery; verify a task abandoned by a crashed sweeper is escalated after expiry
- [ ] 11.3 Implement escalation policy application (pool widening) and the `OBSOLETE` supersession path; verify existing candidates remain eligible after widening
- [ ] 11.4 Exclude terminal and `SUSPENDED` tasks, and honour the policy's `IN_PROGRESS` exemption; verify each exclusion with a table test
- [ ] 11.5 Implement the host-driven sweep runner with no implicit startup; verify no goroutine, timer or polling begins when the host does not start it
- [ ] 11.6 Verify escalation emits its event and the engine sends no notification of its own

## 12. Transport conformance suite

- [ ] 12.1 Build `transporttest` exporting `RunSuite(t, mount)` covering every requirement of spec `task-http-api`
- [ ] 12.2 Add cases for every lifecycle operation reachable over HTTP
- [ ] 12.3 Add error-mapping cases for 400, 403, 404 and 409 including the conflict body reporting the current version
- [ ] 12.4 Add payload pass-through cases for large integers and unknown fields, and inbox cases for mixed types and stable paging

## 13. transport/core and bindings

- [ ] 13.1 Define request and response DTOs and the framework-independent handler signature; verify no standard-library HTTP type appears in the seam
- [ ] 13.2 Implement the route table, request validation and error-to-status mapping; verify against `transporttest` with a trivial in-process binder
- [ ] 13.3 Implement the task type schema endpoint; verify a client can retrieve schemas for an unfamiliar type
- [ ] 13.4 Generate the OpenAPI document from the route table; verify the generated document is regenerated and diff-clean in CI
- [ ] 13.5 Implement `transport/http` over `net/http`; verify it passes `transporttest`
- [ ] 13.6 Implement `transport/gin`; verify it passes `transporttest`
- [ ] 13.7 Implement `transport/fiber` for Fiber v3 without a fasthttp-to-net/http conversion layer; verify it passes `transporttest`
- [ ] 13.8 Take the acting actor from host-established request state; verify the engine performs no authentication of its own

## 14. Documentation, CI and release

- [ ] 14.1 Write package documentation for every exported type and function in the core module; verify `go doc` output is complete and the linter's doc-comment checks pass
- [ ] 14.2 Write the README covering the wiring model, the transaction contract, and a worked embedding example; verify the example compiles as an example test
- [ ] 14.3 Document the schema, the prefix option and the migration workflow per dialect; verify the documented statements match the embedded DDL by test
- [ ] 14.4 Add the CI workflow running lint, unit tests, and the seven-combination store matrix plus three transport suites; verify the matrix runs green end to end
- [ ] 14.5 Add `govulncheck` and race-detector runs to CI; verify both pass
- [ ] 14.6 Document the per-module tagging scheme and release order, with core and `store/sql` released first; verify the documented order is reflected in the release tooling
