## Context

Greenfield repository — a README and nothing else. See `proposal.md` for motivation, and the seven capability specs for the behaviour being committed to.

Three constraints shape everything below:

1. **The engine is embedded, not deployed.** It runs inside a host process that already owns a database handle, a transaction boundary, an identity system and a web framework. The engine cannot assume a process of its own, a scheduler of its own, or a message bus.
2. **The middle must be uniform, the edges want to be specific.** Storage, queries and transport all handle many task types at once; business code handles one at a time and wants types.
3. **Adapter count multiplies every mistake.** Seven driver × dialect combinations and three web frameworks mean any decision duplicated per adapter is a decision maintained ten times.

## Goals / Non-Goals

**Goals**

- Correctness under concurrency on every supported dialect, including one with no row-level locking.
- Adapters that are mechanical: no adapter should contain a decision.
- The dependency rule of `proposal.md` made verifiable, not aspirational — swapping the orchestrator changes only event consumers.
- A host can commit a task change and its own business change together.

**Non-Goals (design level, beyond the proposal's scope exclusions)**

- Pluggable event ordering or exactly-once delivery. At-least-once with a closed event catalogue is the contract.
- An in-engine cache. Task reads go to the database.
- Supporting engine tables in a different database from the host's. The atomicity guarantee depends on co-location.

## Decisions

### D1. Embeddable engine, not a deployable service

The source material describes a service that owns a database, a scheduler, an outbox relay and a webhook sender. We build the same domain as a library the host embeds.

*Why:* a service forces every adopter to operate a new process and a new database, and forfeits the ability to commit a task change with a business change. The library keeps that option and can still be wrapped in a service later.

*Alternative considered:* library plus a `cmd/` server in the same repository. Deferred, not precluded — everything here stays compatible with adding one.

### D2. One core, N thin bindings — on both axes

```
        +-------------------------------+
        |          hmntsk (core)        |
        |  domain, state machine, ports |
        +---+-----------------------+---+
            |                       |
  Repository|                       |operation set
            v                       v
  +-------------------+   +----------------------+
  |  store/sqlcore    |   |   transport/core     |
  |  dialect-aware    |   |  routes, DTOs, codec |
  |  SQL + args;      |   |  error->status map   |
  |  executes nothing |   |                      |
  +--+-------+------+-+   +---+------+-------+---+
     |       |      |         |      |       |
   /sql   /pgx   /gorm      /http  /gin   /fiber
```

*Why:* the alternative — writing each adapter independently — produces N copies of the SQL and the error mapping, and N places to fix every bug. With this shape, every adapter module is roughly two hundred lines with no decisions in it.

### D3. Driver and dialect are orthogonal

`database/sql`, `pgx` and `gorm` are drivers. PostgreSQL, MySQL and SQLite are dialects. `sqlcore` owns dialect (placeholder style, column types, lock clause availability, quoting); driver modules own execution, scanning and transaction participation.

The valid matrix is sparse — `pgx` is PostgreSQL-only — giving **seven** combinations, not nine:

```
                 Postgres   MySQL   SQLite
  database/sql      *         *       *
  pgx               *         -       -
  gorm              *         *       *
```

*Why:* putting SQL inside driver modules would mean writing PostgreSQL three times while still not supporting MySQL.

*Consequence for `gorm`:* the gorm module contains **no GORM models and no `AutoMigrate`**. Its job is transaction participation — running `sqlcore`'s statements through the host's `*gorm.DB` — not object mapping. This avoids leaking `gorm:` struct tags into the engine and avoids GORM owning the schema.

### D4. The host's transaction is joined, never owned

The engine exposes a `Transactor` port; lifecycle operations run inside it.

```go
type Transactor interface {
    Do(ctx context.Context, fn func(ctx context.Context) error) error
}
```

Rules:

- **Whoever begins, commits.** The engine never commits or rolls back a transaction it did not start.
- **Nested scopes join and flatten** — no second transaction, no savepoint. An operation invoked from inside another shares one transaction.
- **Both directions are supported.** Engine-led (`Do` wraps host code, which retrieves the driver handle from the context) and host-led (the host injects an open transaction into the context before calling).

*Why this over the alternatives:* a context-carried transaction alone (host must inject it) fails silently when the host forgets — the outbox row lands outside the transaction with no error. An explicitly scoped repository (`repo.WithTx(tx)`) is type-safe but leaks the driver type into every host call site and into any driver-agnostic host code.

*Honest note:* `Do`'s closure takes a `context.Context`, not a transaction handle, so the transaction still travels in the context underneath. This design does not remove that mechanism — it **confines** it to one adapter module, invisible to the host and pinned by conformance tests, instead of making it a contract the host must remember to honour.

*Known divergence to handle explicitly:* GORM's `db.Transaction` nests using SAVEPOINTs by default, which is different behaviour from the other two drivers for the same engine call. The gorm module must flatten explicitly rather than inherit that default.

### D5. Repository and Transactor are constructed as one value

Two independently constructed ports that must share a connection is a wiring footgun that compiles and fails at runtime as split transactions. Adapter constructors return a single `Store` satisfying both.

```go
st := pgxstore.New(pool)   // exposes Repository and Transactor
svc := hmntsk.New(st)
```

*Alternative rejected:* separate constructors with documentation telling users to pass the same handle. Documentation does not fail a build.

### D6. Events: durable inside the transaction, dispatched after it

```
  Do(ctx, func(ctx) error {
       load task
       transition (pure)
       save task
       sink.Append(events)        <-- INSIDE tx: outbox rows only
  })
  ------------------ COMMIT ------------------
  dispatch(events)                <-- AFTER commit: in-process handlers,
                                      webhooks, metrics
```

*Why both phases:* dispatching in-process handlers inside the transaction exposes consumers to state that may roll back and holds row locks for the duration of a handler. Writing the durable record after the transaction reintroduces the dual-write problem the outbox exists to solve. Neither phase can absorb the other.

Two consequences:

- After-commit work uses `context.WithoutCancel` of the request context — it keeps logging and tracing values while surviving a client disconnect. Without this, notifications are dropped under exactly the conditions where they matter.
- A non-transactional sink configured as the durable record is a **startup error**, not a tolerated configuration. It works in tests and loses events in production.

When the host owns the outer transaction (D4, host-led), the engine cannot observe the commit and therefore returns the pending dispatch for the host to invoke after committing.

### D7. Transitions are pure functions

```go
next, events, err := task.Claim(actor, now)   // returns a new value
```

*Why:* the aggregate is in-memory Go state and the write can roll back underneath it. A mutating receiver leaves the caller holding a task that says `RESERVED` after a rolled-back claim. As a bonus, the whole state machine becomes table-testable with no infrastructure.

### D8. Untyped core, typed facade

Payloads are `json.RawMessage` in the core; a generic facade adds types at the edges.

```go
var Approval = hmntsk.Define[ApprovalInput, ApprovalOutput](svc, "approval")
id, err := Approval.Create(ctx, ApprovalInput{Amount: amt})
```

*Alternatives considered:*

- `map[string]any` — the library can inspect payloads, but JSON numbers decode to `float64` (silently wrong for monetary amounts and large identifiers) and round-tripping reorders keys, polluting the patch-based audit trail.
- Generics on the aggregate (`Task[In, Out]`) — breaks the heterogeneous inbox, forces `Repository[In, Out]` (multiplied across seven store adapters), and makes the HTTP transports impossible, since a handler receiving arbitrary JSON cannot select a generic instantiation. Go also forbids type parameters on methods, closing the obvious escape route.
- `json.RawMessage` alone — correct but pushes an `Unmarshal` plus error handling into every host call site.

*Why the facade wins:* generics are confined to one file, the `Repository` port stays non-generic (the decisive factor given the adapter matrix), transports keep moving bytes, and it is purely additive — the untyped path remains fully usable.

*Accepted gap:* `SaveProgress` stays untyped. Go cannot express a partial of a struct, and drafts are partial by definition. Full type checking happens at `Complete`, which is where the specs put full validation anyway.

### D9. Task type registration is mandatory

`Register(TypeSpec)` accepts plain data; `Define[In, Out]` is `Register` plus the typed handle. Registration is required before a task of that type can exist.

*Why:* the engine needs a schema at runtime regardless — the inbox UI is not written in Go and renders forms from JSON Schema. Given the registry must exist, making it authoritative gives one enforcement point for validation, for rejecting unknown types at the HTTP edge, and for catching a typo in a type string at the boundary rather than as an unrenderable orphan task. Accepting `TypeSpec` as data keeps non-Go hosts and config-driven types viable.

### D10. Optimistic compare-and-swap is the universal concurrency mechanism

Every mutation is a conditional update on a `version` column. Row locks are a PostgreSQL/MySQL-8 optimisation layered on top, never a requirement.

*Why:* SQLite has no row-level locking at all — `FOR UPDATE` is absent, not weaker. A design that depends on it makes SQLite a broken target rather than a first-class one, and SQLite is what makes the conformance suite fast enough to run on every commit. One mechanism also means one conflict error for clients to handle.

*Consequence:* escalation cannot be built on `SELECT ... FOR UPDATE SKIP LOCKED`. It uses a **lease** (`locked_by`, `locked_until`) claimed by conditional update, which works identically on all three dialects and self-heals when a sweeper crashes.

### D11. Never query inside a payload

Anything filterable — type, status, priority, due date, correlation fields, assignee, candidates — is a real column. `jsonb` vs `JSON` vs `TEXT` then becomes a storage detail with no query surface instead of three incompatible query languages.

Candidates live in a **child table**, not an array or JSON column: none of PostgreSQL arrays, MySQL JSON or SQLite can be indexed portably for "the tasks Alice may claim".

### D12. Dialect divergences that are correctness issues, not syntax

| | PostgreSQL | MySQL | SQLite |
|---|---|---|---|
| Placeholder | `$1` | `?` | `?` |
| `FOR UPDATE SKIP LOCKED` | 9.5+ | 8.0+ | absent |
| `RETURNING` | yes | **no** | 3.35+ |
| Default collation | case-sensitive | `utf8mb4_0900_ai_ci` — **case-insensitive** | case-sensitive |
| Timestamp | `timestamptz`, µs | `DATETIME(6)` | no native type |

- **Collation** must be pinned explicitly in the DDL for actor, group and type identifier columns. On MySQL's default collation `assignee = 'Alice'` matches `alice`, silently changing who may claim a task — and only on one dialect.
- **No `RETURNING` on MySQL** means `sqlcore` never depends on it; every write is a conditional update plus a rows-affected check, which composes naturally with D10.
- **Timestamps** are stored as UTC at fixed microsecond precision, `DATETIME(6)` on MySQL (not `TIMESTAMP`, which carries timezone conversion and a 2038 ceiling), with one enforced SQLite encoding.

### D13. The host owns migration; the engine emits and verifies

Per-dialect DDL ships as embedded files with an accessor, so hosts feed it to their own migration tool. A `VerifySchema` call inspects the live schema at startup — including collation — and reports every discrepancy. A minimal runner exists for tests and development only, never automatic in production.

*Why not ship a migration runner:* it would pick a migration tool on the adopter's behalf, fight the pipeline they already have, and run DDL at startup in production, which many organisations forbid.

*Why not merely document the schema:* the conformance suite must build these schemas on all three dialects anyway, so the DDL exists in the repository regardless. Not exporting it means every adopter reinvents something already maintained.

Table names carry a configurable prefix — free on day one, a breaking change later.

### D14. Conformance suites are the mechanism that makes ten adapters affordable

`storetest.RunSuite(t, factory)` and `transporttest.RunSuite(t, mount)` are exported packages. Each adapter's test file is a few lines: start the dependency, run the suite. PostgreSQL and MySQL via testcontainers, SQLite in-process.

The suites must pin the cases where adapters will otherwise diverge: nested `Do` joins rather than nesting (the GORM default is the other way), rollback on error *and* on panic with the panic re-raised, after-commit hooks silent on rollback, sink writes inside the transaction, cancelled context rolling back, repository and transactor sharing a connection, concurrent claim producing exactly one winner, and case-sensitive identifier comparison.

This is also the executable form of the decoupling litmus test from the source material.

### D15. The transport seam sits below `http.Handler`

Gin is `net/http`-based, so a gin binder is nearly free. **Fiber v3 is fasthttp-based** and not `net/http`-compatible; routing every endpoint through a fasthttp↔net/http conversion imposes exactly the overhead fiber users chose fiber to avoid.

So the seam is a DTO boundary, not a handler boundary: each binder only extracts path, query and body into a request DTO and writes back a response DTO. Route table, validation, error-to-status mapping, pagination and the generated OpenAPI document live in `transport/core`.

### D16. `Status` says where, payload says what

Ten states, with `SUSPENDED` carrying a `suspended_from` column because the resume target is not derivable. "Approval denied" is a `COMPLETED` task with a negative output — never `FAILED`.

That fixes the boundary for the other two terminal faults: `FAILED` is actor-originated ("I cannot do this"), `ERROR` is system-originated (assignment resolution failed, output unvalidatable). Status therefore carries no business meaning at all, which is what lets a consumer be written without knowing the task's domain.

### D17. Partial saves use JSON Patch; history stays coarse

`SaveProgress` accepts an RFC 6902 JSON Patch. It handles nested structures and arrays that a shallow key merge cannot, and it *is* the field-level audit record — no before/after blobs needed.

Transition history records one row per lifecycle transition, not per save. Patch operations are recorded separately, so autosave traffic does not drown the transition log.

### D18. Live candidate evaluation, not a creation-time snapshot

Eligibility resolves group membership at the moment of the operation. Someone who joins `finance-approvers` today can act on a task created yesterday.

*Trade-off accepted:* the `GroupResolver` is on the path of claim, delegate and inbox queries, so hosts with a slow directory will need to cache behind the port. Snapshotting would be faster but makes membership changes invisible to existing work, which is wrong for the organisational workflows this exists to serve.

### D19. Identity, versions and tooling

- Task identifiers are UUIDv7 by default — time-ordered, so they index well and page stably — generated behind a port so a host can substitute ULIDs or its own scheme. Stored as an opaque string column for dialect portability.
- The REST contract is defined by `transport/core`'s route table, with the OpenAPI document generated from it rather than hand-maintained alongside it.
- Go baseline: a version providing generics and `context.WithoutCancel`, tracking the two most recent Go releases.
- `go.work` coordinates the modules in development; each module is released and tagged independently.

## Risks / Trade-offs

- **Ten modules before a single adopter.** → Seams are designed for all of them up front, since that is the irreversible part; module release order is sequenced in `tasks.md` so core and one store land first and prove the shape before the rest follow.
- **Three public contracts from first release** — Go API, REST contract and database schema. A bad REST URL cannot be fixed with a deprecation and a compiler error. → Generate OpenAPI from the route table so drift is impossible, and version the REST surface from the first release rather than retrofitting.
- **GORM's savepoint-nesting default silently differs from the other drivers.** → A conformance case that fails if an inner rollback leaves the outer transaction alive.
- **MySQL's case-insensitive default collation changes who may claim a task.** → Collation pinned in DDL and asserted by both `VerifySchema` and a conformance case.
- **In-process-only hosts can lose events.** At-least-once requires the durable record; a host that skips it gets best-effort delivery. → Refuse that configuration at startup rather than degrade silently.
- **Fiber v3's maturity is a dependency risk** the other bindings do not carry. → The binder is thin and isolated in its own module; it can lag or be dropped without touching the contract.
- **Live group resolution puts a host system on the inbox query path.** → Documented as a caching responsibility of the port implementation, with a static implementation shipped for tests.
- **Multi-module tagging and release ordering is ongoing operational cost.** → Accepted deliberately: it keeps `go get hmntsk` free of pgx, gorm, gin and fiber, which is the point of the split.

## Migration Plan

Not applicable — greenfield, no existing deployment, no data to migrate. Adopters apply the published DDL through their own migration pipeline and run `VerifySchema` at startup.

## Open Questions

- **MariaDB as a fourth dialect.** MySQL-shaped but with `RETURNING` and different collation defaults, so it is a distinct dialect wearing a disguise. Deferrable: adding a dialect does not change the seam, the specs or the task breakdown.
- **Webhook delivery mechanics.** Storage and verbatim echo of callback targets are specified; retry policy, backoff and dead-lettering are not, and no delivery module ships in this change. Deferrable: it is a consumer of the event stream, not a change to it.
- **Per-task event ordering guarantees.** At-least-once is committed; whether consumers additionally get per-task ordering depends on the transport a host relays to. Deferrable: it constrains the relay, which the host owns.
- **Built-in metrics and tracing.** Whether the engine emits its own instrumentation or leaves it entirely to event consumers and the host's middleware.
