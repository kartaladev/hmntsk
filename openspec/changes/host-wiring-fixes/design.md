## Context

See proposal.md for why. The current state, checked in the code and in the pinned dependencies:

- **`transport/fiber/app.go:69-79`:** `App()` calls `Mount`, then `app.Use(NotFoundHandler())`. A `Use` without a prefix matches every path, so any route registered afterwards sits behind it.
  - gin's `Engine()` uses `NoRoute`/`NoMethod`, which don't block later routes.
  - net/http's `Mount` registers `"/"`, the least specific pattern, so later host patterns win.
- **Fiber v3.5.0:** when no route matches, the router returns `ErrNotFound`, or `ErrMethodNotAllowed` when the path matches under another method, and passes it to the app's `ErrorHandler` (`router.go:768-785`). Before returning `ErrMethodNotAllowed` it appends an `Allow` header (`router_skip.go:251`). The contract never sends `Allow`.
- **The shared suite** (`transporttest/errors.go:236`) asserts the unknown-route JSON `404`. But the Fiber test harness (`transport/fiber/app_test.go:57-70`) builds its own app with `Mount` plus `Use`, so `App()` itself never runs the suite.
- **`hmntsk.New`** (`service.go:108-136`) applies options in order and then refuses a non-transactional store. Handlers are dispatched sequentially in slice order (`service.go:711-727`).
- **`Kind.OnCompleted`** (`kind.go:192`) uses only the kind's name. It needs a service only because `Define` does.
- **`delivery/redis/retention.go:93-106`:** `trim` always sets `Approx = true` for both `MaxLen` and `MinID`. `docs/delivery.md` explains approximate trimming and its per-publish limit of 100 × `stream-node-max-entries`. go-redis sends `MAXLEN n` or `MINID id` without `~` when `Approx` is false. Exact trimming accepts no `LIMIT`.
- **`relay/relay.go:488`:** `LastError` is `strings.Join(failures, "; ")`, each part `sink + ": " + cause.Error()`. Typed causes reach `WithRelayErrorHandler` wrapped with `%w`.

## Goals / Non-Goals

**Goals:**
- An app from `App()` is as extensible as gin's `Engine()`, with identical unknown-route answers.
- A handler that depends on the service is wired at construction, with no forward-declared variable.
- An exact Redis bound for hosts that need one, with the default unchanged.
- `LastError`'s shape is documented as a limit.

**Non-Goals:**
- Lifecycle operation authorization (follow-up change `lifecycle-authorization`).
- Per-sink failures stored on the outbox row.
- Changing net/http's `Handler()`, which returns a closed `http.Handler`; hosts that need more routes already use `Mount`.

## Decisions

### 1. `App()` answers unknown routes through Fiber's error handler

```go
// NotFoundErrorHandler answers the router's not-found and method-not-allowed
// errors in the contract's JSON 404 and passes every other error to next.
func NotFoundErrorHandler(next fiber.ErrorHandler) fiber.ErrorHandler

func App(api *transportcore.API, opts ...Option) (*fiber.App, error) {
    app := fiber.New(fiber.Config{ErrorHandler: NotFoundErrorHandler(fiber.DefaultErrorHandler)})
    // Mount, no Use
}
```

- **Matching errors:** `errors.Is(err, fiber.ErrNotFound)` or `errors.Is(err, fiber.ErrMethodNotAllowed)`. For those, the handler deletes the `Allow` response header and writes `transportcore.NotFoundResponse()`. A wrong method on a contract path is therefore a `404` with no `Allow`, as today and as on gin and net/http.
- **Nil `next`:** means `fiber.DefaultErrorHandler`.
- **Default:** `App()` serves the contract plus whatever the host adds.
- **Override:**
  - a host that wants its own app, middleware or config uses `fiber.New(fiber.Config{ErrorHandler: fibertransport.NotFoundErrorHandler(mine)})` and `Mount`;
  - `NotFoundHandler` stays for hosts that prefer a trailing `Use`.
- **Stated limit:** on an app using this handler, a host handler that itself returns `fiber.ErrNotFound` or `fiber.ErrMethodNotAllowed` gets the contract's `404` body. The godoc says so. A host that needs its own `404` body returns its own response instead of that error.
- **Alternatives considered:**
  - *An `App` option registering extra routes before the catch-all.* It only works for routes known at construction, and adds an option that duplicates `Mount`. Rejected.
  - *Dropping the catch-all entirely.* Fiber's plain-text `404` would then break the "JSON on every answer" contract. Rejected.
  - *Documentation only.* It leaves `App()` returning an app that looks extensible but isn't. Rejected.

### 2. `WithEventHandlerFactory` builds handlers from the service, in registration order

```go
// WithEventHandlerFactory adds in-process consumers built from the service being
// constructed. New calls factory once, after every option is applied and the store
// is accepted, and before New returns. An error from it fails New. A nil factory,
// and nil handlers in what it returns, are ignored, as WithEventHandlers ignores
// nil handlers.
func WithEventHandlerFactory(factory func(*Service) ([]EventHandler, error)) Option
```

- **Order:**
  - `WithEventHandlers` and `WithEventHandlerFactory` both append an entry, either a handler or a factory, to one ordered list in `Service`.
  - After the transactional check, `New` resolves the list in order into `s.handlers`, calling each factory at its position.
  - Dispatch order is therefore registration order. The spec asserts it, because handlers run sequentially and a host can observe the order.
- **Errors:** a factory error is returned as `fmt.Errorf("hmntsk: build event handlers: %w", err)`, so `errors.As` still finds a `*ConfigurationError` or `*ValidationError` from `Define`.
- **Default:** no factories, and behaviour is unchanged.
- **Override:** this option is the host's control. Existing `WithEventHandlers` is unchanged.
- **Stated limit:** a factory receives a service that `New` has not returned yet. It may call configuration methods (`Register`, `Define`, `Registry`, `Clock`), but must not run lifecycle operations, because handlers from later entries aren't registered yet. The godoc says so. It isn't enforced, because enforcing it would add a construction flag checked on every operation, for a mistake the godoc names plainly.
- **Alternatives considered:**
  - *`Service.AddEventHandler` after construction.* It changes a service that is already in use, so `deliver` would need a lock. Rejected.
  - *Making `Kind` creatable without a service.* It fixes only typed completion handlers, not handlers that create follow-up tasks. Rejected.
  - *Running factories before other options, or appending them last.* The order would then differ from what the host wrote. Rejected.

### 3. `WithExactTrim()` for Redis stream bounds

- **Retention:** `retention` gains `exact bool`, and `trim` sets `args.Approx = !r.exact`. It applies to both `MaxLen` and `MinID`, and a trim mode is still sent when set.
- **Validation:** `WithExactTrim()` without `WithMaxLen` or `WithMaxAge` is a `ConfigurationError`, consistent with a trim mode that has no bound.
- **Default:** approximate, unchanged, because it is cheaper per publish and needs no LIMIT.
- **Override:** `WithExactTrim()`.
- **Stated cost, in godoc and `docs/delivery.md`:**
  - exact trimming makes the broker split stream nodes on most publishes, which costs CPU and memory on every publish;
  - it has no per-publish limit, so enabling it on a large existing stream removes the whole excess in the first publish, which blocks the broker for that command.
- **Alternatives considered:**
  - *`WithTrimPrecision(TrimApprox|TrimExact)`.* A two-valued enum whose zero value is the default adds a type for nothing. Rejected.
  - *Exact only for length.* An age bound has the same node-size surprise. Rejected.

### 4. `LastError` is documented, not changed

- **godoc on `OutboxEntry.LastError`, `AttemptRecord.LastError` and `DeadLetter.LastError`:**
  - it is human-readable text, one part per refusing sink, in sink order, `<sink>: <message>`, joined with `"; "`;
  - it is not a format to parse, and sink messages may contain the separator;
  - which sinks took the event is in `Accepted`;
  - typed per-sink errors are available while the pass runs, through `relay.WithRelayErrorHandler`.
- **`docs/delivery.md`:** one paragraph under inspecting dead letters, stating the same limit and the follow-up.
- **Default:** as today.
- **Override:** the error handler hook.
- **Why no fix now:** storing per-sink failures is a schema change in every dialect and store.

## Risks / Trade-offs

- **[A host relied on `App()` answering `404` for routes added later]** → Unlikely, since it contradicts adding them. Recorded as a behaviour change before the first tag.
- **[Fiber changes how unmatched routes reach the error handler]** → The Fiber module runs the shared suite against `App()` itself, plus a test for routes added afterwards, so a Fiber upgrade that changes this fails the build.
- **[A factory runs a lifecycle operation]** → Documented. Events from it reach only the handlers resolved so far, never lost from the durable record.
- **[Exact trim blocks Redis on a large stream]** → Opt-in, and the cost is documented with the mitigation: trim once by hand with `XTRIM`, then enable it.

## Migration Plan

All additions are backward compatible. Hosts using `App()` get extensibility with no change. PR #14 drops its three workarounds after rebasing. Rollback is reverting the change.
