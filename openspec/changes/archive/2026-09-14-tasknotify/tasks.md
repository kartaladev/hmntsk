## 1. Prerequisites and module scaffolding

- [x] 1.1 Confirm the prerequisites are in place:
  - `event-audience-snapshot`, `task-read-authorization`, `sqlkit` and `notify-core` are applied, and notify-core's implementation includes amendments A1 (close with successors) and A2 (coalescing drafts), which are part of its design (decision 13) and tasks (3.5, 3.6).

  Verify with `go list ./...` in `notify` and `sqlkit`, and that `notify.CloseRequest` has `Successor`/`SuccessorSkip` and `notify.Draft` has `Coalesce`; stop and raise it if any is missing.
- [x] 1.2 Create module `tasknotify` (`go.mod` without naming hmntsk or notify, per `docs/releasing.md`). Add it to `go.work` and to `HMNTSK_MODULES` in the `Makefile`, and add a `doc.go` stating the module's purpose. Verify with `GOTOOLCHAIN=go1.26.8 go build ./...` in `tasknotify` and `make split-check`; never run `go mod tidy` or `make tidy`.

## 2. Engine: `Service.ResolveCandidates`

- [x] 2.1 Red: add a table test in the root module (`table-test` conventions, `t.Context()`). It covers:
  - users only;
  - groups expanded through `hmntsk.NewStaticAssignment`;
  - exclusions removed;
  - duplicates collapsed;
  - sorted output;
  - no resolver with groups returns `*GroupResolutionError`;
  - a resolver error propagates (use-mockgen typed mock of `GroupResolver`).

  Run `go test -run 'TestServiceResolveCandidates' -count=1 .` and watch it fail on the missing method.
- [x] 2.2 Green: implement `Service.ResolveCandidates` over `ResolveCandidates` with the service's resolver, with godoc naming the default. Verify with the test from 2.1.

## 3. Construction and options

- [x] 3.1 Red: table test for `New`. Its cases:
  - defaults: sink name `tasknotify`, closing set of all five final statuses, publish batch 500, task link template `/v1/tasks/{task.id}`;
  - configuration errors: nil engine; nil notifier; closing set missing COMPLETED; missing EXITED; containing READY; empty; batch ≤ 0; empty sink name; empty task link template; nil `Rules`, `TitleFunc`, `DataFunc`, `LinksFunc` and error handler.

  Each error matches `hmntsk.ErrConfiguration`. Watch it fail.
- [x] 3.2 Green: implement `Projector`, the options, and the constants (kinds, reasons, relations, `DefaultSinkName`, `DefaultPublishBatch`, `DefaultTaskLinkTemplate`). Verify with 3.1 and `var _ relay.Sink = (*Projector)(nil)`.

## 4. Links, titles and data

- [x] 4.1 Red: table test for links. Cases:
  - a type with a route and an owner reference gives both relations;
  - a type without a route gives only `task`;
  - an unregistered type gives only `task`;
  - an extra correlation placeholder expands, and an unknown placeholder is left as written;
  - a raw value containing `/` is not escaped;
  - a custom task link template;
  - `WithLinks` replaces both.

  Watch it fail.
- [x] 4.2 Green: implement the event-to-Task adapter and link building. Verify with 4.1.
- [x] 4.3 Red, then green: table tests for the default titles per kind and the default data JSON. The data test asserts that no input, output, correlation extra or pool appears, plus the `WithTitles` and `WithData` overrides. Verify the tests fail first, then pass.

## 5. Default rules (planning, no store)

- [x] 5.1 Red: one table test of `DefaultRules.Plan` per event. Assert the exact ordered steps of design decision 3, using `Input` built with a static resolver. Cases:
  - created READY: offers to eligible minus actor, with groups and exclusions;
  - created RESERVED: assigned, and none when the assignee is the actor;
  - claimed: one close with successor `taken`, skipping the claimant;
  - released: close taken and assigned, then offers minus the releaser;
  - delegated: close assigned except the new holder, then assigned;
  - escalated READY: coalescing offers;
  - escalated while RESERVED: no steps;
  - each closing status: close every kind with the lower-cased reason;
  - a narrowed closing set: FAILED produces no steps;
  - started, suspended, resumed: no steps.

  Watch it fail.
- [x] 5.2 Green: implement `DefaultRules`, `RulesFunc`, `Input` (memoised `Eligible`), `Plan` and `Step`. Verify with 5.1.

## 6. Projector execution and failure classification

- [x] 6.1 Red: table test of `Deliver` against an in-memory `notify.Service`, plus a use-mockgen typed mock `notify.Store` for failures. Cases:
  - success: `Delivered`;
  - `ErrUnavailable`: `Retryable`;
  - context deadline: `Retryable`;
  - resolver failure: `Retryable`;
  - missing resolver: `Delivered`, reported to the error handler;
  - `ErrValidation` from a host title: `Delivered`, reported;
  - a plan with a draft for another subject: `Delivered`, reported;
  - drafts beyond the batch size: split into several `Publish` calls;
  - no case ever returns `Permanent`.

  Watch it fail.
- [x] 6.2 Green: implement `Deliver`, step execution, batching and classification. Verify with 6.1.
- [x] 6.3 Red, then green: a `WithRules` override test. A host appends a creator notification on completion to `DefaultRules`, and the default closes still run (spec: rules are replaceable).

## 7. Spec behaviour on the in-memory store

- [x] 7.1 Red: scenario table tests that drive events through `Deliver` on an in-memory notify store and assert notification states per recipient. Cover every scenario in `specs/task-notifications/spec.md` that needs no relay:
  - offers;
  - actor exclusion;
  - taken, including from a read offer;
  - release;
  - delegation;
  - widening, including coalescing;
  - closing statuses, and the narrowed set;
  - suspension no-op;
  - the late group member;
  - links.

  Watch them fail against a stubbed plan.
- [x] 7.2 Red: ordering table tests, delivering sequences out of order and repeated:
  - creation after claim leaves no active offer;
  - claim after completion leaves no active taken;
  - release before claim keeps the release's offers and leaves no active taken;
  - a claim whose first attempt fails after the close commits leaves exactly one taken per other candidate (inject the failure through a store wrapper);
  - duplicate delivery of every event type.

  Watch them fail for the intended reason.
- [x] 7.3 Green: fix whatever 7.1 and 7.2 expose. Verify with `go test -race -count=1 ./...` in `tasknotify`.

## 8. End-to-end through the relay on SQL stores

- [x] 8.1 Add `tasknotify/testutils.go` helpers that reuse `sqlkit/sqlkittest` container helpers (`RunTestPostgres`, `RunTestMySQL`, `RunTestSQLite`), per use-testcontainers, rather than starting containers directly. Build the hmntsk store (`store/sql`) and `notify/sqlstore` on the same database. Verify the helpers compile and a smoke test starts each database.
- [x] 8.2 Red: an end-to-end table test on PostgreSQL, MySQL and SQLite. Create, claim, release, delegate, escalate and complete tasks through `hmntsk.Service`; run `relay.Relay` with the projector and a recording second sink; then assert notification states. Cases:
  - normal order;
  - a transient notify store failure on the claim's first attempt, retried on the next pass (`WithBackoff` shortened, clock controlled);
  - reordering produced by failing an older event's attempt while newer events succeed;
  - a deterministic projection failure: the second sink still accepts the event, and nothing is dead-lettered.

  Watch it fail.
- [x] 8.3 Green: fix whatever 8.2 exposes. Verify with `GOTOOLCHAIN=go1.26.8 make test-integration` for `tasknotify`.

## 9. Documentation

- [x] 9.1 Write `docs/notifications.md`:
  - wiring;
  - the per-event rules table;
  - the kind, reason and relation constants;
  - closing statuses;
  - links and escaping;
  - the membership-at-projection limit;
  - failure classification and the error handler;
  - sink renaming;
  - pre-snapshot backlogs.

  Link it from the README and from `docs/inbox.md`. Verify with the repository's docs tests (`docs_test.go` pattern) if present, and a manual link check.
- [x] 9.2 Add runnable `Example` tests for wiring the projector and for extending `DefaultRules`. Verify with `go test -run Example ./...` in `tasknotify`.

## 10. Refactor and full verification

- [x] 10.1 Run `/simplify` on the code touched in this change, then re-run `go test -race -count=1 ./...` in `tasknotify` and the root module.
- [x] 10.2 Run the full check `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln split-check`. Confirm all of it passes, and never run `make tidy` or `go mod tidy`.
