## Why

Applications that contain human decision steps — approvals, reviews, manual data entry — each reimplement the same machinery: a task inbox, a claim/complete lifecycle, deadlines and escalation, and a way to notify whatever started the work. That machinery is generic (WS-HumanTask / BPEL4People model it), but no reusable Go implementation exists that stays independent of a specific orchestrator, database driver, or HTTP framework.

`hmntsk` fills that gap as an **embeddable engine library**: the host application owns the process, the database and the transport; the library owns the task lifecycle and depends on nothing above it.

## What Changes

This is a greenfield change. Nothing exists yet beyond a README.

**Core engine (`hmntsk`)**

- A `Task` aggregate with an explicit ten-state lifecycle (`CREATED`, `READY`, `RESERVED`, `IN_PROGRESS`, `SUSPENDED`, `COMPLETED`, `FAILED`, `ERROR`, `EXITED`, `OBSOLETE`) whose transitions are pure functions returning a new value, funnelled through a single set of operations (`Create`, `Claim`, `Release`, `Start`, `SaveProgress`, `Complete`, `Fail`, `Delegate`, `Suspend`, `Resume`, `Escalate`, `Cancel`).
- Caller-agnostic correlation: `CorrelationData` (`OwnerType`, `OwnerRef`, `ActivityKey`, `Extra`) and `CallbackTarget` (address plus opaque reference parameters, echoed back verbatim), modelled on the WS-Addressing Endpoint Reference. The library never names a workflow engine type.
- A mandatory **task type registry**: a `TypeSpec` carries input/output JSON Schema, default priority, default deadline, escalation policy and assignment defaults. Registration is required; creating a task of an unregistered type is an error.
- Payloads are stored as opaque `json.RawMessage`. An additive generic facade `Kind[In, Out]` gives Go hosts compile-time-typed `Create`, `Complete`, `Get` and event handlers without making the core, the repository port or the transports generic.
- Ports only, no implementations in core: `Repository`, `Transactor`, `Clock`, `GroupResolver`, `AssignmentStrategy`, `EventSink`.

**Persistence (`sqlcore` + three driver modules)**

- `sqlcore` builds dialect-aware SQL and executes nothing. Three dialects: PostgreSQL, MySQL, SQLite.
- Three driver modules — `store/sql` (`database/sql`), `store/pgx`, `store/gorm` — execute that SQL and supply transaction participation. Seven valid driver × dialect combinations (pgx is PostgreSQL-only).
- Concurrency is **optimistic compare-and-swap on a `version` column for every mutation**, portable across all three dialects; row locks are a PostgreSQL/MySQL-8 optimisation, not a requirement.
- The library emits per-dialect DDL for the host's own migration pipeline and verifies the live schema at startup. It never runs DDL automatically in production.

**Transport (`transport/core` + three framework modules)**

- `transport/core` owns the route table, DTOs, validation, error-to-status mapping and pagination — written once.
- `transport/http` (`net/http`), `transport/gin` and `transport/fiber` (v3) are thin binders that only decode a request into a DTO and encode the result. The seam sits below `http.Handler` deliberately, because Fiber is fasthttp-based and not `net/http`-compatible.

**Events**

- Lifecycle operations produce events; they are written to an outbox table **inside** the host's transaction and dispatched to in-process handlers **after** commit. The library never invokes business use cases directly.

**Conformance suites**

- `storetest` and `transporttest` export shared behavioural suites so all seven store combinations and all three transports are asserted against one set of cases rather than seven and three.

### Non-goals (v1)

- The workflow engine itself, and any task inbox UI.
- Notification channels (email, Slack). Escalation emits an event; the host decides what to do with it.
- Load-balancing / auto-claim assignment strategies. The `AssignmentStrategy` seam admits them later.
- A separate hot-draft store (Redis) for autosave. Debounced client-side saves use the normal path.
- SOAP or WS-* tooling. Only the Endpoint Reference *pattern* is adopted, over JSON.
- Running the outbox relay, or webhook retry/backoff machinery.

## Capabilities

### New Capabilities

- `task-lifecycle`: the task aggregate, its ten states and legal transitions, the operations that drive them, actor authorisation for each, optimistic-concurrency conflict semantics, and the transition history record.
- `task-types`: the type registry — `TypeSpec` contents, mandatory registration, JSON Schema validation scope (shape on progress, full on completion), per-type defaults and per-task overrides, and the typed `Kind[In, Out]` facade.
- `task-assignment`: candidate users, candidate groups and exclusions; the `GroupResolver` and `AssignmentStrategy` ports; claim, release and delegate eligibility; auto-reservation of single-candidate tasks.
- `task-persistence`: the `Repository` and `Transactor` ports, transaction ownership and reentrancy rules, optimistic CAS, the table layout, dialect divergences, DDL emission and schema verification.
- `task-events`: the closed event catalogue, the in-transaction outbox sink, after-commit dispatch, and `CallbackTarget` storage and echo semantics.
- `task-escalation`: deadlines, the lease-based claim used by the escalation runner, escalation actions, and the `OBSOLETE` transition.
- `task-http-api`: the REST contract served by `transport/core` and its three framework bindings, including error-to-status mapping and inbox pagination.

### Modified Capabilities

None — this is the project's first change.

## Impact

- **New code**: seven Go modules plus two test-suite packages in a multi-module repository coordinated by `go.work`. No existing code is touched.
- **New public contracts**: a Go API (the `Service` and the `Kind[In, Out]` facade), a REST contract, and a database schema — all three are compatibility surfaces from first release.
- **Host constraints**: the task tables must live in the same database as the host's business tables, because the joinable transaction and the outbox depend on it. Table names carry a configurable prefix to avoid collisions.
- **Dependencies**: a JSON Schema validator and a JSON Patch implementation in core; `pgx`, `gorm`, `gin` and `fiber` confined to their own modules so `go get hmntsk` pulls none of them.
- **CI**: a seven-combination store matrix (PostgreSQL and MySQL via testcontainers, SQLite in-process) plus three transport suites, and per-module tagging and release ordering.
