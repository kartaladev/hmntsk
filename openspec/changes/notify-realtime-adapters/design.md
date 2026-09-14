## Context

See proposal.md for why. The requirements are in `specs/notification-realtime/spec.md`. Constraints that shape the approach:

- **notify-core defines the realtime contract these adapters plug into** (its design §10 and §11):
  - `notify.Signal{Recipient, Change, At}`, `notify.Broadcaster{Broadcast(ctx, []Signal) error; Listen(ctx, deliver func(Signal)) error}`, `NewInProcessBroadcaster()`;
  - `notify.Hub` (`NewHub`, `Run`, `Running`) with capacity-1 coalescing delivery per stream, heartbeat 25s, write timeout 10s, 8 streams per recipient per instance;
  - `notify.SubscriptionAuthorizer` (`SelfOnly` default, `AllowAll`), and `NewHandler(svc, hub, WithActor, …)` with the SSE stream and error body `{"error":{"code","message"}}`.
- **Split rule.** Every module here imports only `notify` and its own client library, never hmntsk or the `delivery/*` modules. `make split-check` enforces it.
- **Precedents for style:** `delivery/redis` (`goredis.UniversalClient`, `ConfigurationError` → `ErrConfiguration`, `PublishError`) and `delivery/nats` (`*natsgo.Conn`, subject validation that refuses wildcards and empty tokens, `RunTestNATS`/`RunTestRedis` container helpers with pinned images `redis:8.2.9-alpine` and `nats:2.12.7-alpine`).
- The library-design rule requires each decision below to state its default and how a consumer overrides it.

## Goals / Non-Goals

**Goals:**
- A multi-instance deployment gets signals to every connection with one line of wiring.
- WebSocket clients get the same guarantees as SSE, plus marking read over the connection.
- No adapter can slow a publisher, leak content, or fail a notification write.

**Non-Goals:**
- Replay, persistence or ordering guarantees for signals. The store is the source of truth.
- Sharded Redis pub/sub (`SPUBLISH`) and JetStream. Both are possible later behind the same options.
- WebSocket support through Fiber's adaptor. Fiber hosts use SSE (see decision 6).
- Any change to `delivery/redis` or `delivery/nats`.

## Decisions

### 1. Spec delta: ADDED requirements under `notification-realtime`

- `notification-realtime` is introduced by the unarchived `notify-core` change, so it does not exist under `openspec/specs/` yet. A MODIFIED delta would have no requirement to modify.
- **Choice:** ADDED requirements under the same capability path, with no `## Purpose` (notify-core supplies it). `openspec validate --strict` accepts this, and at archive time, after notify-core, the requirements are appended to the existing capability.
- **Ordering constraint:** this change must be archived after `notify-core`.
- **Rejected:** new capabilities such as `notification-websocket` and `notification-broadcast`. They would split one contract (who receives which signals, and how) across three specs.

### 2. Amendments this change needs from notify-core (now included there)

All three are now part of notify-core's design (decision 13) and tasks (8.4, 8.5, 9.4, 9.5), and are implemented when notify-core is applied. They are recorded here as this change's reason for them:

1. **A public hub subscription**, so a second transport shares the cap, the running check and coalescing instead of reimplementing them:
   ```go
   func (h *Hub) Subscribe(recipient string) (*Subscription, error) // ErrUnavailable if !Running(); ErrTooManyStreams over the cap
   func (s *Subscription) Ready() <-chan struct{}                    // capacity 1: a signal is pending
   func (s *Subscription) Take() (Signal, bool)                       // the latest pending signal; clears it
   func (s *Subscription) Close()                                     // releases the cap slot; idempotent
   func (h *Hub) Heartbeat() time.Duration; func (h *Hub) WriteTimeout() time.Duration
   ```
   The SSE handler is rewritten on top of `Subscribe`. That is what makes "the cap counts streams and WebSocket connections together" true by construction.
2. **A shared HTTP error writer**, `notify.WriteError(w http.ResponseWriter, err error)`, mapping the notify sentinels to the status and body both transports use. Without it, the WebSocket module would have to copy the mapping.
3. **A shared signal codec**, `notify.EncodeSignals([]Signal) ([]byte, error)` and `notify.DecodeSignals([]byte) ([]Signal, error)` with `notify.SignalFormatVersion = 1` and `notify.ErrUnknownSignalFormat`. Redis and NATS then carry byte-identical messages, and an instance can switch brokers without a format change.

**If amendment 3 is rejected**, each broadcaster keeps a private copy of the codec, tested against the same golden file. Amendments 1 and 2 are not optional.

### 3. WebSocket library: `github.com/coder/websocket`

| | coder/websocket | gorilla/websocket |
|---|---|---|
| Latest release (checked 2026-09-14) | v1.8.15, 2026-06-15 | v1.5.3, 2024-06-14 |
| `context.Context` on read, write, ping | yes | no (deadlines only) |
| Fits `net/http` | `websocket.Accept(w, r, opts)` | `Upgrader.Upgrade` |
| Origin checking | built in (`OriginPatterns`, same-host default) | `CheckOrigin` callback, permissive if left nil |
| Dependencies | none | none |

- **Choice:** coder/websocket. It is actively released, its context-based API matches the hub's context lifecycle and goleak discipline, and its safe origin default matches the library-design rule.
- **Vulnerabilities:** `govulncheck` runs on the module in task 3.1 when the dependency is added, and in `make vuln`. A finding blocks the task.
- **Rejected:** gorilla/websocket (no release in over two years, no context), and a hand-rolled RFC 6455 implementation (a large, security-sensitive surface).

### 4. `notify/websocket`: API and defaults

Module `github.com/kartaladev/hmntsk/notify/websocket`, package `websocket`, following the `delivery/redis` precedent of naming the package after its technology. Hosts alias the library import.

```go
func NewHandler(svc *notify.Service, hub *notify.Hub, opts ...Option) (*Handler, error) // implements http.Handler
// Options and defaults:
//   WithActor(func(*http.Request) (string, error))     REQUIRED; absent -> ConfigurationError
//   WithSubscriptionAuthorizer(notify.SubscriptionAuthorizer)  default notify.SelfOnly; nil -> ConfigurationError
//   WithOriginPatterns(patterns ...string)             default none: same host only
//   WithAnyOrigin()                                    explicit opt-out of origin checking
//   WithReadLimit(bytes int64)                         default DefaultReadLimit = 4096; <= 0 -> ConfigurationError
//   WithPingInterval(time.Duration)                    default hub.Heartbeat() (25s); <= 0 -> ConfigurationError
//   WithWriteTimeout(time.Duration)                    default hub.WriteTimeout() (10s); <= 0 -> ConfigurationError
//   WithBasePath(string)                               default "/v1"; route GET {base}/notifications/socket
const Subprotocol = "notify.v1"
```

**Connection sequence** (one HTTP request goroutine owns the connection):
1. Resolve the actor. The recipient is `?recipient=` or the actor.
2. Authorize the subscription.
3. `hub.Subscribe(recipient)`.
4. `websocket.Accept`.

Every refusal happens before `Accept`, and is written with `notify.WriteError` as 403, 429 or 503. The subscription is closed on every exit path.

**Goroutines:**
- The request goroutine runs the write loop: `select` on `sub.Ready()`, the ping ticker, and the connection context.
- One child goroutine reads client messages and pushes replies onto a buffered reply channel (capacity 16) that the write loop drains. When the channel is full, the reader blocks, which applies backpressure to that client only.
- Both goroutines exit when the request context or the connection ends. `goleak` verifies this.

**Writes** use a context with `WithWriteTimeout`. A failed or timed-out write closes the connection with status 1011 and releases the subscription.

**Heartbeat:** `conn.Ping(ctx)` every ping interval, bounded by the write timeout. A missing pong closes the connection.

**Protocol** (JSON text frames; the `notify.v1` subprotocol is offered but not required):

| Direction | Message |
|---|---|
| server → client | `{"type":"unread-changed","change":"created","at":"2026-09-14T10:00:00.000000Z"}` |
| client → server | `{"type":"mark-read","ref":"r1","ids":["…"]}` |
| client → server | `{"type":"mark-all-read","ref":"r2","through":"…"}` (`through` optional) |
| server → client | `{"type":"marked","ref":"r1","marked":1}` |
| server → client | `{"type":"error","ref":"r1","code":"not_found","message":"…"}` |

- Errors use the HTTP error codes (`validation`, `not_found`, `internal`).
- A malformed message is answered with a validation error and does not close the connection.
- A message over the read limit closes the connection with 1009, enforced by the library's `SetReadLimit`.
- Mark requests call `svc.MarkRead` and `svc.MarkAllRead` for the connection's recipient only, so the not-found reply is the same for another recipient's notification and an unknown ID.

**Rejected alternatives:**
- Signals only, with marking through HTTP. The proposal asked for acknowledgement over the connection.
- A per-connection signal queue. Coalescing is sufficient, as notify-core decision 10 argues.

### 5. Cross-instance broadcasters

Both carry the notify-core codec (amendment 3): one message per `Broadcast` call, `{"v":1,"signals":[{"recipient":"alice","change":"created","at":"…"}]}`.

- A call carrying more than `MaxSignalsPerMessage = 500` signals is split into several messages. At about 100 bytes per signal, 500 signals stays well under NATS's default 1 MiB `max_payload`.
- Escalating to a large group is therefore a handful of publishes, not one per recipient.
- Recipient identifiers do travel on the broker. That is routing metadata, not notification content, and it is documented.

**`notify/redis`**, package `redis`:
```go
func NewBroadcaster(client goredis.UniversalClient, opts ...Option) (*Broadcaster, error)
// Options and defaults:
//   WithChannel(string)                        default DefaultChannel = "notify.signals"; empty -> ConfigurationError
//   WithPublishTimeout(time.Duration)          default DefaultPublishTimeout = 5s; <= 0 -> ConfigurationError
//   WithDecodeErrorHandler(func(ctx, error))   default no-op, documented as silent
```
- **Broadcast:** `PUBLISH` per message under the timeout. A failure is returned as a `*PublishError` matching `ErrPublish`, and the notify `Service` passes it to its signal error handler; the write is never failed.
- **Listen:**
  - `client.Subscribe(ctx, channel)`, then wait for the subscription confirmation before draining `PubSub.Channel(WithChannelSize(1000))`;
  - decode each message and call `deliver` for each signal. `deliver` is the hub's non-blocking send, so the channel drains fast;
  - on ctx done, close the `PubSub` and return nil. If the client is closed, return the error.
  - go-redis resubscribes automatically after a reconnect, which satisfies the "resumes without restart" requirement.
- **Redis Cluster:** classic `PUBLISH` is propagated to every node, so it works unchanged. Sharded pub/sub is a non-goal.
- **Not `delivery/redis`:** that sink appends durable events to a stream with `XADD`. This broadcaster publishes ephemeral signals on a pub/sub channel. The docs and the godoc say so, and the default names do not overlap.

**`notify/nats`**, package `nats`:
```go
func NewBroadcaster(conn *natsgo.Conn, opts ...Option) (*Broadcaster, error)
// Options and defaults:
//   WithSubject(string)                        default DefaultSubject = "notify.signals"; empty, wildcard ('*', '>')
//                                              or empty-token subjects -> ConfigurationError (same rule as delivery/nats)
//   WithDecodeErrorHandler(func(ctx, error))   default no-op, documented as silent
```
- **Broadcast:** `conn.Publish` per message. nats.go buffers during a reconnect, up to its `ReconnectBufSize`. An overflow or a closed connection is returned as a `*PublishError`. There is no `Flush` per call, because the round trip is a cost with no guarantee to buy.
- **Listen:**
  - `conn.Subscribe(subject, handler)`; the handler decodes and calls `deliver`, and runs on the connection's dispatcher goroutine, which nats.go owns;
  - block until ctx is done, then `Unsubscribe` and return nil;
  - nats.go resubscribes after a reconnect;
  - slow-consumer drops are reported by the connection's async error handler, which the host configures on `*natsgo.Conn`. The docs say so.
- **No queue group, deliberately.** A queue group delivers each message to only one instance, which defeats fanning out to every instance.
- **Rejected:** one subject per recipient (`notify.signals.<recipient>`). Recipient identifiers can contain `.`, `*` or spaces, the same problem `delivery/nats` avoids with task types.
- **Not `delivery/nats`:** that sink publishes durable events under `hmntsk.events.<type>`. This broadcaster uses a separate ephemeral subject.

**Errors in both modules:** `ErrConfiguration`/`*ConfigurationError`, `ErrPublish`/`*PublishError`, and, decoding through notify, `notify.ErrUnknownSignalFormat` reported to the decode error handler. The shapes mirror `delivery/*` and are copied, not imported.

### 6. Mounting and proxies

- **stdlib:** `mux.Handle("GET /v1/notifications/socket", h)`.
- **Gin:** `r.GET("/v1/notifications/socket", gin.WrapH(h))`. Gin's writer supports hijacking.
- **Fiber:** not supported. Its `adaptor.HTTPHandler` cannot hand a hijacked net/http connection to the handler, so Fiber hosts use the SSE stream. This limit is stated in the docs, and the constructor cannot detect it.
- **Operations guide**, `notify/docs/realtime-operations.md`:
  - proxy idle timeouts must exceed the 25s heartbeat (nginx `proxy_read_timeout`, AWS ALB idle timeout 60s default);
  - WebSocket upgrades need `Upgrade`/`Connection` forwarding;
  - SSE needs buffering and compression off (`X-Accel-Buffering: no`);
  - prefer HTTP/2 for SSE, because of the HTTP/1.1 limit of six connections per origin;
  - **sticky sessions are not required** once a cross-instance broadcaster is configured; without one, only single-instance deployments work;
  - graceful shutdown: `http.Server.Shutdown` does not close hijacked WebSocket connections, so hosts cancel the server's base context or use `RegisterOnShutdown` (a documented snippet).

### 7. Module layout, split and release

| Module | Imports |
|---|---|
| `notify/websocket` | `notify`, `github.com/coder/websocket` |
| `notify/redis` | `notify`, `github.com/redis/go-redis/v9` (tests: testcontainers redis module) |
| `notify/nats` | `notify`, `github.com/nats-io/nats.go` (tests: testcontainers nats module) |

- All three are in `NOTIFY_MODULES` and `go.work`. `split-check` covers them.
- Each module has its own `testutils.go` with `RunTestRedis`/`RunTestNATS`, copied from `delivery/*` because the split rule forbids importing those, with the same pinned images.
- **Release order:** after `notify` (and after `notify/notifytest` and `notify/sqlstore`, which these modules' integration tests do not need; they use the memory store). They move to the notify repository with the rest of `notify/**` before the first tag.

## Risks / Trade-offs

- [Amendments 1–3 widen notify-core's public API] → They are small, now part of notify-core (its decision 13), and they make one rule (the cap, the codec) true in one place. Leaving them out would duplicate rules across transports.
- [coder/websocket is maintained by a small team] → It has no dependencies and a narrow surface. Swapping libraries is confined to one module, because the protocol and behaviour are fixed by the spec, not the library.
- [Recipient identifiers visible on the broker] → Documented. Hosts that consider identifiers sensitive run the broker on a private network with TLS and auth, configured on their own client.
- [A subscription gap: `Hub.Running()` becomes true when `Run` starts, slightly before the broker confirms the subscription] → Signals in that window are lost, which best effort permits, and clients re-read on connect. Redis `Listen` waits for the confirmation before delivering; NATS `Subscribe` returns once registered locally.
- [Redis pub/sub output buffer limits disconnect a slow subscriber] → The hub's non-blocking deliver keeps the channel drained. The docs note `client-output-buffer-limit pubsub`, and go-redis reconnects.
- [NATS slow-consumer drops are silent unless the host sets an async error handler] → Documented, with an example handler.
- [Fiber hosts get no WebSocket] → Stated limit. SSE covers the same signals.

## Migration Plan

- The change is additive: new modules, no schema, and no change to existing modules. The notify-core APIs it needs (decision 2) ship with notify-core.
- **Enabling it:** replace `notify.WithBroadcaster(notify.NewInProcessBroadcaster())` with the Redis or NATS broadcaster on every instance, and mount `websocket.NewHandler` where wanted.
- **Rollback:** revert to the in-process broadcaster. Signals then reach only same-instance connections, and nothing is lost from the store.

## Implementation notes

Recorded while applying the change; none alters a requirement.

- **coder/websocket** is v1.8.15, the version chosen; `govulncheck` reports nothing.
- **`Listen` returns ctx's error**, not nil, on cancellation, in both broadcasters. That is the `notify.Broadcaster` contract as notify-core implemented it, and what `Hub.Run` passes on.
- **NATS `Listen` uses a channel subscription** (`ChanSubscribe`) read by the `Listen` goroutine, not a callback on the connection's dispatcher. deliver then runs only on that goroutine, so nothing is delivered after `Listen` returns, and no subscription goroutine outlives it. A full channel still counts as a slow consumer, reported to the connection's async error handler.
- **WebSocket error frames use the HTTP contract's codes as implemented**: `validation_failed`, `not_found`, `internal`. A not-found reply carries the fixed message `notify: not found`, so it cannot distinguish identifiers.
- **Origin checking happens before `websocket.Accept`**, in the handler, so that a refusal carries the notify error body; the library's own check is switched off. A pattern matches the origin's host with `path.Match`.
- **A failed write or ping closes the connection immediately** (`CloseNow`), rather than attempting a 1011 close frame the stalled client would not accept. Shutdown still closes with 1001.
- **The connection's context is detached from the request's**, so that a server shutdown can still write the 1001 close frame; a read on a cancelled context would otherwise close the connection first.
- **The Gin example is documented, not compiled**: adding Gin as a dependency of `notify/websocket` for one example is not worth it. The stdlib example is an example test.
- **The NATS reconnect test drops the connection through an in-test TCP proxy** instead of restarting the container, whose mapped port changes on restart. The Redis test kills the pub/sub connection with `CLIENT KILL TYPE pubsub`.
- **The NATS outage test disables the reconnect buffer** (`ReconnectBufSize(-1)`), so a publish while the server is away fails and reaches the signal error handler; with the default buffer it would wait in memory, which is also documented.
- **The documentation tests live in each adapter module** and read `notify/docs/realtime-operations.md`, because `notify` cannot import its adapters.

## Open Questions

None that change the specs or tasks. A sharded Redis pub/sub option is deferred until a host needs it.
