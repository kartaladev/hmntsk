Every task is test-first. Write the failing test, run it with `GOTOOLCHAIN=go1.26.8 go test -run '<TestName>' -count=1 ./...` in the module, and watch it fail for the intended reason. Then make the smallest change that turns it green.

Conventions:
- Tables with two or more cases follow the `table-test` skill.
- Brokers come from the existing `delivery/redis` test helpers, per `use-testcontainers`.
- Navigate with gopls at `/Users/zakyalvan/go/bin/gopls`.
- Never run `make tidy` or `go mod tidy`.

## 1. transport/fiber: App() without a catch-all

- [ ] 1.1 Write table test `TestAppServesRoutesAddedAfterConstruction`, with a host `GET /healthz` added after `App()`. Cases:
  - the host route answers 200;
  - an unknown path is a JSON 404 with code `not_found` and no `Allow` header;
  - a wrong method on `/tasks/{id}` is the same 404 with no `Allow`.

  Verify the first case fails today with 404.
- [ ] 1.2 Add `NotFoundErrorHandler(next fiber.ErrorHandler)`. Build `App()` with it as the Fiber app's `ErrorHandler`, and remove the `Use`. Verify 1.1 passes.
- [ ] 1.3 Write `TestNotFoundErrorHandlerPassesOtherErrorsOn`. A host route returning `fiber.ErrBadRequest` reaches a recording `next`, and a nil `next` falls back to `fiber.DefaultErrorHandler`. Verify it passes.
- [ ] 1.4 Add a second `transporttest.RunSuite` run whose harness builds the app with `App()` and an actor func reading `transporttest.ActorHeader`. Keep the existing `Mount` harness. Verify both suites pass.

## 2. hmntsk: WithEventHandlerFactory

- [ ] 2.1 Write table test `TestWithEventHandlerFactory`. Cases:
  - the factory receives the service being built, and its handler gets events;
  - a factory error fails `New`, and `errors.As` finds the cause;
  - a nil factory, and nil handlers in its result, are ignored;
  - a non-transactional store is refused without calling the factory.

  Verify it fails to compile, then implement the ordered handler entries resolved in `New` after the transactional check. Verify it passes.
- [ ] 2.2 Write `TestEventHandlersDispatchInRegistrationOrder` (handler, factory, handler) against the recorded call order. Verify it passes.
- [ ] 2.3 Write `TestKindOnCompletedFromFactory`: `Define` plus `OnCompleted` inside a factory receive a typed completion. It is the schema-form case without the late-bound variable. Verify it passes.
- [ ] 2.4 Add the godoc stated limit (a factory must not run lifecycle operations) and a runnable `ExampleWithEventHandlerFactory`. Update `README.md` where it shows `WithEventHandlers` with a service-dependent handler. Verify with `go test -run Example ./...` and the root docs test.

## 3. delivery/redis: WithExactTrim

- [ ] 3.1 Extend the retention validation table with "exact trim without a bound is a ConfigurationError", and exact trim with `WithMaxLen` or `WithMaxAge` accepted. Verify it fails, then add `WithExactTrim()` and the check. Verify it passes.
- [ ] 3.2 Write an integration test on the default node size: `WithMaxLen(2)` plus `WithExactTrim()` after 10 publishes holds exactly 2 entries, while the same case without exact trimming holds more than 2. Verify the first fails before `trim` honours `exact`, then set `Approx = !exact`. Verify both pass.
- [ ] 3.3 Write an integration test for `WithMaxAge` plus `WithExactTrim()` with a test clock: after a publish, no entry is older than the cutoff. Verify it passes.
- [ ] 3.4 Document exact trimming in the godoc and in `docs/delivery.md`, under "Bounding the Redis stream": the per-publish cost, no per-publish limit, and the one-time manual `XTRIM` before enabling it. Verify the `delivery/redis` docs test passes.

## 4. Documented limit: LastError

- [ ] 4.1 Update the godoc for `OutboxEntry.LastError`, `AttemptRecord.LastError` and `DeadLetter.LastError`, and add the dead-letter inspection paragraph to `docs/delivery.md` (format, not a parse contract, `Accepted` and `WithRelayErrorHandler` for typed errors). Verify the root and relay docs tests pass.

## 5. Verification

- [ ] 5.1 Run the full check from the repository root and verify everything passes: `GOTOOLCHAIN=go1.26.8 make lint split-check test test-race test-integration vuln`.
