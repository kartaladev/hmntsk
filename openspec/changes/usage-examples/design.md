## Context

See proposal.md for motivation. Constraints found on main at 2ca7c4c:

- **27 modules in `go.work`.** The Makefile groups them as `HMNTSK_MODULES`, `SQLKIT_MODULES` and `NOTIFY_MODULES`; `MODULES` is their union unless `GROUP` narrows it. `RELEASE_ORDER` lists hmntsk modules only; sqlkit and notify are split out before tagging.
- **`TestReleaseOrderCoversEveryModule`** (`docs_test.go`) compares a hardcoded list of hmntsk modules with `make release-order`. It does not read `MODULES` or `go.work`, so a module that is in the workspace but not in the release order does not break it.
- **Satellite `go.mod` files** carry no `require` for sibling modules; `go.work` supplies them until tagging. `go mod tidy` and `make tidy` must never run.
- **`make split-check`** constrains only sqlkit and notify imports; an examples module importing everything is allowed.
- **CI:** the `unit tests` job loops over an explicit module list; `race detector` and `govulncheck` run `make` over all `MODULES`; `generated artefacts are current` regenerates and runs `git diff --exit-code`.
- **Defaults that the scenarios must work with, not around:**
  - `GET /tasks` and `/tasks/count` are self-only (`transportcore.SelfOnly`).
  - `GET /tasks/{id}` and `/history` are participants-only (`transportcore.ParticipantsOnly`), 403 with no actor.
  - The webhook sink's `DefaultPolicy` refuses loopback, so an `httptest` receiver needs `webhook.AllowLoopback()`.
  - `notify.NewHandler` requires `WithActor`; the stream authorizer defaults to `notify.SelfOnly`.
  - Relay, sweeper, hub, pruner and email dispatcher start nothing on their own.
- **Local toolchain:** Go pinned by `GOTOOLCHAIN=go1.26.8`; Node 26 and npm are available locally; CI has no Node step today.

## Goals / Non-Goals

**Goals:**

- A reader can open one directory per feature, run it, and read top to bottom: wiring, default, override.
- Every scenario is a regression test for the library's documented behaviour.
- The browser demo shows the whole contextual-inbox loop in one place.

**Non-Goals:**

- ~~Scenarios for Redis, NATS, WebSocket, PostgreSQL, MySQL, pgx, GORM, Gin and Fiber.~~ Reversed in the second round at the user's request: see decision 10.
- A reusable UI component library, authentication, or production hardening of the demo.
- Any change to a library module. A gap a scenario exposes is reported and fixed in its own change.

## Decisions

### 1. Module and layout

```
examples/                        module github.com/kartaladev/hmntsk/examples
  go.mod                         no sibling requires; modernc.org/sqlite only
  README.md                      feature → scenario index
  internal/invoicing/            shared fictional domain
  internal/demo/                 shared scenario helpers (sections, fixed clock, deterministic IDs)
  quickstart/
  correlated-tasks/
  inbox-buckets/
  record-page/
  context-links/
  schema-form/
  escalation/
  event-delivery/
  notifications/
  contextual-ui/
    main.go, server.go, ...      Go server
    web/                         React + TypeScript source (Vite)
    dist/                        committed build, embedded with go:embed
```

One module rather than one module per scenario keeps `go.work`, the Makefile and CI to a single entry. `internal/` keeps the shared domain from looking like API. Directory names are kebab-case for readability on disk; package names are `main`.

- **Alternative, examples inside each library module:** scatters the story across 27 modules and cannot show cross-module wiring. Rejected.
- **Alternative, `Example` functions in the root module:** cannot import `store/sql`, `relay`, transports or notify without breaking the core's dependency direction (`depdirection_test.go`). Rejected.

### 2. Scenario shape: `run(ctx, w)` plus a test

Each scenario's `main.go` holds only `main`, which calls `run(ctx, os.Stdout)` and exits non-zero on error. All logic lives in `run(ctx context.Context, w io.Writer) error` in the same package. `main_test.go` calls `run(t.Context(), &buf)` and compares `buf` with an expected transcript.

- **Expected output is a Go string literal in the test,** not a golden file. The transcript sits beside the code a reader is studying, and an intended change is a visible diff. Scenarios keep transcripts short (roughly 10–40 lines).
- **Sections:** `internal/demo` provides `Section(w, title)` so every scenario prints `== default: ...` and `== override: ...` headers identically, which is what the "default, then override" requirement is checked against.
- **Why not `Example` functions with `// Output:`:** a `main` package cannot hold `Example` tests that `go run` also shows, and examples with HTTP servers need setup that reads poorly in `Example` form.

### 3. Determinism

- **Clock:** scenarios that print time-dependent outcomes (due dates, escalation, retention) pass a fixed clock through `hmntsk.WithClock` and `notify.WithClock`, advanced explicitly.
- **Identifiers:** transcripts never print task, event or notification IDs. Where a link contains one, the scenario prints it with the ID replaced by a stable placeholder via `internal/demo`. Scenarios do not override ID generation, because that would teach readers to do so.
- **Background loops:** scenarios call the single-pass methods (`Relay.Relay`, `Sweeper` single sweep, `Pruner.Prune`, `EmailDispatcher.Dispatch`) instead of `Run` loops, so ordering is deterministic. Each scenario's comments point at the `Run` form a host uses.
- **HTTP:** `main` listens on `127.0.0.1:0` and prints no port; the test uses `httptest.NewServer` through the same `run`, because `run` constructs the handler and serves it on a listener it opens itself. Requests are made by a small client in the scenario whose printed lines are `METHOD path → status` plus selected body fields.

### 4. Shared domain: invoice approval

`internal/invoicing` defines an `Invoice` record (ID, supplier, amount, due date), an in-memory and a SQL-backed invoice repository, two task types (`invoice.review` with priority and deadline defaults, `invoice.approve` with a candidate group `finance-approvers` and escalation to `finance-managers`), their JSON schemas, route metadata (`/invoices/{correlation.ownerRef}/{correlation.activityKey}?task={task.id}`), and a `hmntsk.StaticAssignment` directory (`alice`, `bob` in `finance-approvers`; `carol` in `finance-managers`; `dave` an auditor). Correlation is always `ownerType=invoice`, `ownerRef=<invoice ID>`, `activityKey=<review|approve>`, matching the specs' own examples (`INV-42`, `finance-approvers`, `carol`).

### 5. Scenario content

Each row is what the scenario's default and override sections demonstrate.

| Scenario | Default | Override |
| --- | --- | --- |
| `quickstart` | memstore, static directory, register, create → claim → start → complete | none (defaults only) |
| `correlated-tasks` | SQLite (`store/sql`, `sqlcore.SQLite`), `Migrate` + `VerifySchema`; the invoice row and its review task commit in one host transaction via `sqlstore.ContextWithTx`; events dispatch after commit | host rolls back: neither invoice nor task exists, no event dispatched |
| `inbox-buckets` | `Service.Query` with every ordering, paging with a cursor, a cursor from another ordering refused, `CountBuckets` for mine/available/overdue; HTTP: `candidate=me`, `assignee=me` → 200, `candidate=bob` and `group=` → 403 under `SelfOnly` | `WithQueryAuthorizer` letting `carol` read the `finance-approvers` queue, delegating everything else to `SelfOnly` |
| `record-page` | `ownerType=invoice&ownerRef=INV-42` lists every task for one invoice; `GET /tasks/{id}` → 200 for a candidate, 403 for `dave`, 403 with no actor, 404 unknown | `WithTaskReadAuthorizer` letting auditor `dave` read, delegating to `ParticipantsOnly` |
| `context-links` | `MetadataRoute` + `ExpandRoute`, with the host escaping values; an unknown `{tenant}` placeholder left intact | host keys (`acme.icon`, `acme.page`) and a host resolver that picks a link by task status |
| `schema-form` | `GET /task-types/invoice.approve` schemas; `SaveProgress` with a partial output accepted; `Complete` with the partial output → 400; with full output → 200 | the same type through `hmntsk.Define[In, Out]`, schemas derived from Go types |
| `escalation` | overdue approval task escalated by the sweeper under the type's `DefaultEscalation` (widen to `finance-managers`) | per-task `CreateRequest.Escalation` reassigning to `carol`; `WithSweepTypes` limiting the sweep |
| `event-delivery` | `WithEventHandlers` handler printing completions | relay + `webhook.New`; first to an `httptest` receiver with the default policy → dead-lettered as refused loopback; then with `AllowLoopback()` → delivered, receiver verifies with `webhook.NewVerifier`, a tampered body is rejected |
| `notifications` | `notify.NewMemoryStore`, `tasknotify.New`, relay pass; offers to `alice` and `bob`; `bob` claims → `alice`'s offer closed as taken; HTTP list, count, mark read; SSE stream receives `unread-changed` | `WithTaskLinkTemplate` + `WithTitles`; subscription authorizer letting `carol` follow a team member; `NewPruner` with `RetainActive`; `NewEmailDispatcher` with a `MailerFunc` printing messages; SQLite `notify/sqlstore` over `sqlkit/stdsql` |

### 6. Browser demo: React SPA embedded in a Go server

- **Server (`contextual-ui/*.go`):** one `http.ServeMux` mounting `httptransport.Mount` for the task API with `WithActorFunc`, `notify.NewHandler` with `WithActor`, a small demo API (`GET /demo/users`, `POST /demo/seed`), and the embedded SPA with an `index.html` fallback. Store: SQLite file in a temp directory (so a reader can inspect it), seeded with invoices and tasks at startup. A relay, hub and projector run with `Run` loops under the server's context, because the demo is long-lived.
- **Identity:** the selected demo user is sent in a `demo_user` cookie that the server reads in both actor functions. A cookie rather than a header because `EventSource` cannot set headers. The page shows a permanent banner that this is not authentication.
- **Frontend (`contextual-ui/web`):** Vite, React, TypeScript and Material UI v9 (with Emotion), chosen by the user so that the demo is attractive to end users. It follows MUI's official agent skills (`skills/material-ui-theming`, `skills/material-ui-styling` in mui/material-ui):
  - one `createTheme` with `colorSchemes` (light and dark) and `cssVariables`, provided by `ThemeProvider` with `CssBaseline` at the root;
  - styling at the narrowest scope: `sx` for one-off layout, `theme.components` for app-wide defaults, no global CSS file;
  - one-level imports (`@mui/material/Button`), and scoped state selectors only.

  No router library and no state library. Modules: API client, bucket list with counts (`/tasks/count` per bucket), task list ordered by `orderBy=urgency`, link rendering via the type's `hmntsk.route` expanded client-side the same way as `ExpandRoute`, a minimal JSON-Schema form walker (object of string, number, integer, boolean, enum; anything else falls back to a JSON textarea), and a notification badge that refetches `/notifications/count` on each SSE signal.
- **Build and embed:** `npm ci && npm run build` writes `contextual-ui/dist/`, which is committed and embedded with `//go:embed all:dist`. `go run ./contextual-ui` needs no Node. The build is driven by `make ui-build`, not by a `//go:generate` directive: `make generate` runs over every module, so a directive would make every Go contributor install Node.
- **Freshness check:** the CI `generated` job gains `actions/setup-node` and runs `npm ci` + build for `examples/contextual-ui/web` before its existing `git diff --exit-code`. Vite output is deterministic for identical source and lockfile; hashed filenames change only when content changes.
- **Tests:**
  - Go tests (`contextual-ui/server_test.go`): the SPA index is served at `/` and for unknown client routes; the demo cookie reaches both actor functions; the task and notification APIs are mounted with the expected defaults; seeding produces the buckets the page expects.
  - Vitest for the pure frontend logic: route expansion parity with `ExpandRoute` (same table of cases as `route_test.go`), the schema-form walker, and bucket query building. Run in a new CI job `examples ui` and a `make ui-test` target.
  - No browser end-to-end test in CI. Manual verification with `go run` is a task.
- **Alternatives:**
  - **No-build React from a CDN with import maps:** needs network at view time and gives no type checking. Rejected.
  - **`html/template` server rendering:** simpler to test but not what the user asked for, and it hides the HTTP contract a real SPA consumes. Rejected.
  - **Build at `go generate` time only, not committed:** `go run` would then need Node. Rejected by the "runs without a frontend toolchain" requirement.

### 7. Tooling

- **Makefile:** `EXAMPLES_MODULES := examples`, added to `MODULES` when `GROUP=all`, and `GROUP=examples` accepted. Not added to `RELEASE_ORDER`. New targets `ui-build` and `ui-test` (both `npm --prefix examples/contextual-ui/web ...`), kept out of `all` so that Go-only contributors are unaffected.
- **`go.work`:** `use ./examples`.
- **`examples/go.mod`:** `module github.com/kartaladev/hmntsk/examples`, `go 1.26.0`, the same comment block as satellites explaining that sibling modules come from `go.work` and that this module is never tagged. Third-party requirements added with `go get <module>@<version already used by store/sql>`, never tidy.
- **Tests guarding the release rule:** extend `docs_test.go` with a check that `make release-order` does not contain `examples` and that `docs/releasing.md` says the examples are never tagged.
- **CI:** add `examples` to the `unit tests` job's module list; race and vuln pick it up through `make`; new `examples ui` job for Vitest; Node in `generated`.
- **Lint:** existing rules apply unchanged. `revive`'s `package-comments` requires a `// Command <name> ...` doc comment per scenario, which doubles as the scenario's summary.

### 8. Documentation

- `examples/README.md`: how to run, the feature → scenario index (the capability list from the spec), and what each scenario deliberately does not cover.
- Root `README.md` "Documentation" section and the relevant guides (`docs/inbox.md`, `docs/notifications.md`, `docs/delivery.md`, `notify/docs/notifications.md`) link to the matching scenario.
- `docs/releasing.md`: the module count and a sentence that `examples` is developed here and never tagged.

### 9. Delivery in independent groups

Tasks are grouped so each group lands and passes on its own: module and tooling first, then core scenarios, HTTP scenarios, delivery and notifications, and the browser demo last. A group can be reviewed and merged separately if the change is split into several PRs.

### 10. Second round: an example for every capability

A coverage review against all twenty capability specs found about half of the library's behaviours unshown, including everything in the Redis, NATS and database driver modules. The user asked for all of it, service-backed scenarios included, and a README in every example.

- **New scenarios with no external service:** `lifecycle-operations`, `schema-migrations`, `notify-standalone`, `http-frameworks`. **Extended:** `escalation`, `event-delivery`, `notifications`. They keep `run(ctx, w)`, deterministic transcripts, and default-then-override sections.
- **Service-backed scenarios:** `store-drivers` (PostgreSQL, MySQL; `store/sql`, `store/pgx`, `store/gorm`), `event-bus` (Redis stream, NATS subject, JetStream), `realtime-scaling` (Redis and NATS broadcasters across two hubs, the WebSocket endpoint).
  - **Shape:** `run(ctx, w, services)` takes the connections it needs, so `main` and the test share everything but where the service comes from.
  - **Tests:** provision with the owning module's helper (`storetest.RunTestPostgres`/`RunTestMySQL`, `delivery/redis`/`delivery/nats` and `notify/redis`/`notify/nats` `RunTest*`), as the `use-testcontainers` rule requires. No fakes.
  - **`go run`:** reads `HMNTSK_POSTGRES_DSN`, `HMNTSK_MYSQL_DSN`, `HMNTSK_REDIS_ADDR` or `HMNTSK_NATS_URL` through `demo.RequireEnv`, which fails with the setting's name and the `docker run` command when it is absent. testcontainers is not used outside tests.
  - **Determinism:** transcripts print no container addresses, ports or broker-assigned identifiers.
- **CI and `make test`:** the examples tests now start containers, like every store and delivery module's tests already do; GitHub's Ubuntu runners have Docker. No build tag, consistent with the Makefile's stance on integration tests. Contributors running `make test` without Docker see those scenario tests fail for a named reason, as they already do for the store modules.
- **READMEs:** every example directory has one, from the same outline: what it shows, the domain it assumes, what it leaves out, run and test commands, and services with their `docker run` lines. `examples/README.md` stays the index.
- **Delivery:** the second round is written by parallel agents, one per scenario group, each owning its directories. Shared files (`go.mod`, `internal/`, `examples/README.md`, `tasks.md`, CI) are changed by the coordinating session only, so agents never race on them.

### 11. Third round: `inbox-ui` becomes `contextual-ui`

The user reviewed the browser demo and asked for tasks to live inside an application's own pages, the way the library's "contextual tasks" are meant to be used: rename it, replace the user switcher with a sign-in page, add an orders page that starts the work, give each invoice a dedicated page where the review and approval are done, and put the user's avatar and sign-out in the top-right corner.

- **Rename:** `examples/inbox-ui` → `examples/contextual-ui`, including the Makefile's `UI_DIR`, both CI jobs, the npm package name and every doc link. No redirect or alias: the examples are never tagged, so nobody depends on the old path.
- **Demo users:** alice, bob (approvers), carol (manager), dave (auditor) as before, plus **erin, purchasing**. erin exists only in the demo's user list, not in `invoicing.Directory()`, so she takes part in no invoice task and nobody approves their own purchase. The shared `invoicing` package is unchanged, so no other scenario's transcript moves.
- **Session (still not authentication):** `GET /demo/session` answers the signed-in demo user or 401; `POST /demo/session` with `{"user": "..."}` signs in, `DELETE /demo/session` signs out. The `demo_user` cookie is now `HttpOnly`, because the page asks the server who is signed in instead of reading `document.cookie`, which is how a real session cookie behaves. The actor functions of the task and notification handlers read it exactly as before. The sign-in page carries the "not authentication" notice.
- **Orders:** the example owns an `orders` table next to the engine's tables and `invoices` (`id`, `invoice_id`, `description`, `requested_by`, `status`, `created_at`). `POST /demo/orders` (purchasing only: 401 without a session, 403 for anyone else, 400 for an empty supplier or description or a non-positive amount) saves the order, its invoice and the invoice's review task in one host-led SQLite transaction and dispatches after commit, as `correlated-tasks` does. `GET /demo/orders` lists them, newest first. `POST /demo/invoices` ("Simulate a new invoice") is removed: placing an order replaces it.
- **Workflow after commit:** a relay sink named `invoice-workflow`, run by the same relay as the notification projector, reacts to completed invoice tasks.
  - A review with `matchesOrder: true` creates the approval task and moves the order to `awaiting-approval`. `false` moves it to `disputed`.
  - A completed approval moves the order to `approved` or `rejected`.
  - It is idempotent, because the relay delivers at least once. The approval task is created with a caller-supplied ID derived from the invoice (`CreateRequest.ID`, the engine's idempotent create), so a repeat answers `ErrConflict`, which counts as done, whichever relay asked. An order moves only from the one status each step expects, so a late repeat never moves it back.
  - A relay sink rather than an in-process `WithEventHandlers` handler, because the handler would need the `*Service` before `New` returns (library finding 3) and because a sink's work happens only after the completion is committed.
  - Seeded invoices get seeded orders by erin, so every invoice page has an order.
- **Invoice record:** `GET /demo/invoices/{id}` answers the invoice, its order and every task on it (type, status, assignee, activity, created time), read by the host through `Service.Query` filtered on correlation. That is what `record-page` shows, and it keeps the task API's `SelfOnly` default untouched: the page needs a record's whole history, which is the host's own record page and its own authorization (any signed-in demo user here), not an inbox query.
- **Pages:** `/login`, `/` (inbox), `/orders`, `/invoices/{id}` and `/invoices/{id}/{activity}?task={id}`, the existing `hmntsk.route`. Still no router library: a small tested `matchPage(pathname)` and `navigate(path)` over `history.pushState`. A task row, a notification and an order row all open the invoice page. The page works on the task named by `?task=`, and otherwise on the viewer's open task on that invoice. The task drawer becomes a card on that page.
- **App bar:** Inbox and Orders navigation, the notification bell, the colour mode toggle, and an `Avatar` with the user's initials at the far right whose menu shows name, role and groups and "Sign out".
- **Tests:** Go table tests cover session sign-in, sign-out and 401, orders (201, 400, 401, 403), and invoice records (200, 401, 404). A sequence test places an order, runs a relay pass, completes the review and checks the approval and the order status, with a table for disputed, approved and rejected. Vitest covers `matchPage`, the workflow steps derived from an invoice's tasks, and initials.

### 12. After the library fixes: workarounds removed

The first round reported four library findings in PR #14. They were fixed in PR #15, merged before this one, and this branch was rebased onto it. Each example's workaround gave way to the library's own mechanism; transcripts are unchanged.

- **`realtime-scaling`, `notifications`, `contextual-ui`:** a hub used to be started with `demo.Background` and polled with `demo.WaitUntil(hub.Running)`, and `realtime-scaling` also sent probe signals between instances until one arrived, because a hub reported running before its broadcaster had subscribed.
  - They now call `demo.RunHub`, a shared helper written test-first. It runs the hub and returns once `hub.Ready()` closes, or with `Run`'s error when the broadcaster cannot subscribe (for NATS, within `WithSubscribeTimeout`), or on timeout. Either failure stops the hub.
  - The probes and `demo.WaitUntil` are deleted.
  - Default: five seconds, ten for broker-backed instances. Override: the caller passes its own timeout.
- **`http-frameworks`:** Fiber builds its app with `fibertransport.App()` and adds the notification routes afterwards. It no longer builds the app by hand with a catch-all not-found handler added last.
- **`schema-form`:** `hmntsk.WithEventHandlerFactory` defines the typed kind and returns its `OnCompleted` handler while `New` runs, instead of a handler variable assigned after `Define`.
- **`event-bus`:** both bounded Redis sinks use `WithExactTrim()`, so the lengths printed are exact on any Redis. The test no longer starts its container with `stream-node-max-entries 1`.
- **Not fixed in the library, recorded there as follow-ups:**
  - authorization of cancel, suspend and resume;
  - per-sink failures persisted on the outbox row.

  No example depends on either.

## Risks / Trade-offs

- **[Transcripts are brittle]** → Print stable fields only, no IDs or wall-clock times; keep transcripts short; the diff on an intended change is the documentation update.
- **[A library behaviour change breaks unrelated PRs through examples]** → That is the purpose; the failing transcript names the behaviour. The owning change updates the scenario in the same PR.
- **[`notify` and `sqlkit` import paths move before release]** → The examples module is untagged, so the move rewrites its imports in the same split commit; noted in `docs/releasing.md` step 0 and 0b.
- **[Node toolchain and npm supply chain]** → Pinned, exact dependencies: react and react-dom, Material UI (`@mui/material`, `@mui/icons-material`) with Emotion at runtime; vite, typescript, vitest and @vitejs/plugin-react for development. The lockfile is committed and installed with `npm ci`, and Go-only contributors never need Node. The MUI bundle is about 550 kB (170 kB gzipped), acceptable for a page served from localhost. `govulncheck` does not cover npm, so the `examples ui` job runs `npm audit --omit=dev --audit-level=high`.
- **[Readers copy the demo's cookie identity or form walker into production]** → Banner on the page, doc comments and README say so; the walker is deliberately minimal.
- **[Non-deterministic Vite output fails the freshness check]** → Pin Vite and Node major in CI; if hashes still vary, commit only the content and check with a content comparison instead of `git diff`.
- **[SQLite in a scenario test is slower than memstore]** → Only `correlated-tasks`, `notifications` (override section) and `contextual-ui` use it. Each opens a file in a temporary directory with write-ahead logging and immediate transactions, as `sqlkittest.RunTestSQLite` does, rather than `file::memory:`, whose per-connection databases would break a host transaction read from another pooled connection.

## Migration Plan

Additive only. Rollback is removing `examples/`, its `go.work` entry, the Makefile group and the CI steps.
