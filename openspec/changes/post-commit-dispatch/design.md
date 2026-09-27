## Context

See proposal.md for why. The current state, checked in the code:

- **`service.go` `finish`:** when `store.InTransaction(ctx)` was true before the operation, the host began the transaction. `finish` stores the events in `Result.pending` and returns without dispatching. Otherwise it dispatches through `deliver` and sends any error to `onDispatchError`, which is `WithDispatchErrorHandler` and a no-op by default.
- **`Result.Dispatch`:** delivers `pending` and returns the joined error. It does not call `onDispatchError`. The godoc and `README.md:181` say "Call it after committing, never before".
- **`sweep.go:144-200` `Sweep`:**
  - `ClaimOverdue` and every `Escalate` join the caller's context. Under a host transaction they all share it, so the godoc line "Escalation of each claimed task runs in its own transaction" does not hold.
  - Lines 191-195 call `escalated.Dispatch(ctx)` whenever `Pending()` is true, which is exactly the host-led case, before the host commits. The error goes to the sweep hook, not the dispatch hook.
- **`transport/core/api.go:452-461` `respond`:** runs `_ = result.Dispatch(ctx)` inside the handler. A host that wraps handlers in `store.Do` or `sqlstore.ContextWithTx`, the documented way to join host writes, gets consumers run before its commit and their errors dropped.
- **Reproductions** in `$V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`:
  - `$V/core/sweep_test.go` `TestSweep_InHostTransactionDoesNotDispatchBeforeCommit` (C6): memstore `ContextWithTx`, then `Sweep`, then rollback. The handler already saw the escalation.
  - `$V/transport/dispatch_test.go` `TestHostLedDispatch` (T6): net/http, gin and fiber, with a host middleware wrapping `store.Do`. The consumer reads `READY`, and its error never reaches the hook.
- **No tags yet** (`git tag` is empty), so changing a default is free. It is recorded here per `library-design.md` rule 7.

## Goals / Non-Goals

**Goals:**
- No library component runs an in-process consumer before a host-led commit.
- Every host-led path has a way to dispatch after the commit, and a safe default when the host uses none.
- No dispatch error is dropped.

**Non-Goals:**
- Making the engine observe a host commit through store adapters, for example `sql.Tx` has no commit hook. That is store-specific and not available for every driver.
- Changing relay or outbox delivery, which is already durable and at-least-once.
- `Sweeper.Run` under a host transaction, and the other sweep findings (C5, C9, C11). They belong to other changes.

## Decisions

### 1. A context-carried `DeferredDispatch` collects host-led events

```go
// ContextWithDeferredDispatch returns a context under which every lifecycle
// operation that joins a host-led transaction queues its events on the returned
// DeferredDispatch instead of its Result. Install it before beginning the
// transaction; call Dispatch after committing; drop it on rollback.
func ContextWithDeferredDispatch(ctx context.Context) (context.Context, *DeferredDispatch)

// Dispatch delivers every queued event once, in queue order, and empties the
// queue. Consumer errors go to each originating service's dispatch error hook
// and are returned joined.
func (d *DeferredDispatch) Dispatch(ctx context.Context) error
```

- **Behaviour:** `finish` checks `hostLed` first. When it is true and the context carries a `DeferredDispatch`, `finish` appends `(service, events)` to it under a mutex, because a host may run operations concurrently under one context. `Result.pending` stays empty, so `Pending()` is false and an unconditional `result.Dispatch` cannot deliver twice. Engine-led operations ignore the queue.
- **Default:** no queue. Host-led events stay on `Result`, as today, so direct engine callers, the README and `examples/*` are unchanged.
- **Override:** the host installs the queue in the middleware or unit of work that begins its transaction.
- **Stated limits:**
  - A queue that is never dispatched after a commit delivers nothing in-process. The relay still delivers from the outbox. The godoc says so.
  - Dispatch detaches cancellation exactly as `deliver` does.
- **Alternatives considered:**
  - *An after-commit hook on the `Store` port.* `database/sql`, pgx and GORM transactions the host opened expose no commit callback, so it would work for memstore only. Rejected.
  - *Refusing host-led transactions in the transport and sweeper.* That removes the documented way to make task changes atomic with host writes. Rejected.
  - *Keeping `Result.pending` set as well.* Every host that already calls `Result.Dispatch` would then deliver twice. Rejected.

### 2. Every dispatch path reports consumer errors to `WithDispatchErrorHandler`, and returns them

- **Behaviour:** `Result.Dispatch`, `DeferredDispatch.Dispatch` and `SweepResult.Dispatch` call the originating service's `onDispatchError` with the joined error when it is non-nil, then return it.
- **Default:** the hook is a no-op, as today.
- **Override:** `WithDispatchErrorHandler`.
- **Stated limit:** a host that logs both the hook and the returned error logs twice. The godoc says the returned error is for control flow, and the hook is the place to log.
- **Behaviour change before tag:** `Result.Dispatch` now also calls the hook. It is recorded here.
- **Alternative considered:** *return-only, as today.* The engine-led and host-led paths would then differ in observability, and the transport has nothing to return to. Rejected.

### 3. `Sweep` never dispatches, and hands back through `SweepResult`

```go
func (r SweepResult) Pending() bool
func (r SweepResult) Dispatch(ctx context.Context) error
```

- **Behaviour:**
  - `Sweep` drops lines 191-195.
  - For each escalation with `Pending()`, it keeps the `Result` in an unexported `pending []Result` on `SweepResult`.
  - `SweepResult.Dispatch` dispatches them in escalation order, with Decision 2's error routing.
  - Engine-led escalations have already dispatched inside `finish`. Under a `DeferredDispatch`, `Pending()` is false, so nothing is held.
- **Default:** host-led events are handed back on the `SweepResult`.
- **Override:** a `DeferredDispatch` on the sweep context. `SweepResult.Dispatch` stays a safe no-op then.
- **Stated limit:** under a host transaction, the claim and every escalation share the host's transaction. The `Sweep` godoc is corrected to say so. A failed escalation then aborts the host's transaction on dialects such as PostgreSQL. That is the host's choice to sweep inside its transaction.
- **Alternative considered:** *send the hand-back error to `WithSweepErrorHandler`.* The error is a consumer error, not a sweep error, and Decision 2 routes all consumer errors to one hook. Rejected.

### 4. The transport never dispatches, and declines what no one will dispatch

```go
// ErrDispatchNotDeferred reports host-led events that no in-process consumer
// will see, because nothing was left to dispatch them after the commit.
var ErrDispatchNotDeferred = errors.New("hmntsk: host-led dispatch was not deferred")

// Decline reports pending events to the dispatch error hook as
// ErrDispatchNotDeferred instead of dispatching them. It is a no-op when
// nothing is pending.
func (r Result) Decline(ctx context.Context)
```

- **Behaviour:** `respond` calls `result.Decline(ctx)` in place of `Dispatch`, then encodes as before.
  - With no host transaction, `Pending()` is false, because the engine already dispatched. This is a no-op.
  - Under a `DeferredDispatch`, `Pending()` is also false, and the host dispatches after its commit.
  - Under a bare host transaction, the hook receives `fmt.Errorf("%w: task %s, %d event(s)", ErrDispatchNotDeferred, id, n)`.
- **Default:** safe. No early dispatch, and no silent drop. The outbox and relay are the durable path.
- **Override:** install `ContextWithDeferredDispatch` in the host's transaction middleware. The README and the `transport/core` package doc show a net/http, gin and fiber-neutral pattern.
- **Why no transport option:** the context-carried queue is already the override point, and a transport option could only choose to dispatch early or to drop. Both break the spec. Rule 4 of `library-design.md`: the limit is stated, not relaxable.
- **Why not a construction error:** whether a host wraps handlers in a transaction is invisible at `transportcore.New`. Rule 6 cannot apply, so the runtime report is the next best thing.
- **Alternative considered:** *answer `500` when events are pending without a queue.* The host middleware still commits, because the handler returned normally, so the client would see a failure for a change that happened. Rejected.

## Risks / Trade-offs

- [A host wraps handlers in a transaction and relied on in-process consumers running early.] → They stop running and the hook reports `ErrDispatchNotDeferred` on every such request. The migration note in the README shows the one-line middleware change.
- [A `DeferredDispatch` context outlives its transaction and is reused.] → `Dispatch` drains the queue, so reuse cannot double-deliver. The godoc says to take one per transaction.
- [Double logging from the hook and the returned error.] → Documented. See Decision 2.

## Migration Plan

Additive API, no schema change, no module ordering beyond the usual `hmntsk` then `transport/core` then bindings. Rollback is a revert.

Two behaviour changes are recorded before the first tag:
- `Result.Dispatch` calls the hook.
- A transport request under a host transaction without a queue no longer dispatches.

## Assumptions

- The reviewer was not asked. It is assumed that an additive, context-carried queue is preferred over a `Store` port change.
- It is assumed that the transport default should decline and report, not dispatch early.
- memstore's isolation, where reads outside the transaction do not see uncommitted writes, is enough to reproduce T6. The SQL dialects are not needed for these tests, because the defect is in the dispatch order, not in a driver.

## Open Questions

- Should `Sweeper.Run` refuse a context that already carries a host transaction, since one transaction around an unbounded loop is a wiring mistake? This is deferred to the sweeper-hardening change. It does not affect this change's specs or tasks.
