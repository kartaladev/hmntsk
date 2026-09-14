# hmntsk

An embeddable human task engine for Go.

Applications with human decision steps — approvals, reviews, manual data entry —
each end up reimplementing the same machinery: a task inbox, a claim-and-complete
lifecycle, deadlines and escalation, and some way to tell whatever started the
work that it has finished. That machinery is generic. This is an implementation
of it that stays out of the way of your orchestrator, your database driver and
your web framework.

It is a **library the host embeds**, not a service you deploy. The host already
owns a database handle, a transaction boundary, an identity system and an HTTP
router; the engine owns the task lifecycle and depends on nothing above it. That
is what makes it possible to commit a task change and a business change in one
transaction — which a separate service could not do at all.

```go
svc, err := hmntsk.New(store, hmntsk.WithGroupResolver(directory))
```

## What it does

- A **ten-state lifecycle** — `CREATED`, `READY`, `RESERVED`, `IN_PROGRESS`,
  `SUSPENDED`, `COMPLETED`, `FAILED`, `ERROR`, `EXITED`, `OBSOLETE` — with an
  explicit transition table and twelve operations that drive it.
- **Status says where, the payload says what.** A denied approval is a
  `COMPLETED` task with a negative output, never a `FAILED` one. That is what
  lets a consumer be written without knowing the task's domain.
- A **mandatory type registry**: input and output JSON Schema, default priority,
  deadline, escalation policy and assignment, per kind of work.
- **Candidate pools** of users, groups and exclusions, with membership resolved
  through your directory at the moment of the operation, not snapshotted when
  the task was created.
- **A contextual inbox** from plain queries: priority, due-date and urgency
  ordering with exact paging, counts for badges, team queues, and type metadata
  that links a task to its business form.
- **Events, not calls.** The engine publishes; it never invokes host business
  logic. Events are written to an outbox inside your transaction and dispatched
  after it commits.
- **Deadlines and escalation**, swept by a runner you start, claimed by a lease
  that needs no row locks.
- Optional **typed access** through `Kind[In, Out]` for Go hosts, over the same
  untyped core the storage and the HTTP transports use.

## Modules

Each is released and tagged independently, so `go get github.com/kartaladev/hmntsk`
pulls in no driver and no web framework.

| Module | What it is |
| --- | --- |
| `github.com/kartaladev/hmntsk` | The engine: domain, state machine, ports, event relay |
| `.../store/sqlcore` | Dialect-aware SQL and the published DDL. Executes nothing |
| `.../store/sql` | `database/sql` adapter |
| `.../store/pgx` | `jackc/pgx` adapter |
| `.../store/gorm` | GORM adapter — transaction participation, no models |
| `.../transport/core` | The REST contract: routes, DTOs, error mapping, OpenAPI |
| `.../transport/http` | `net/http` binding |
| `.../transport/gin` | gin binding |
| `.../transport/fiber` | Fiber v3 binding |
| `.../delivery/webhook` | Webhook sink: signs, echoes reference parameters, refuses internal addresses |
| `.../delivery/redis` | Redis Streams sink, producer only; optional length or age retention |
| `.../delivery/nats` | NATS sinks, producer only: plain subjects, or JetStream with broker-side de-duplication |
| `.../storetest` | The suite every store adapter must pass |
| `.../transporttest` | The suite every transport binding must pass |
| `.../relaytest` | The suite every relay must pass, on every dialect |
| `.../tasknotify` | Turns task events into per-user notifications: offers, taken notices, assignments, closes |

Drivers and dialects are orthogonal, and the matrix is sparse because pgx is
PostgreSQL-only:

|  | PostgreSQL | MySQL | SQLite |
| --- | :---: | :---: | :---: |
| `database/sql` | ✓ | ✓ | ✓ |
| `pgx` | ✓ | — | — |
| `gorm` | ✓ | ✓ | ✓ |

All seven combinations run the same conformance suite — both of them: the
storage suite and the relay suite, because claiming and retry scheduling are
storage behaviour too.

## Wiring

```go
// One value satisfying Repository, Transactor, EventSink and OutboxStore.
// They are one value on purpose: ports that must share a connection, wired
// independently, is a mistake that compiles cleanly and shows up in
// production as a transaction that silently split in two.
store := sqlstore.New(db, sqlcore.PostgreSQL)

svc, err := hmntsk.New(store,
    hmntsk.WithGroupResolver(directory),   // your identity system
    hmntsk.WithEventHandlers(consumers...), // in-process consumers
    hmntsk.WithClock(clock),                // usually the default
)
```

A consumer that needs the engine it observes — a typed completion handler from
`Kind.OnCompleted`, or one that creates a follow-up task — is built from the
service during construction, with no variable assigned after `New` returns:

```go
svc, err := hmntsk.New(store,
    hmntsk.WithEventHandlerFactory(func(svc *hmntsk.Service) ([]hmntsk.EventHandler, error) {
        approvals, err := hmntsk.Define[Request, Decision](svc, spec)
        if err != nil {
            return nil, err // fails New
        }

        return []hmntsk.EventHandler{approvals.OnCompleted(onDecision)}, nil
    }),
)
```

Consumers run in registration order, factories included. A factory may
configure the service (`Register`, `Define`) but must not run lifecycle
operations: `New` has not returned, and later consumers are not attached yet.

`New` refuses a store whose event sink cannot join your transaction. That
configuration works in tests and loses events in production, exactly when they
matter, so it fails at startup instead.

Register the kinds of work before serving traffic:

```go
err := svc.Register(hmntsk.TypeSpec{
    Name:            "approval",
    InputSchema:     inputSchema,
    OutputSchema:    outputSchema,
    DefaultDeadline: 24 * time.Hour,
    DefaultEscalation: &hmntsk.EscalationPolicy{
        Action: hmntsk.EscalationWiden, AddGroups: []string{"managers"},
    },
})
```

Registering the same name twice with different configuration fails here, at
wiring time, rather than at first use.

## The transaction contract

**Whoever begins, commits.** The engine never commits or rolls back a
transaction it did not start.

**Engine-led** is the default. Call an operation outside any transaction and the
engine opens one, applies the change, writes the events, commits, and only then
delivers to in-process consumers:

```go
result, err := svc.Complete(ctx, hmntsk.CompleteRequest{
    TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: actor, Version: &version},
    Output:      output,
})
```

**Host-led** is how a task change and a business change commit together. Put your
open transaction on the context and the engine joins it:

```go
tx, err := db.BeginTx(ctx, nil)
ctx = sqlstore.ContextWithTx(ctx, tx)

result, err := svc.Complete(ctx, req)   // joins your transaction
if err != nil {
    tx.Rollback()
    return err
}

if err := writeYourOwnRows(ctx, tx); err != nil {
    tx.Rollback()
    return err
}

if err := tx.Commit(); err != nil {
    return err
}

// The engine could not observe your commit, so it handed the dispatch back.
// Run it after committing, never before.
return result.Dispatch(ctx)
```

**Nested scopes join and flatten.** An operation invoked from inside another
shares one transaction: no second transaction, no savepoint, so an inner failure
aborts the whole scope. This is not GORM's default, and the GORM adapter
overrides it deliberately.

**Every mutation is conditional on the version you last read.** Supply
`Version` and a stale write is refused with a `*hmntsk.ConflictError` naming the
current version, so a client can re-read, merge and retry.

## Events

Lifecycle operations produce events from a closed catalogue of thirteen — one
per accepted transition, and none at all for a progress save. They are written
to the outbox in the same transaction as the state change, and delivered to
in-process handlers after it commits, on a context detached from the request's
cancellation: a client hanging up in the instant after a commit must not drop
the notification.

Nothing in an event names a caller-specific type. A consumer routes on
`CorrelationData` — `OwnerType`, `OwnerRef`, `ActivityKey` — which the host
supplied at creation and the engine echoes back unchanged.

## Delivering events

At-least-once delivery needs a relay reading the outbox table, and `relay` is
it. Due events are claimed by a time-bounded lease, so relays in several
instances do not deliver the same event twice; a retryable failure is
rescheduled with an exponential, jittered delay; and an event that exhausts its
attempts, or that a sink rejects permanently, is dead-lettered and retained with
its attempt count and last error rather than retried forever.

```go
r, err := relay.NewRelay(svc,
    relay.WithSinks(hook, bus),
    relay.WithMaxAttempts(8),
)
go r.Run(ctx, 10*time.Second)   // yours to start, and yours to stop
```

Like the sweeper, it starts nothing on its own. Each event is fanned out to
every configured sink and acceptance is tracked per sink, so a broker outage
does not re-POST to a webhook that already succeeded.

Three sink modules ship, each on its own so that neither reaches a host that does
not import it:

| Module | Delivers to |
| --- | --- |
| `delivery/webhook` | The task's `CallbackTarget.Address`, with reference parameters echoed verbatim, an HMAC signature over the timestamp and body, and a default-deny policy on the resolved destination address |
| `delivery/redis` | A Redis Stream, for internal consumers — producer only: no consumer groups, no offsets. Unbounded by default; optionally trimmed by length or age on each publish, with a trim mode (Redis 8.2+) deciding whether unacknowledged entries may go — see the constraints in [docs/delivery.md](docs/delivery.md#bounding-the-redis-stream) |
| `delivery/nats` | NATS, for internal consumers — producer only, in two modes with two sink names. `NewSink` publishes to plain subjects: delivered means the server received it, so an event with no subscriber is lost. `NewJetStreamSink` publishes to JetStream: delivered means a stream stored it, de-duplicated on the event ID, and the sink never creates the stream — see [docs/delivery.md](docs/delivery.md#publishing-to-nats) |

Because a callback address is supplied by whoever created the task, the webhook
sink refuses to connect to a destination its policy rejects, evaluated against
the **resolved address at dial time** rather than the URL text. The default
rejects loopback, link-local — the cloud metadata address included — private
ranges, unique-local IPv6 and the unspecified address, and does not follow
redirects. A host that wants an internal destination supplies its own policy.

## Escalation

Deadlines are evaluated off the request path. Reading or querying an overdue
task leaves it exactly as it was.

```go
sweeper, err := hmntsk.NewSweeper(svc, hmntsk.WithLeaseDuration(5*time.Minute))
go sweeper.Run(ctx, time.Minute)   // yours to start, and yours to stop
```

Nothing starts on its own: constructing an engine or a sweeper begins no
goroutine, no timer and no polling. Overdue tasks are claimed by a time-bounded
lease taken with a conditional update, so two instances sweeping at once
escalate each task exactly once — and so that it works identically on a database
with no row-level locking at all.

## Inbox

A bucket is a query you name. Order it, count it, and link its tasks to where
the work is done:

```go
page, err := svc.Query(ctx, hmntsk.Query{Candidate: actor, OrderBy: hmntsk.OrderUrgency})
counts, err := svc.CountBuckets(ctx, map[string]hmntsk.Query{
    "mine": {Assignee: actor},
    "team": {Group: "finance-approvers"},
})
link := hmntsk.ExpandRoute(spec.Metadata[hmntsk.MetadataRoute], task)   // raw: escape it yourself
```

Creation order is the default and paging is exact under every ordering; tasks
without a deadline sort last. See [docs/inbox.md](docs/inbox.md) for buckets,
orderings and their limits, counts, team queues, metadata keys and query
authorization.

## HTTP

```go
api, err := transportcore.New(svc)          // the contract
handler, err := httptransport.Handler(api)  // or gintransport.Engine / fibertransport.App
```

The contract is identical across all three bindings, and
[`transport/core/openapi.json`](transport/core/openapi.json) is generated from
the route table so the two cannot drift apart.

Authentication, authorisation of the caller's identity, rate limiting, CORS and
TLS are yours. The engine takes the acting actor as an input your middleware
established:

```go
ctx := httptransport.ContextWithActor(r.Context(), whoeverYouAuthenticated)
```

Inbox queries and counts (`GET /tasks`, `GET /tasks/count`) are **self-only by
default**: `candidate=me` and `assignee=me` name that actor, and a query for
anyone else's inbox, a group's queue, or nobody in particular is `403`. Replace
the policy for supervisors, admins or your own permission system:

```go
api, err := transportcore.New(svc, transportcore.WithQueryAuthorizer(yourPolicy))  // or transportcore.AllowAll
```

Reading one task and its history (`GET /tasks/{id}`, `GET /tasks/{id}/history`)
is **participants-only by default**: the holder, the creator or an eligible
candidate may read it, and a read with no acting user is `403`. Replace that
policy the same way:

```go
api, err := transportcore.New(svc, transportcore.WithTaskReadAuthorizer(yourReadPolicy))  // or transportcore.AllowAll
```

**Unreleased breaking changes:** a client that queried another actor's inbox now
needs a policy that permits it, a client that read a task it takes no part in
now needs a read policy that permits it, and the direction parameter is now
`direction=asc|desc` beside `orderBy`, replacing `order`.

Errors map predictably: `409` for a concurrent-modification conflict and for an
illegal transition, `403` for a failed eligibility or assignee check, a refused
query or a refused read, `404` for
an unknown task or route, `400` for a schema or request validation failure and
for an unregistered task type. A failure to reach your directory is a `500` and
never a `403` — the engine could not decide, which is not the same as deciding
against the caller.

## Schema

The engine publishes its schema per dialect; your migration tool applies it. It
never runs DDL for you in normal operation.

```go
statements, err := sqlcore.New(sqlcore.PostgreSQL).Migrations()
err = store.VerifySchema(ctx)   // at startup, before serving traffic
```

See [docs/schema.md](docs/schema.md) for the tables, the table prefix and the
per-dialect workflow.

## Documentation

- [docs/inbox.md](docs/inbox.md) — buckets, ordering, counts, team queues, metadata, query authorization
- [docs/notifications.md](docs/notifications.md) — notifying people about their tasks: rules, closing statuses, links, failures
- [docs/schema.md](docs/schema.md) — tables, prefix, migration workflow
- [docs/releasing.md](docs/releasing.md) — module tagging scheme and release order
- Runnable examples: `Example`, `Example_hostLedTransaction`, `Example_typedFacade`
  in [`example_test.go`](example_test.go)
- [examples/](examples/README.md): runnable, tested scenarios for each capability,
  each showing the default and then an override, plus a browser demo of a
  contextual inbox

## Development

```sh
make lint              # golangci-lint over every module
make test              # every module's tests, containers included
make test-integration  # the same, with the time a container run needs
make test-race         # the same, under the race detector
make store-matrix      # the storage suite over all seven driver/dialect pairs
make transport-matrix  # the transport suite over all three bindings
make vuln              # govulncheck over every module
make release-order     # the order the modules must be tagged in
```

Tests that need PostgreSQL or MySQL start them with testcontainers and need a
working Docker daemon. They are deliberately not behind a build tag: a tag is
how integration tests end up broken for a fortnight without anyone noticing.
