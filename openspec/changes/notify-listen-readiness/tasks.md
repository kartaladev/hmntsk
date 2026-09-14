Every task is test-first. Write the failing test, run it with `GOTOOLCHAIN=go1.26.8 go test -run '<TestName>' -count=1 ./...` in the module, and watch it fail for the intended reason. Then make the smallest change that turns it green.

Conventions:
- Tables with two or more cases follow the `table-test` skill.
- Mocks come from `use-mockgen`. Brokers come from the existing `notify/redis` and `notify/nats` test helpers, per `use-testcontainers`.
- Navigate with gopls at `/Users/zakyalvan/go/bin/gopls`.
- Never run `make tidy` or `go mod tidy`.

## 1. notify: the ready contract and the in-process broadcaster

- [x] 1.1 Write table test `TestInProcessBroadcasterListenRefusesMissingFunctions` (nil deliver, nil ready: both a `*ConfigurationError`, and ready is never called). Then add `ready func()` to `Broadcaster.Listen` and `InProcessBroadcaster.Listen`, with the nil check. Verify it passes.
- [x] 1.2 Write `TestInProcessBroadcasterCallsReadyOnceRegistered`: a broadcast made from inside `ready` is delivered. Then call `ready` after the listener is registered. Verify it passes.
- [x] 1.3 Regenerate `realtime_mock_test.go` with its `//go:generate` directive. Update the test broadcasters in `service_test.go` and `service_ops_test.go` to the new signature. Verify with `go vet ./...` in `notify`.

## 2. notify: hub run states and Ready()

- [x] 2.1 Write table test `TestHubRunningOnlyAfterReady` with a mock broadcaster whose `Listen` blocks before calling `ready`. Cases:
  - before `ready`: `Running()` is false and `Subscribe` matches `ErrUnavailable`;
  - after `ready`: `Running()` is true and `Subscribe` succeeds;
  - after `Listen` returns: `Running()` is false again.

  Then implement the idle, starting and receiving states. Verify it passes.
- [x] 2.2 Write `TestHubReadyChannel`. It covers:
  - the channel from `Ready()` taken before `Run` closes on `ready`;
  - a `Ready()` taken after the run ends is open again and closes on the next run's `ready`;
  - a run whose `Listen` fails before `ready` never closes it.

  Then implement `Hub.Ready()`. Verify it passes.
- [x] 2.3 Write `TestHubIgnoresLateAndRepeatedReady`: `ready` called twice, or after `Listen` returned, doesn't change `Running()` or a later run's channel. Then guard with a per-run `sync.Once` and a run generation. Verify it passes with `-race`.
- [x] 2.4 Confirm that a second `Run` while starting or receiving still returns `ConfigurationError`: extend the existing hub test with the starting case, then run it.
- [x] 2.5 Update the SSE and WebSocket tests that poll `Running()` (`notify/websocket/helpers_test.go` and others found with gopls references to `Hub.Running`) to wait on `Ready()` where that simplifies them. Verify with `go test ./...` in `notify` and `notify/websocket`.

## 3. notifytest: broadcaster conformance suite

- [x] 3.1 Add `RunBroadcasterSuite(t, newPair)` to `notify/notifytest` with the four cases in design decision 4. Run it from a new in-process test in `notify`. Verify it passes. Temporarily call `ready` before registering the listener and confirm case 3 fails; then revert.

## 4. notify/redis

- [x] 4.1 Run `RunBroadcasterSuite` against two Redis broadcasters on one test server. Verify it fails to compile, or fails, before the adapter changes.
- [x] 4.2 Add `ready` to `Listen` with the nil check. Require `Receive` to return a `*goredis.Subscription`, then call `ready` before draining. Verify the suite and the existing `listen`, `multiinstance` and `reconnect` tests pass.

## 5. notify/nats

- [x] 5.1 Write table test `TestNewBroadcasterSubscribeTimeout` (default `DefaultSubscribeTimeout`, an override, zero and negative both `ConfigurationError`). Then add `DefaultSubscribeTimeout` and `WithSubscribeTimeout`. Verify it passes.
- [x] 5.2 Run `RunBroadcasterSuite` against two NATS broadcasters on one test server. Then add `ready` with the nil check, and a `FlushWithContext` bounded by the subscribe timeout before calling `ready`. Verify the suite passes.
- [x] 5.3 Write `TestListenReturnsErrorWhenSubscriptionIsNotConfirmed`: a connection to a stopped server with a short subscribe timeout makes `Listen` return an error without calling `ready`, and a cancelled context returns the context's error. Verify it passes. Also verify the existing `listen`, `multiinstance` and `reconnect` tests pass.

## 6. Docs and verification

- [x] 6.1 Update the godoc for `Broadcaster`, `Hub.Run`, `Hub.Running` and `Hub.Ready`, including the `select` on `Ready()` and the `Run` error, and the stated limits (the reconnect gap, a stale `Ready()` channel). Update the adapter docs and notify's docs pages where they describe `Listen` or `Running`. Verify the docs tests (`docs_test.go`) pass in each module.
- [x] 6.2 Run the full check from the repository root and verify everything passes: `GOTOOLCHAIN=go1.26.8 make lint split-check test test-race test-integration vuln`.
