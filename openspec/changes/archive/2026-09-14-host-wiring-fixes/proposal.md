## Why

The usage examples (PR #14) found three places where a host can't wire the library the obvious way:
- routes added to the app from `fibertransport.App()` answer `404`;
- an in-process event handler that needs the `*hmntsk.Service`, such as `Kind.OnCompleted`, can't be built before `hmntsk.New` returns;
- a Redis stream bound only trims approximately, so a small bound does nothing at Redis's default node size.

They also found that an outbox row's `LastError` is text the host can't inspect by sink. Fixing that properly needs a schema change, so this change documents it as a limit instead.

## What Changes

- **`transport/fiber`: `App()` no longer installs a catch-all.**
  - It answers an unmatched path or method in the contract's JSON `404` through the Fiber app's error handler, so routes the host adds afterwards are served.
  - New `NotFoundErrorHandler(next fiber.ErrorHandler)` lets a host that builds its own app and uses `Mount` get the same behaviour while keeping its own error handling.
  - `NotFoundHandler` stays.
- **`hmntsk`: new `WithEventHandlerFactory(func(*Service) ([]EventHandler, error))`.**
  - `New` calls it with the service it is building, once the rest of construction has succeeded.
  - A factory error fails construction.
  - Handlers run in the order their options were given, factories included.
- **`delivery/redis`: new `WithExactTrim()`.**
  - With `WithMaxLen` or `WithMaxAge`, it trims exactly instead of approximately.
  - Approximate trimming stays the default, and exact trimming's cost is documented.
  - Exact trim without a bound is a configuration error.
- **Documented limit:** `OutboxEntry.LastError` is human-readable text, one `<sink>: <message>` part per refusing sink, joined with `"; "`, not a format to parse. Typed per-sink errors are available only while the pass runs, through `relay.WithRelayErrorHandler`.

## Non-goals

- **Authorization of `Cancel`, `Suspend` and `Resume`.** `Cancel` checks no actor, and `Suspend`/`Resume` check only a held task's assignee. That is a policy design comparable to task-read-authorization (#8), and it is recorded as follow-up change `lifecycle-authorization`.
- **Per-sink failures stored on the outbox row.** That needs a schema change in every store, and is only documented here.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-http-api`: a host can add its own routes to an application built by a binding's convenience constructor, and unknown routes still answer in the contract's JSON `404`.
- `task-events`: in-process consumers can be built from the engine they consume, at construction.
- `redis-delivery`: the length and age bounds trim approximately by default, and exactly when the host opts in.

## Impact

- **Code:** `transport/fiber` (`app.go` and tests), `hmntsk` (`service.go`, and the godoc on `outbox.go` and `relay`), and `delivery/redis` (`retention.go` and tests).
- **Docs:** `README.md` (event handlers), `docs/delivery.md` (exact trimming, `LastError`), and the Fiber binding docs.
- **API:** additive only. `App()` behaviour changes: routes added after it now serve instead of answering `404`, and a wrong method on a contract path answers `404` with no `Allow` header, as before.
- **PR #14:** `http-frameworks` can use `App()`, `schema-form` can drop its late-bound handler variable, and `event-bus` can use `WithExactTrim()` instead of changing the test server's node size.
