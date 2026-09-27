Every task is test-first (`.claude/rules/golang-tdd.md`, `.claude/rules/error-reproducible.md`).

- Port or write the failing test, then run it with `GOTOOLCHAIN=go1.26.8 go test -run '<TestName>' -count=1 ./...` in the owning module, and watch it fail for the reason stated.
- Then make the smallest change that turns it green. Then refactor, and consider `/simplify` on the touched code.

Conventions:
- Tables with two or more cases follow the `table-test` skill: the `assert` closure form, and `t.Context()`.
- Memstore is the right backend here, because the defect is dispatch order, not a dialect.
- Navigate with gopls at `$(go env GOPATH)/bin/gopls`.
- Never run `make tidy` or `go mod tidy`.
- Scratch module: `$V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`.

## 1. hmntsk: dispatch errors reach the hook on every path (Decision 2)

- [ ] 1.1 Red: in `service_dispatch_test.go`, add table test `TestHostLedDispatchReportsConsumerErrors`, with a failing handler and a recording `WithDispatchErrorHandler`.
  - Case "Result.Dispatch after commit": the hook receives the error, and `Dispatch` returns it.
  - Case "no hook configured": `Dispatch` returns the error and does not panic.

  Run it, and see the first case fail because the hook was never called.
- [ ] 1.2 Green: `Result.Dispatch` calls `service.onDispatchError` with the joined error when it is non-nil, then returns it. Verify 1.1 and the existing `TestServiceHostLedTransactionWithholdsDispatch` pass.

## 2. hmntsk: DeferredDispatch (Decision 1)

- [ ] 2.1 Red: add table test `TestDeferredDispatch` in `service_dispatch_test.go`, using memstore `ContextWithTx`. Cases:
  - two host-led operations under one scope: nothing is delivered before `Dispatch`, both are delivered after commit plus `Dispatch` in operation order, and each `Result.Pending()` is false;
  - rollback, then drop the scope: nothing is delivered;
  - `Dispatch` twice: the second call delivers nothing;
  - an engine-led operation under a scope dispatches at once and queues nothing;
  - a failing consumer: the hook receives the error, and `Dispatch` returns it.

  Run it, see it fail to compile, then add stubs and see it fail on the assertions.
- [ ] 2.2 Green: add `ContextWithDeferredDispatch`, `DeferredDispatch` (mutex-guarded queue of `(service, events)`) and `Dispatch`. In `finish`, when `hostLed`, queue on the scope if one is present, and otherwise set `result.pending`. Verify 2.1 passes, and passes under `-race`.
- [ ] 2.3 Add godoc naming the default (no scope: the hand-back is on `Result`) and the stated limit (an undispatched scope delivers nothing in-process, and the relay still delivers). Add a runnable `ExampleContextWithDeferredDispatch`. Verify with `go test -run Example ./...`.

## 3. hmntsk: Sweep never dispatches inside a host transaction (C6, Decision 3)

- [ ] 3.1 Red: port `$V/core/sweep_test.go` `TestSweep_InHostTransactionDoesNotDispatchBeforeCommit` into the root package's `sweep_test.go` as table test `TestSweepInHostTransaction`, using the repo's sweep harness in place of the scratch `newFixture`. Cases:
  - "rollback: no consumer saw the escalation": the scratch assertion, where `dispatchedBeforeCommit` is zero;
  - "commit, then SweepResult.Dispatch delivers": `Pending()` is true, and the escalation reaches the handler only after `Dispatch`;
  - "deferred scope collects the sweep": `SweepResult.Pending()` is false, and the scope's `Dispatch` delivers;
  - "engine-led sweep unchanged": dispatched per escalation, and `Pending()` is false;
  - "hand-back consumer error reaches the dispatch hook".

  Run it, and see the first case fail with the scratch message "an in-process handler was told about an escalation before the host committed".
- [ ] 3.2 Green: remove the inline `Dispatch` from `Sweep`. Hold pending `Result`s on `SweepResult`, and add `SweepResult.Pending` and `SweepResult.Dispatch` with Decision 2 routing. Verify 3.1 and every existing `sweep_test.go` test pass.
- [ ] 3.3 Refactor and document (observation): correct the `Sweep` godoc, which says each escalation "runs in its own transaction". Say that under a host transaction the claim and escalations share it, and dispatch is handed back. Verify with the root docs test.

## 4. transport/core: respond never dispatches (T6, Decision 4)

- [ ] 4.1 Red, engine half: add `ErrDispatchNotDeferred` and `Result.Decline` behind a table test `TestResultDecline` in `service_dispatch_test.go`. Cases:
  - pending host-led result: the hook receives an error that `errors.Is` `ErrDispatchNotDeferred` and that names the task, and no consumer runs;
  - nothing pending: no hook call.

  See it fail, then implement. Verify it passes.
- [ ] 4.2 Red, transport half: port `$V/transport/dispatch_test.go` `TestHostLedDispatch` into `transporttest`.
  - Add a `HostTransactionMount func(t *testing.T, api *transportcore.API, tx TxFunc) Binding` and `RunHostTransactionSuite(t, mount HostTransactionMount)`, with the scratch `observed` recorder and `createPooled` flow.
  - Cases, as a table with each binding's harness:
    - "default: no dispatch before commit, declined dispatch reported": `200`, zero handler runs, and the hook got `ErrDispatchNotDeferred`;
    - "override: consumer reads RESERVED after commit": the middleware installs `ContextWithDeferredDispatch`, runs `store.Do`, then calls `Dispatch`. This is the scratch case "a consumer reading the task back sees the committed claim";
    - "override: a failing consumer's error reaches the hook": this is the scratch case "a failing consumer's error reaches the dispatch error hook";
    - "no host transaction: unchanged".
  - Wire `RunHostTransactionSuite` into the net/http, gin and fiber binding tests, using the scratch `serveHTTP`, `serveGin` and `serveFiber` middleware as the harnesses.
  - Run `make transport-matrix` and see the default and override cases fail on all three bindings: the consumer read `READY`, and the hook was empty.
- [ ] 4.3 Green: in `transport/core/api.go` `respond`, replace `_ = result.Dispatch(ctx)` with `result.Decline(ctx)`, and update its godoc. Verify `make transport-matrix` passes, including the existing `RunSuite`.
- [ ] 4.4 Refactor: fold any harness duplication between `RunSuite` and `RunHostTransactionSuite` in `transporttest`. Re-run `make transport-matrix`.

## 5. Documentation (observations and override pattern)

- [ ] 5.1 README:
  - Under "Transactions", add the host-middleware pattern with `ContextWithDeferredDispatch`, and the note that the transport declines to dispatch without it.
  - Under "Events", say that consumer errors reach `WithDispatchErrorHandler` on every path.
  - Update the `WithDispatchErrorHandler` and `Result.Dispatch` godoc to match: log in the hook, and the returned error is for control flow.
  - Add a `transport/core` package-doc paragraph on host transactions.

  Verify the root and `transport/core` docs tests pass.
- [ ] 5.2 Check that `examples/store-drivers`, `examples/contextual-ui` and `examples/correlated-tasks` build and pass unchanged, since they use `Result.Dispatch` without a scope: `cd examples && go test ./...`.

## 6. Checks

- [ ] 6.1 From the repository root, run the full check and verify everything passes: `GOTOOLCHAIN=go1.26.8 make lint split-check test test-race transport-matrix`. Also run `go test -count=20 -run 'TestSweepInHostTransaction|TestDeferredDispatch' .` to confirm the tests are deterministic.
