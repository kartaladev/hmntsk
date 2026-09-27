Every defect's work starts the same way, per `.claude/rules/golang-tdd.md` and `.claude/rules/error-reproducible.md`:

1. **Port.** Port the named scratch reproduction from `$V/notify` into the repo, next to the code it covers (`V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`). Use the `table-test` form: `assert` closures, and `t.Context()`. NATS comes from `nats.RunTestNATS`, and SQL databases from the existing sqlstore harness (`use-testcontainers`). A case every store must satisfy goes into the `notifytest` suite.
2. **Red.** Run `GOTOOLCHAIN=go1.26.8 go test -run '<TestName>' -count=1 ./...` in the owning module, and watch it fail for the reason the audit recorded. A compile error is not a red.
3. **Green.** Make the smallest change that passes.
4. **Refactor.** Refactor with the tests green, and consider `/simplify`.

Other conventions:
- Navigate with gopls at `/Users/zakyalvan/go/bin/gopls`.
- Never run `go mod tidy` or `make tidy`.

## 1. Named policies become immutable (N9, observation fixed)

- [ ] 1.1 **Port** `$V/notify/globals/globals_test.go` `TestNilDefaultPolicyIsRefusedAtConstruction`, **red.** Before touching the API, run the scratch test against the repo and record that it fails (`An error is expected but got nil`). Then port its intent into `notify/authorize_test.go` as table test `TestDefaultPolicyIsIndependentOfOtherHandlers`, with two cases:
  - build a handler with `WithSubscriptionAuthorizer(AllowAll)`, then one with no policy: the second refuses alice following bob with `403`;
  - the same check for the WebSocket handler, in `notify/websocket/refusal_test.go`.

  Temporarily make `NewHandler` default to `AllowAll`, and confirm both cases fail.
- [ ] 1.2 **Green.** Replace `var SelfOnly`/`var AllowAll` with `func SelfOnly() SelfOnlyPolicy` and `func AllowAll() AllowAllPolicy` (design decision 5).
  - Default both constructors to `SelfOnlyPolicy{}`.
  - Migrate call sites found with gopls references: `notify/http.go` (and its "pass notify.AllowAll" detail), `notify/websocket/handler.go`, `authorize_test.go`, `http_test.go`, `websocket/handler_test.go`, `websocket/refusal_test.go` and `examples/notifications/main.go`.
  - Verify 1.1 and `go test ./...` pass in `notify`, `notify/websocket` and `examples/notifications`.

## 2. Follow is not write on WebSocket (N1)

- [ ] 2.1 **Port** `$V/notify/websocket_test.go` `TestWebSocketFollowerCannotMarkRecipientsNotifications`, **red.**
  - Port it into `notify/websocket/handler_test.go`, using the package's `startServer`/`dial` helpers.
  - Keep its two cases, `mark-read` naming bob's notification and `mark-all-read`. Each asserts bob's notification stays ACTIVE, and the reply is a `forbidden` error frame echoing `r1`.
  - Run it and see bob's notification become READ.
- [ ] 2.2 **Green.**
  - Add `MarkAuthorizer`/`MarkAuthorizerFunc` to `notify/authorize.go`, and implement `AuthorizeMark` on `SelfOnlyPolicy` and `AllowAllPolicy`.
  - Thread `actor` through `serve`, `read` and `answer` in `notify/websocket/handler.go`.
  - Authorize each mark before calling the service.
  - Map `ErrUnauthorized` to `forbidden` in `errorReply`.

  Verify 2.1 passes, and that the existing mark tests (own-connection mark-read, another recipient's notification not found) still pass.
- [ ] 2.3 Write table test `TestNewHandlerMarkAuthorizer` in `notify/websocket` with three cases:
  - **default:** the follower is refused;
  - **override:** a delegate policy lets alice mark bob's notification READ, with the reply `marked: 1`;
  - **wiring:** `WithMarkAuthorizer(nil)` is a `*notify.ConfigurationError`.

  Watch the override and nil cases fail before adding `WithMarkAuthorizer`, then add it. Verify it passes.
- [ ] 2.4 Write table test `TestMarkPolicies` in `notify/authorize_test.go`, covering `SelfOnly()` and `AllowAll()` as `MarkAuthorizer`: self, other and empty actor. Verify it passes. Temporarily invert `SelfOnlyPolicy.AuthorizeMark` and confirm it fails.

## 3. Followers get their own budget (N6)

- [ ] 3.1 **Port** `$V/notify/hub_test.go` `TestFollowersCannotExhaustRecipientsOwnStreamCap`, **red.**
  - Port it into `notify/hub_test.go` (it drives the SSE handler, as the scratch test does) as table test `TestHubStreamBudgets`.
  - Cases:
    - eight followers, then bob's own stream is `200`;
    - a ninth follower is `429` while bob still gets `200`;
    - `WithMaxFollowerStreamsPerRecipient(2)`: a third follower is `429`, and bob's own budget is still 8;
    - bob's own ninth stream is `429`.
  - See the first case fail with `429`.
- [ ] 3.2 **Green.**
  - Change `Hub.Subscribe(recipient)` to `Hub.Subscribe(actor, recipient)`, recording follower-ness on the `Subscription` and counting the two budgets separately.
  - Add `DefaultMaxFollowerStreamsPerRecipient` and `WithMaxFollowerStreamsPerRecipient`.
  - Update both callers: `notify/http.go` `stream` and `notify/websocket/handler.go` `ServeHTTP`.
  - Update the existing hub tests to the new signature.

  Verify 3.1 and `go test ./...` pass in `notify` and `notify/websocket`.
- [ ] 3.3 Extend the existing `NewHub` configuration table test with `WithMaxFollowerStreamsPerRecipient(0)` and `(-1)`, both `*ConfigurationError`. See them fail before the check, then add it. Verify it passes.
- [ ] 3.4 Extend the WebSocket cap test ("streams and WebSocket connections together") with a follower case: follower budget full across one SSE stream and WebSocket connections → `429` before upgrade. Verify it passes.

## 4. Stopping the hub closes open streams (N3)

- [ ] 4.1 **Port** `$V/notify/hub_test.go` `TestHubStopClosesOpenStreams`, **red.**
  - Port the SSE case into `notify/hub_test.go`, and the WebSocket case into `notify/websocket/shutdown_test.go`, in table form with a `stop()` that cancels `Run` and waits for it.
  - SSE asserts `io.EOF` with no heartbeat after stop.
  - WebSocket asserts a `CloseError` with status 1013.
  - See both keep heartbeating or answering.
- [ ] 4.2 **Green.**
  - Add `Subscription.Done()`. `Hub.endRun` ends and removes every subscription under `h.mu`, and `Close` stays idempotent.
  - The SSE `stream` returns on `Done()`.
  - WebSocket `serve` closes with `StatusTryAgainLater` on `Done()`.

  Verify 4.1 passes under `-race -count=20`.
- [ ] 4.3 Write `TestSubscriptionDoneClosesOnceOnRunEnd` in `notify/hub_test.go`, covering:
  - `Done()` is open while running and closed after `Run` returns;
  - a later `Close` doesn't panic;
  - a new `Run` accepts fresh subscriptions.

  Verify it passes with `-race`.

## 5. A closed NATS connection ends receiving (N2)

- [ ] 5.1 **Port** `$V/notify/nats_test.go` `TestNATSListenEndsWhenConnectionCloses`, **red.**
  - Port it into `notify/nats/listen_test.go` as a table test on real NATS through `RunTestNATS` with `WithTestContainer` and `WithTestConnectOptions(MaxReconnects(1), ReconnectWait(10ms), ClosedHandler(...))`.
  - Two cases: the host closes the connection, and the container is stopped until reconnects are exhausted.
  - Each asserts `Hub.Run` returns an error matching `natsgo.ErrConnectionClosed`, `Running()` is false, and `Subscribe` matches `ErrUnavailable`.
  - Use no sleeps: wait on the `ClosedHandler`, with a bounded select on the run error.
  - See it time out.
- [ ] 5.2 **Green.**
  - Register `conn.StatusChanged(natsgo.CLOSED)` before subscribing, and remove it on return.
  - Check `IsClosed()` after registering.
  - Select on the status channel in the receive loop and return the wrapped `ErrConnectionClosed`.

  Verify 5.1 passes with `-count=3`. Also verify that the existing `reconnect_test.go` (a drop that recovers keeps receiving) and the `RunBroadcasterSuite` conformance test still pass.
- [ ] 5.3 Write `TestListenOnClosedConnectionReturnsAtOnce`: `Listen` on an already-closed connection returns `ErrConnectionClosed` without calling `ready`. Verify it passes.

## 6. Bounded list filters (N7)

- [ ] 6.1 **Port** `$V/notify/sqlstore_test.go` `TestListFilterCountIsValidated`, **red.**
  - Add store-suite cases to `notify/notifytest/suite.go`, run through `notify.New(store).List`:
    - 70,000 kinds → `*ValidationError` pointing at `/kinds`;
    - 70,000 `ACTIVE` states → pointing at `/states`;
    - exactly `MaxListFilterValues` kinds → succeeds, filtered;
    - an empty kind, or one over `MaxKindBytes` → `/kinds/<i>`.
  - Run the suite through `notify/memory_test.go` and `notify/sqlstore/harness_test.go` (SQLite, PostgreSQL, MySQL containers).
  - See SQLite fail with "too many SQL variables" and PostgreSQL with its parameter limit.
- [ ] 6.2 **Green.** Add `MaxListFilterValues = 100` and the count and kind checks to `ListQuery.Validate`. Verify the suite passes on every store. Also add the unit cases to the existing `ListQuery.Validate` table test in `notify`.

## 7. An unparseable query string is a bad request (N7′)

- [ ] 7.1 **Port** `$V/notify/sqlstore_test.go` `TestListHTTPDropsFiltersPastQueryParamLimit`, **red.**
  - Port it into `notify/http_test.go` as table test `TestHandlerRefusesUnparseableQuery`.
  - Cases:
    - list with `kind=wanted` repeated 10,001 times → `400` validation error, no notifications;
    - list with `subject=%zz` → `400`;
    - stream with an unparseable query → `400`, no stream opened;
    - list with 101 distinct kinds → `400` pointing at `/kinds`.
  - See the first case answer `200` including the `excluded` notification.
- [ ] 7.2 **Green.** Add the exported `notify.ParseQuery` (design decision 7), and use it in `list` and `stream`. Verify 7.1 passes, and that the existing list, cursor and stream tests pass.
- [ ] 7.3 Add the WebSocket case to `notify/websocket/refusal_test.go`: an unparseable query is `400` before upgrade. See it fail, then use `notify.ParseQuery` in `ServeHTTP`. Verify it passes.

## 8. Docs, and the audit record

- [ ] 8.1 Update the godoc and docs:
  - `notify/websocket/doc.go`: marks follow the mark policy;
  - `notify/docs/realtime-operations.md`: mark policy and override example, two stream budgets, streams closing when `Run` ends with WebSocket 1013, NATS CLOSED ending `Run`, `GODEBUG=urlmaxqueryparams`;
  - `notify/docs/notifications.md`: `MaxListFilterValues`, and `SelfOnly()`/`AllowAll()`.

  Every new option names the default it replaces. Verify with each module's `docs_test.go`.
- [ ] 8.2 Record in this change's design that N7's HTTP `500` path was refuted (Go stops at 10,000 parameters) and superseded by N7′. Record that N9 was fixed here while `tasknotify.DefaultRules` is deferred. Verify both notes are present in design.md before archiving.

## 9. Final checks

- [ ] 9.1 From the repository root, run `GOTOOLCHAIN=go1.26.8 make lint split-check test test-race test-integration vuln`, and verify everything passes. Then run `openspec validate notify-realtime-access --strict`.
