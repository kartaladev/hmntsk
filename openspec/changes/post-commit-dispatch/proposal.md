## Why

`Result.Dispatch` and the README promise that in-process handlers run after a host-led transaction commits, "never before". Two library paths break that promise when they run inside a transaction the host began:

- **C6:** `Sweeper.Sweep` calls `Dispatch` on each pending escalation straight away (`sweep.go:191-195`). An in-process handler is told about an escalation that the host then rolls back.
- **T6:** the HTTP transport calls `result.Dispatch(ctx)` inside the handler and discards the error (`transport/core/api.go:459`). A host that wraps the handler in its own transaction, which is the documented way to join the engine to host writes, has its consumers run before the commit. They read the old state (`READY` instead of `RESERVED`), and a failing consumer's error never reaches `WithDispatchErrorHandler`.

Both are confirmed by failing reproduction tests in the audit scratch module.

## What Changes

- **`hmntsk`: new deferred dispatch for host-led transactions.**
  - `ContextWithDeferredDispatch(ctx)` returns a context and a `*DeferredDispatch`. A host installs it before beginning its transaction.
  - A lifecycle operation that joins a host transaction under such a context hands its events to the `DeferredDispatch` instead of the `Result`, so `Result.Pending()` is false.
  - The host calls `DeferredDispatch.Dispatch(ctx)` after it commits, and simply drops it on rollback.
- **`hmntsk`: every host-led dispatch path reports handler errors to `WithDispatchErrorHandler`.** That covers `Result.Dispatch`, `DeferredDispatch.Dispatch` and the new `SweepResult.Dispatch`, as the engine-led path already does. The error is also returned to the caller.
- **`hmntsk`: `Sweeper.Sweep` never dispatches inside a host-led transaction.**
  - It leaves the pending escalations on the `SweepResult`, and the new `SweepResult.Pending()` and `SweepResult.Dispatch(ctx)` hand them back to the host.
  - Under a `DeferredDispatch` they go there instead.
  - An engine-led sweep is unchanged: each escalation commits and dispatches on its own.
- **`transport/core`: `respond` no longer dispatches.**
  - Under a host-led transaction with a `DeferredDispatch`, the events are already queued for after the commit.
  - Without one, the transport cannot observe the commit. It runs no in-process handler, and reports `ErrDispatchNotDeferred` to `WithDispatchErrorHandler` through the new `Result.Decline(ctx)`. The events stay durable in the outbox for the relay.
- **Docs:** README ("Transactions", "Events"), `Sweeper.Sweep` godoc (it claims each escalation runs in its own transaction, which is false under a host transaction), and the transport godoc gain the host middleware pattern.

No **BREAKING** API change: everything is additive. Two behaviours change before the first tag:
- `Result.Dispatch` now also calls the dispatch error hook.
- The HTTP transport under a host transaction without deferred dispatch no longer runs handlers before the commit.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-events`: in-process delivery after a host-led commit gets a defined mechanism. Handler errors from every dispatch path reach the dispatch error hook.
- `task-escalation`: a sweep run inside a host transaction withholds dispatch until the host commits.
- `task-http-api`: a request served inside a host transaction never dispatches before the commit, and never drops a dispatch error.

## Impact

- **Code:**
  - `hmntsk`: `service.go` (`finish`, `Result`, the new `DeferredDispatch`, `ErrDispatchNotDeferred`), `sweep.go` (`Sweep`, `SweepResult`) and `errors.go`.
  - `transport/core/api.go` (`respond`).
  - `transporttest`: a new host-transaction case group, run by the net/http, gin and fiber bindings.
- **Docs:** `README.md`, godoc on `Result`, `Sweeper`, `WithDispatchErrorHandler` and `transport/core`.
- **Examples:** `examples/*` call `Result.Dispatch` directly after commit without a `DeferredDispatch`, and keep working unchanged.
- **Modules:** root `hmntsk`, `transport/core`, `transporttest`, and the three binding modules (tests only).
