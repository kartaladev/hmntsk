# hmntsk examples

Runnable scenarios that show how to wire hmntsk into an application. Every one
uses the same made-up business, invoice approval, so you learn the domain once
and then see only the feature.

hmntsk is opinionated: everything has a working default. It also leaves you in
control: every default can be replaced. So each scenario prints a **default**
section first, then one or more **override** sections that configure the same
capability the way a real host might.

Every scenario is also a test (`main_test.go` runs it and checks what it
prints). A scenario that stops showing what it claims fails the build. Each
directory has its own README with the context and the exact commands.

This module is developed in this repository and **never tagged**. It is
documentation that compiles; nothing should require it.

## The shared domain

- **Task types:**
  - `invoice.review` checks an invoice against its order;
  - `invoice.approve` approves or rejects payment, and widens to the finance
    managers when overdue.
- **People:**
  - `alice` and `bob` are in `finance-approvers`;
  - `carol` is in `finance-managers`;
  - `dave` is in `auditors` and takes part in no task.
- **Creator:** `billing-service` creates tasks.
- **Correlation:** always `ownerType=invoice`, `ownerRef=<invoice number>`,
  `activityKey=review|approve`.

## Running

From this directory:

```sh
go run ./quickstart      # any scenario
go test ./...            # every scenario (service-backed ones need Docker)
go run ./contextual-ui   # the browser demo, then open http://127.0.0.1:8080
```

Most scenarios need nothing but Go. Three demonstrate a real server; see
[Services](#services).

## Which scenario shows what

### Tasks

| You want to see | Scenario |
| --- | --- |
| A task's lifecycle, created → claimed → started → completed, with defaults only | [`quickstart`](quickstart) |
| Every other lifecycle operation: release, delegate, suspend and resume, fail, cancel; a stale-version conflict and an illegal transition refused; progress saves starting a task | [`lifecycle-operations`](lifecycle-operations) |
| Assignment: a single eligible candidate reserved at creation, `ERROR` when nobody is eligible, excluded users | [`lifecycle-operations`](lifecycle-operations) |
| Served task-type schemas, saving progress, full validation on completion, the typed facade and its typed completion handler | [`schema-form`](schema-form) |
| Type metadata (`hmntsk.route`, `hmntsk.formKey`), `ExpandRoute` and escaping, host metadata keys and a link resolver | [`context-links`](context-links) |
| Escalation by the sweeper under a type's policy and a per-task one; direct escalation; exempting started work; an escalation cap; superseding into `OBSOLETE` | [`escalation`](escalation) |

### Storage and transactions

| You want to see | Scenario |
| --- | --- |
| Tasks correlated to a record, created in the host's own transaction (commit and rollback) | [`correlated-tasks`](correlated-tasks) |
| The published migration statements, a table prefix, schema verification reporting a mismatch, nested transaction scopes | [`schema-migrations`](schema-migrations) |
| The task store on PostgreSQL and MySQL through `store/sql`, `store/pgx` and `store/gorm`, each with its own host transaction type | [`store-drivers`](store-drivers) (Docker) |

### Inbox and HTTP

| You want to see | Scenario |
| --- | --- |
| Buckets as queries: every ordering, exact paging, a cursor bound to its ordering, bucket counts | [`inbox-buckets`](inbox-buckets) |
| Self-only inbox queries over HTTP, and a policy letting a supervisor read a team queue | [`inbox-buckets`](inbox-buckets) |
| Every task for one record, participants-only single-task reads, an auditor read policy | [`record-page`](record-page) |
| The same task contract and notification handlers served by Gin and by Fiber | [`http-frameworks`](http-frameworks) |
| Tasks inside an application's own pages: sign-in, an order approved, its purchase order sent or uploaded, and its invoice reviewed and approved, on an order page reached through `hmntsk.route`; schema forms and the application's own form chosen by `hmntsk.formKey`, workflow steps run by a relay sink after commit, data grids, inbox buckets with counts, a live notification badge | [`contextual-ui`](contextual-ui) |

### Events and delivery

| You want to see | Scenario |
| --- | --- |
| In-process event handlers; the relay delivering to a signed, verified webhook; the default destination policy refusing loopback; retry with backoff; two sinks accepting independently; the audience snapshot every event carries | [`event-delivery`](event-delivery) |
| Events published to a Redis stream with a retention bound, to NATS subjects and to JetStream | [`event-bus`](event-bus) (Docker) |

### Notifications

| You want to see | Scenario |
| --- | --- |
| Task events projected into notifications, listed, counted, read and streamed; custom links and titles; a supervisor's stream; retention; email through your own mailer; the SQLite notification store | [`notifications`](notifications) |
| Notifications on release, delegation and widening escalation; custom rules and closing statuses; email kind filtering and recipients without an address; age retention and the default eviction strategy | [`notifications`](notifications) |
| `notify` on its own, without tasks: idempotent publishing, coalescing, closing with a successor, version watermarks, marking all read | [`notify-standalone`](notify-standalone) |
| Change signals shared across instances through Redis and NATS broadcasters, and the WebSocket endpoint with its origin check and mark-read requests | [`realtime-scaling`](realtime-scaling) (Docker) |

The guides explain the rules behind each scenario:
[inbox](../docs/inbox.md), [notifications](../docs/notifications.md),
[delivery](../docs/delivery.md), [schema](../docs/schema.md),
[notify](../notify/docs/notifications.md) and
[realtime operations](../notify/docs/realtime-operations.md).

## Services

Three scenarios exist to show a real server:

- `store-drivers` needs PostgreSQL and MySQL;
- `event-bus` needs Redis and NATS with JetStream;
- `realtime-scaling` needs Redis and NATS.

**Their tests** start the servers themselves in containers, through the
repository's testcontainers helpers, so `go test` only needs Docker running.

**`go run`** takes each server's address from the environment and says exactly
what to set when one is missing:

| Setting | Used by |
| --- | --- |
| `HMNTSK_POSTGRES_DSN` | `store-drivers` |
| `HMNTSK_MYSQL_DSN` | `store-drivers` |
| `HMNTSK_REDIS_ADDR` | `event-bus`, `realtime-scaling` |
| `HMNTSK_NATS_URL` | `event-bus`, `realtime-scaling` |

Each scenario's README has the `docker run` commands that start them.

## Shortcuts the scenarios take on purpose

- **Identity:** a request header (or, in `contextual-ui`, a cookie set by a
  password-less sign-in) says who is asking. That stands for your
  authentication middleware. It is not authentication, and hmntsk trusts
  whatever actor you establish.
- **One pass instead of a loop:** scenarios call `Relay.Relay`, `Sweeper.Sweep`,
  `Pruner.Prune` and `EmailDispatcher.Dispatch` once, so the output is the same
  on every run. A host runs the `Run` form of each on an interval.
- **Migrations:** most scenarios call `Migrate`. A host applies the published
  schema through its own migration tool and calls `VerifySchema` at startup;
  `schema-migrations` shows that.
- **Fixed clock and masked identifiers:** these keep the printed output stable.
  Don't copy them into a host.

## `contextual-ui`'s page

The page is a React and Material UI app in
[`contextual-ui/web`](contextual-ui/web). Its build is committed in
`contextual-ui/dist` and embedded in the Go program, which is why `go run`
needs no Node. It illustrates the HTTP contracts; it is not a component
library, and its schema form handles only flat forms.

Changing the page needs Node 22.12 or newer. From the repository root:

```sh
make ui-test     # page matching, workflow steps, cursor paging, uploads, route expansion, the form walker, bucket queries (Vitest)
make ui-build    # typecheck and rebuild contextual-ui/dist; commit the result
```

CI rebuilds the page and fails if the committed build does not match its source.
