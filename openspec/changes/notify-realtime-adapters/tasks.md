Every implementation task below is strict TDD:
- write the failing test first, and run it to watch it fail for the intended reason (not a compile error or a missing fixture);
- make it green with the smallest change;
- refactor.

Tables of cases follow the project `table-test` skill: `assert` closures, a `ctx` modifier where context matters, and `t.Context()`. Port doubles come from `use-mockgen` (`--typed`). Brokers come from `use-testcontainers`: one `RunTestX` helper per module in `testutils.go`, never mocks or fakes. Never run `make tidy` or `go mod tidy`. Always use `GOTOOLCHAIN=go1.26.8`.

## 1. Prerequisites (design decision 2)

- [x] 1.1 Confirm `notify-core` is applied, including its amendments for this change (notify-core decision 13, tasks 8.4, 8.5, 9.4, 9.5): `Hub.Subscribe`/`Subscription` (`Ready`, `Take`, `Close`), `Hub.Heartbeat`/`WriteTimeout`, the SSE handler built on `Hub.Subscribe`, `notify.WriteError`, and the signal codec (`EncodeSignals`, `DecodeSignals`, `SignalFormatVersion`, `ErrUnknownSignalFormat`) with its golden file `notify/testdata/signals.v1.json`. Verify with `go doc` in `notify` for each symbol; stop and raise it if any is missing.

## 2. Module scaffolding

- [x] 2.1 Create modules `notify/websocket`, `notify/redis` and `notify/nats` with `go.mod` and `doc.go`. Add them to `go.work` and `NOTIFY_MODULES`. Verify `GOTOOLCHAIN=go1.26.8 go build ./...` in each, and `make split-check` passes.
- [x] 2.2 Add `testutils.go` with `RunTestRedis` (pinned `redis:8.2.9-alpine`) in `notify/redis` and `RunTestNATS` (pinned `nats:2.12.7-alpine`) in `notify/nats`, copied from `delivery/*` and not imported. Verify that a smoke test per module starts the container, pings it, and terminates it through `t.Cleanup`.
- [x] 2.3 Verify that `make split-check` fails on a temporary import of `github.com/kartaladev/hmntsk/delivery/redis` from `notify/redis`, then revert it.

## 3. `notify/websocket`

- [x] 3.1 Add `github.com/coder/websocket` (latest tagged release) to `notify/websocket/go.mod` with `go get`, not tidy. Run `GOTOOLCHAIN=go1.26.8 govulncheck ./...` in the module. Verify it reports no vulnerability, and record the version in `design.md` if it differs from v1.8.15.
- [x] 3.2 Test-first, write `NewHandler` and its options. Verify with a table test:
  - a missing `WithActor` is a `ConfigurationError`;
  - a nil authorizer, a read limit ≤ 0, a ping interval ≤ 0 and a write timeout ≤ 0 are each a `ConfigurationError`;
  - the defaults are `SelfOnly`, same-host origin, 4096 bytes, and the hub's heartbeat and write timeout;
  - the route is `GET /v1/notifications/socket`.
- [x] 3.3 Test-first, write the pre-upgrade refusals, each answered by `notify.WriteError` with no upgrade:
  - no actor gives 403;
  - another recipient under `SelfOnly` gives 403;
  - a host policy permitting a supervisor upgrades;
  - the hub not running gives 503;
  - the ninth connection mixing SSE streams and WebSockets gives 429;
  - a foreign `Origin` gives 403;
  - `WithOriginPatterns("app.example.com")` permits that origin;
  - `WithAnyOrigin()` permits any origin.

  Verify with an `httptest` table test using a coder/websocket client dial.
- [x] 3.4 Test-first, write the write loop:
  - a published notification yields one `unread-changed` frame with `change` and `at`, and no other fields;
  - pings are sent every interval (fake short interval);
  - a client that stops reading is closed after the write timeout, while 1,000 publishes complete without blocking.

  Verify under `-race`, and with `goleak.VerifyNone` after the server closes.
- [x] 3.5 Test-first, write the read loop and replies:
  - `mark-read` with ref `r1` gives `marked` with `marked:1` plus an `unread-changed` `read` frame;
  - `mark-all-read` with and without `through`;
  - bob's ID and an unknown ID give byte-identical `not_found` error frames;
  - a malformed message gives a `validation` error frame, and the connection stays open;
  - a message over the read limit closes the connection with 1009.

  Verify with a table test over a live `httptest` connection backed by the memory store.
- [x] 3.6 Test-first, write shutdown: cancelling the server's base context closes open connections with 1001 and releases their subscriptions. Verify that `Hub` cap slots return to zero and goleak is clean.
- [x] 3.7 Add example tests mounting the handler on a stdlib `ServeMux` and on Gin via `gin.WrapH`. Verify both examples run. Document in `notify/docs/realtime-operations.md` that Fiber is unsupported.

## 4. `notify/redis` broadcaster

- [x] 4.1 Test-first, write `NewBroadcaster` and its options, `errors.go` (`ErrConfiguration`, `*ConfigurationError`, `ErrPublish`, `*PublishError`), `DefaultChannel` and `DefaultPublishTimeout`. Verify with a table test: a nil client, an empty channel and a timeout ≤ 0 are each a `ConfigurationError`, and the defaults apply.
- [x] 4.2 Test-first, write `Broadcast`:
  - publishes the notify codec encoding on the channel, split at `MaxSignalsPerMessage`;
  - an unreachable broker returns an error matching `ErrPublish` within the timeout.

  Verify against `RunTestRedis` with a raw subscriber asserting the payloads, and with a client pointed at a closed port.
- [x] 4.3 Test-first, write `Listen`:
  - it waits for subscription confirmation, then delivers each decoded signal;
  - an unknown-version message is reported to the decode error handler and later messages are still delivered;
  - ctx cancel returns nil and closes the `PubSub`.

  Verify against `RunTestRedis`, under `-race`, with goleak clean.
- [x] 4.4 Test-first, write reconnect: kill the client's connection (`CLIENT KILL TYPE pubsub`), then publish from another client after recovery, and the signal is delivered without restarting `Listen`. Verify against `RunTestRedis` with `require.Eventually`.

## 5. `notify/nats` broadcaster

- [x] 5.1 Test-first, write `NewBroadcaster` and its options, `errors.go`, and `DefaultSubject`. Verify with a table test: a nil conn, an empty subject, `notify.*`, `notify.>`, `notify..signals` and a trailing dot are each a `ConfigurationError`, and the defaults apply.
- [x] 5.2 Test-first, write `Broadcast`: publishes codec messages split at `MaxSignalsPerMessage`, and a closed connection returns an error matching `ErrPublish`. Verify against `RunTestNATS` with a raw subscriber.
- [x] 5.3 Test-first, write `Listen`: it delivers decoded signals, reports unknown versions to the handler and keeps receiving, and ctx cancel unsubscribes and returns nil. Verify against `RunTestNATS`, under `-race`, with goleak clean (ignoring nats.go's connection-owned goroutines through a `goleak.IgnoreTopFunction` documented in the test).
- [x] 5.4 Test-first, write reconnect: restart the NATS container, then a signal published after recovery is delivered to the same `Listen`. Verify against `RunTestNATS` with `require.Eventually`.

## 6. Multi-instance behaviour

- [x] 6.1 In `notify/redis`, write an integration test with two `notify.Service` + `notify.Hub` instances on separate clients to one Redis, sharing the memory store (or separate stores; only signals matter). A publish on A reaches an SSE stream on B; a publish on B reaches a stream on A; instances on different channels do not see each other. Verify it passes under `-race`.
- [x] 6.2 In `notify/nats`, write the same test with a WebSocket connection on B (importing `notify/websocket` in the test only, or an SSE stream if that would break the split graph). Verify it passes under `-race`.
- [x] 6.3 In both modules, write a test that publishing while the broker is stopped stores the notification, returns success, and calls the service's signal error handler with an error matching `ErrPublish`. Verify against a stopped container.

## 7. Documentation

- [x] 7.1 Write `notify/docs/realtime-operations.md`, per design decision 6. Verify with a docs test that the documented constant values match the code. It covers:
  - choosing a broadcaster;
  - Redis and NATS wiring snippets;
  - default channel and subject;
  - best-effort semantics;
  - proxy idle timeouts, buffering, upgrade headers and HTTP/2;
  - sticky sessions not required;
  - graceful shutdown of WebSockets;
  - the Fiber limit;
  - broker security for recipient identifiers;
  - the explicit note that these are not the `delivery/*` sinks.
- [x] 7.2 Add godoc to every exported identifier, naming the default each option replaces. Verify that `make lint` reports no missing doc comments.

## 8. Verification

- [x] 8.1 Run `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln`. Verify all pass, and `make split-check` confirms these modules import only `notify` and their client libraries.
- [x] 8.2 Run `/simplify` on the three modules and the notify-core amendments, re-run the full check, and verify it is still green.
