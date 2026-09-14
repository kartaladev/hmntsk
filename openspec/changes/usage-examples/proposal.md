## Why

A consumer evaluating hmntsk today has three `Example` functions and a set of guides. Nothing shows the pieces wired together into the thing they actually build: an inbox whose items link back to business records, delivered over HTTP, with events leaving the process and people being notified. Each guide also states a default and an override, but no running code demonstrates both side by side, so the library's central promise, opinionated defaults that the consumer can replace, has to be taken on trust.

The inbox, task-read authorization and notification changes have now landed on main, so every feature a sample would demonstrate exists.

## What Changes

- **New module `examples/`** (`github.com/kartaladev/hmntsk/examples`), in `go.work`, built, linted and tested with every other module, and **never tagged or released**.
- **One fictional domain, invoice approval,** shared by every scenario, so a reader learns the domain once and then sees only the feature.
- **Runnable scenarios**, each `go run`-able and each proven by its own test. Every scenario shows the default first, then a consumer override:
  - `quickstart`: defaults only, one task's life in memory.
  - `correlated-tasks`: a domain creates correlated tasks inside its own SQLite transaction.
  - `inbox-buckets`: buckets as queries, orderings, exact paging, counts; over HTTP the self-only default, then a supervisor policy for a team queue.
  - `record-page`: every task for one invoice, and the participants-only read default, then an auditor read policy.
  - `context-links`: type metadata and route expansion, then host-defined keys and a host link resolver.
  - `schema-form`: served schemas, progress under the relaxed schema, completion under the full one, then the typed facade.
  - `escalation`: the sweeper applying a type's default escalation, then a per-task override.
  - `event-delivery`: an in-process event handler, then the relay with a signed webhook sink verified by the receiver, including the destination policy refusing loopback by default.
  - `notifications`: task events projected into notifications, listed, counted, read and streamed; then custom links and titles, a supervisor subscription policy, a retention strategy, and email through a host mailer.
- **Second round, so that every capability spec has an example** (added at the user's request after a coverage review found about half of the library's behaviours unshown):
  - `lifecycle-operations`: release, delegate, suspend and resume, fail, cancel, a stale-version conflict, auto-reserve for a single candidate, `ERROR` for none, excluded users, auto-start on the first progress save.
  - `schema-migrations`: the published migration statements, a table prefix, a failing schema verification, nested transaction scopes.
  - `notify-standalone`: `notify` without tasks: publishing, idempotency, coalescing, closing with a successor, version watermarks, mark all read.
  - `http-frameworks`: the same task contract and notification handlers served by Gin and by Fiber.
  - `escalation`, `event-delivery` and `notifications` extended with the behaviours they did not yet show (direct escalation, exemption, caps and supersession; retry with backoff, independent sinks, the audience snapshot; release, delegation and escalation projection, custom rules and closing statuses, email filtering, age retention and the default eviction strategy).
  - **Service-backed scenarios**, which need a real server: `store-drivers` (PostgreSQL and MySQL through `store/sql`, `store/pgx` and `store/gorm`), `event-bus` (Redis streams, NATS subjects and JetStream), `realtime-scaling` (Redis and NATS broadcasters across two instances, and the WebSocket endpoint). Their tests provision servers with the repository's testcontainers helpers; `go run` takes a server address from the environment.
- **A README in every example directory** saying what the example shows, the context it assumes, and how to run it.
- **One browser demo, `inbox-ui`:** a React single-page app built with Material UI (MUI), so the demo looks like something an end user would want, served by a Go program, showing buckets with counts, urgency ordering, route links, a schema-driven form and a live notification badge. It is labelled as an illustration, not a UI library, and runs with `go run` without Node installed.
- **Tooling:** a Makefile group for the examples module, kept out of the release order; CI runs the scenarios and checks the committed UI bundle is current.
- **Docs:** `examples/README.md` maps each feature to the scenario that shows it; the root README and guides link to it.

No library module's API or behaviour changes.

## Capabilities

### New Capabilities

- `usage-examples`: runnable, tested scenarios that demonstrate each library capability with its default and a consumer override, without external services, plus a browser demo of a contextual inbox; kept buildable on every change and never released.

### Modified Capabilities

None.

## Impact

- **New code:** `examples/` only, including `examples/inbox-ui/web/` (TypeScript, React, Vite) and its committed build output.
- **Repository tooling:** `go.work`, `Makefile` (new module group, excluded from `RELEASE_ORDER`), `.github/workflows/ci.yml` (scenario tests, UI bundle freshness, UI unit tests), `docs/releasing.md` (examples are never tagged).
- **Docs:** `README.md`, `docs/inbox.md`, `docs/notifications.md`, `docs/delivery.md` gain links to scenarios.
- **Dependencies:** the examples module requires the repository's own modules through `go.work` and `modernc.org/sqlite`; the UI adds a Node toolchain for contributors who change it, and to CI.
- **Coupling:** `notify` and `sqlkit` import paths change once when they move to their own repositories; the examples move with that change. Behaviour changes in any library module surface as a failing scenario test.
