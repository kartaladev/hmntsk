Every scenario is built test-first: write `main_test.go` with the expected transcript, run it and watch it fail for the intended reason (missing output, not a compile error in an unrelated package), then write `run` until it passes. Run commands with `GOTOOLCHAIN=go1.26.8`. Never run `go mod tidy` or `make tidy`.

## 1. Module and tooling

- [x] 1.1 Create `examples/go.mod` (`github.com/kartaladev/hmntsk/examples`, `go 1.26.0`, satellite comment block saying sibling modules come from `go.work` and the module is never tagged) and add `use ./examples` to `go.work`; verify `go list github.com/kartaladev/hmntsk/examples/...` resolves from the repository root
- [x] 1.2 Add `modernc.org/sqlite v1.58.0` (the version `store/sql` uses) with `go get` inside `examples/`; verify `go build ./...` in `examples/` succeeds and `git diff` shows no other module's `go.mod` changed
- [x] 1.3 Write a failing `docs_test.go` check that `make release-order` omits `examples` and `docs/releasing.md` states the examples are never tagged; then add `EXAMPLES_MODULES := examples` to the Makefile (in `MODULES` for `GROUP=all`, `GROUP=examples` accepted, not in `RELEASE_ORDER`) and the sentence to `docs/releasing.md`; verify the test passes and `make build GROUP=examples` runs the module
- [x] 1.4 Add `examples` to the CI `unit tests` job's module list; verify with `actionlint` (or a YAML parse) and by reading that `race detector` and `govulncheck` reach it through `make`

## 2. Shared helpers and domain

- [x] 2.1 Test-first `internal/demo`: `Section(w, title)` header format, a fixed advanceable clock satisfying both `hmntsk.Clock` and `notify.Clock`, and ID masking for printed links; verify `go test ./internal/demo/...` passes with table tests in the project's `assert`-closure form
- [x] 2.2 Test-first `internal/invoicing`: `Invoice`, in-memory and SQL invoice repositories, the `invoice.review` and `invoice.approve` type specs (schemas, defaults, `hmntsk.route` metadata), and the static directory (alice, bob, carol, dave); verify a test registers both types on a memstore service without error and that the SQL repository round-trips an invoice on in-memory SQLite

## 3. Core scenarios (no HTTP)

- [x] 3.1 `quickstart`: transcript of create → claim → start → complete with defaults only; verify `go test ./quickstart` passes and `go run ./quickstart` prints the same transcript
- [x] 3.2 `correlated-tasks`: default commits an invoice and its correlated review task in one SQLite transaction with events dispatched after commit; override rolls back and shows neither exists and no event; verify the scenario test passes
- [x] 3.3 `context-links`: default expands `hmntsk.route` with host escaping and leaves `{tenant}` intact; override uses host metadata keys and a status-based resolver; verify the scenario test passes
- [x] 3.4 `escalation`: default sweeps an overdue approval and widens to `finance-managers` under a fixed clock; override uses a per-task escalation and `WithSweepTypes`; verify the scenario test passes

## 4. HTTP scenarios

- [x] 4.1 `inbox-buckets`: default shows every ordering, cursor paging, a cross-ordering cursor refused, `CountBuckets`, and `SelfOnly` HTTP outcomes (200 for `me`, 403 for `candidate=bob` and `group=`); override adds a supervisor `QueryAuthorizer` for carol; verify the scenario test passes
- [x] 4.2 `record-page`: default lists tasks by `ownerType`/`ownerRef` and shows `ParticipantsOnly` outcomes (200 candidate, 403 dave, 403 no actor, 404 unknown); override adds an auditor `TaskReadAuthorizer`; verify the scenario test passes
- [x] 4.3 `schema-form`: default serves the type's schemas, accepts partial progress, rejects incomplete completion with 400 and completes with full output; override uses `hmntsk.Define`; verify the scenario test passes

## 5. Delivery and notification scenarios

- [x] 5.1 `event-delivery`: default in-process handler; override relay + webhook sink showing loopback refused and dead-lettered under `DefaultPolicy`, then delivered with `AllowLoopback()`, verified by `webhook.NewVerifier`, and a tampered body rejected; verify the scenario test passes
- [x] 5.2 `notifications` default: memory store, `tasknotify` projector on a relay pass, offers to alice and bob, bob's claim closes alice's offer, HTTP list/count/mark-read, and one SSE `unread-changed` signal read by the test client; verify the scenario test passes with `goleak`-clean shutdown of hub and stream
- [x] 5.3 `notifications` override: `WithTaskLinkTemplate` + `WithTitles`, a subscription authorizer for carol, `NewPruner` with `RetainActive`, `NewEmailDispatcher` with a printing `MailerFunc`, and `notify/sqlstore` over `stdsqlexec` on SQLite; verify the scenario test passes

## 6. Browser demo `contextual-ui`

- [x] 6.1 Test-first Go server: SPA index served at `/` and for unknown client routes, `demo_user` cookie reaching the task and notification actor functions, task API mounted with default policies, notify handler mounted, seeding producing the expected buckets; verify `go test ./contextual-ui` passes using a placeholder `dist/index.html`
- [x] 6.2 Scaffold `contextual-ui/web` (Vite, React, TypeScript, Vitest; pinned versions; committed `package-lock.json`) and add `make ui-build` / `make ui-test`; verify `npm ci && npm run build` writes `contextual-ui/dist` and `make ui-test` runs an empty suite green
- [x] 6.3 Test-first frontend logic with Vitest: route expansion matching the cases in root `route_test.go`, the schema-form walker (string, number, integer, boolean, enum, JSON fallback), bucket query building; verify `make ui-test` passes
- [x] 6.4 Build the page with Material UI v9, following MUI's official theming and styling skills (one theme with light and dark color schemes and CSS variables, `ThemeProvider` + `CssBaseline`, `sx` before theme overrides, one-level imports): user switcher with a "demo only, not authentication" banner, buckets with counts, urgency-ordered list with route links, claim/start/progress/complete through the rendered form, notification badge refreshed on SSE signals; commit `dist/`; verify manually with `go run ./contextual-ui` against every spec scenario (switching users, completing from the form, live notification) and record the result in the PR description
- [x] 6.5 Wire freshness and UI checks into CI: Node setup plus `npm ci` and build before `git diff --exit-code` in `generated`, and a new `examples ui` job running Vitest and `npm audit --omit=dev --audit-level=high`; verify by changing a source file without rebuilding and confirming the diff check fails locally

## 7. Documentation

- [x] 7.1 Write `examples/README.md` with run instructions, the feature → scenario index covering every capability in the spec, and non-goals; verify every scenario directory is linked and every spec capability bullet maps to a row
- [x] 7.2 Link scenarios from `README.md`, `docs/inbox.md`, `docs/notifications.md`, `docs/delivery.md` and `notify/docs/notifications.md`, and update the module count in `docs/releasing.md`; verify existing `docs_test.go` checks still pass (`notify/docs/notifications.md` deliberately left unlinked: `notify/` moves to its own repository, where a link back into `examples/` would break; `docs/notifications.md`, which it already links to, carries the link instead)

## 8. Verification

- [x] 8.1 Run `/simplify` over `examples/` and the tooling changes, then re-run `go test ./...` in `examples/`
## 9. Second round: dependencies and shared tooling

- [x] 9.1 Add the Gin, Fiber, go-redis, nats.go, coder/websocket, pgx, MySQL driver and GORM requirements to `examples/go.mod` at the versions sibling modules use, with `go get`, never tidy; verify `go build ./...` in `examples/` succeeds and no other `go.mod` changed
- [x] 9.2 Add `demo.RequireEnv` (or equivalent) test-first: a service-backed scenario's `main` reads its address from the environment and exits with a message naming the setting and the `docker run` command when it is absent; verify its table test

## 10. Second round: scenarios with no external service

- [x] 10.1 `lifecycle-operations`, test-first: release, delegate, suspend/resume, fail, cancel, stale-version conflict, illegal transition, auto-reserve with one candidate, `ERROR` with none, excluded users, auto-start on progress save; default then override sections; verify the scenario test passes
- [x] 10.2 Extend `escalation`: direct `Escalate`, `ExemptInProgress`, `MaxEscalations`, `EscalationSupersede` into `OBSOLETE`; update its transcript first and watch it fail; verify the scenario test passes
- [x] 10.3 Extend `event-delivery`: a receiver answering 503 then 200 showing retry with backoff, two sinks accepting independently, the relay error handler, and the audience snapshot in the payload; transcript first; verify the scenario test passes
- [x] 10.4 `schema-migrations`, test-first: `sqlcore` migration statements, a table prefix, `VerifySchema` reporting a mismatch, nested transaction scopes joining one transaction; verify the scenario test passes
- [x] 10.5 `notify-standalone`, test-first: `notify` with no tasks: publish, duplicate source, coalescing, closing a subject with a successor, a watermark suppressing an older publish, mark all read; verify the scenario test passes
- [x] 10.6 Extend `notifications`: projection on release, delegation and widening escalation, `WithRules` adding a kind, `WithClosingStatuses`, email `WithEmailKinds` and a recipient without an address, the age bound, `EvictOldestActive` evicting an unread notification (the typed `OnCompleted` handler moved to `schema-form`, which covers the typed facade); transcript first; verify the scenario test passes with goleak
- [x] 10.7 `http-frameworks`, test-first: the task contract and the notification handler served by Gin and by Fiber, the same requests answering the same statuses on both; verify the scenario test passes

## 11. Second round: service-backed scenarios

- [x] 11.1 `store-drivers`, test-first with `storetest.RunTestPostgres`/`RunTestMySQL`: the same host-led transaction and query on PostgreSQL via `store/sql` and `store/pgx`, MySQL via `store/sql`, and GORM on both; `go run` reads `HMNTSK_POSTGRES_DSN` and `HMNTSK_MYSQL_DSN`; verify the scenario test passes
- [x] 11.2 `event-bus`, test-first with the delivery modules' `RunTestRedis`/`RunTestNATS`: Redis stream sink with a length bound, NATS subject sink and JetStream sink, each read back by a consumer; `go run` reads `HMNTSK_REDIS_ADDR` and `HMNTSK_NATS_URL`; verify the scenario test passes
- [x] 11.3 `realtime-scaling`, test-first with notify's `RunTestRedis`/`RunTestNATS`: a signal published on one instance reaching a stream on another through each broadcaster, and the WebSocket endpoint refusing a foreign origin, then accepting it and marking read over the socket; verify the scenario test passes with goleak

## 12. Second round: documentation

- [x] 12.1 Write a `README.md` in every example directory (all seventeen): what it shows, the context and domain it assumes, what it leaves out, how to run it and its test, and for service-backed ones the `docker run` commands; verify every directory has one and every command in them works
- [x] 12.2 Update `examples/README.md` so the index maps every capability bullet in the spec to a scenario, and remove the "not covered" list that no longer applies; verify each bullet has a row

## 13. Second round: verification

- [x] 13.1 Run `/simplify` over the second-round changes and re-run the examples tests
- [x] 13.2 Run the full check `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln`, `make ui-test`, and `openspec validate usage-examples --strict`; all pass

## 14. Third round: `contextual-ui`

- [x] 14.1 Rename `examples/inbox-ui` to `examples/contextual-ui` with `git mv`, and update the Makefile, CI, npm package name, READMEs, docs and this change; verify no `inbox-ui` reference remains outside built assets
- [x] 14.2 Test-first demo session: `GET`/`POST`/`DELETE /demo/session` with an `HttpOnly` cookie, erin added as purchasing, 400 for an unknown user and 401 with no session; verify the server table test fails first, then passes
- [x] 14.3 Test-first orders: `POST /demo/orders` saves order, invoice and review task in one transaction (201, 400, 401, 403), `GET /demo/orders` lists newest first, seeded orders for the seeded invoices, and `POST /demo/invoices` removed; verify red then green
- [x] 14.4 Test-first `invoice-workflow` relay sink: a matching review creates the approval once even when delivered twice, a mismatch marks the order disputed, an approval marks it approved or rejected; verify red then green (the repeat-delivery cases were also confirmed to fail with the duplicate guard and the status guard removed)
- [x] 14.5 Test-first `GET /demo/invoices/{id}`: invoice, order and every task on it (200, 401, 404); verify red then green
- [x] 14.6 Test-first page logic with Vitest: `matchPage`, workflow steps from an invoice's tasks, initials; verify red then green with `make ui-test`
- [x] 14.7 Build the pages with Material UI: sign-in page with the "not authentication" notice, app bar with navigation and avatar menu with sign-out, inbox without a switcher, orders page with the order form and list, invoice page with details, steps and the task card; `make ui-build` and commit `dist/`
- [x] 14.8 Update `contextual-ui/README.md`, `examples/README.md` and doc links; run `/simplify` over the change; run the full check and verify every spec scenario manually in a browser with `go run ./contextual-ui`

## 15. After the library fixes (PR #15)

- [x] 15.1 Rebase onto `main` with PR #15 merged; verify `go build`, `go vet` and the scenario tests pass unchanged before touching any workaround
- [x] 15.2 Test-first `demo.RunHub`: returns once the hub is ready, returns the broadcaster's error when it cannot subscribe, gives up on timeout, and stops on a cancelled context, with the hub stopped on every failure; verify red against a stub, then green under `-race`
- [x] 15.3 `realtime-scaling`, `notifications`, `contextual-ui`: start hubs with `demo.RunHub`; delete the readiness probes and `demo.WaitUntil`; verify the scenario tests pass, `realtime-scaling` against its containers
- [x] 15.4 `http-frameworks` with `fibertransport.App()`, `schema-form` with `hmntsk.WithEventHandlerFactory`, `event-bus` with `WithExactTrim()` and a default Redis in its test; update the READMEs; verify each transcript test passes unchanged
- [x] 15.5 Fix the five code review findings test-first:
  - the workflow sink moves the order before creating the approval, so a failed order update never leaves an approval that can be completed while the order waits in review (Go table case with an injected update failure);
  - the task panel re-reads a task after a 409 (`isStale`, Vitest);
  - a malformed path escape shows "no such page" instead of crashing the page (Vitest);
  - "Load more" drops a page for a bucket no longer shown and reports its errors (checked by typecheck and review only: the component has no test harness, and the seeded buckets never fill a 20-task page, so the button does not appear in the demo);
  - a number field holding only spaces is left out, not sent as 0 (Vitest).
- [x] 15.6 Run the full check and `make ui-test`, validate this change, update the PR description's library findings and test plan, and push the rebased branch
- [x] 15.7 Fix the second code review's two findings:
  - `realtime-scaling`'s `openStream` wraps the read error only when the read failed, and otherwise names the status and first line (`TestOpenStream` table with local servers; red on `%!w(<nil>)`, then green);
  - the notification menu reports a failed mark-read and reads the count again (checked in the browser by ending the session with the menu open: the menu shows the server's refusal).
