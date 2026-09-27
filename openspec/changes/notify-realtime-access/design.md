## Context

See proposal.md for why. The evidence is the audit's section G. Every item marked CONFIRMED has a failing reproduction test in the scratch module `$V/notify`, where `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`. Run a test with `cd $V/notify && go test -count=1 -run '<Name>' .`.

The current state, checked in the code:

- **N1: follow grants write.**
  - `notify/websocket/handler.go` `ServeHTTP` authorizes the connection with `SubscriptionAuthorizer(actor, recipient)`.
  - `serve` then passes only `recipient` to `read` and `answer`.
  - `answer` calls `svc.MarkRead(ctx, recipient, ...)` and `svc.MarkAllRead(ctx, recipient, ...)`, so the actor is lost, and a follower marks the followed user's notifications.
  - The main spec says "apply each request to the connection's recipient only", which describes the defect as intended behaviour.
  - The package doc says "a client can mark *its* notifications read".
  - The HTTP endpoints always act on the actor, so they are correct.
  - Reproduction: `websocket_test.go` `TestWebSocketFollowerCannotMarkRecipientsNotifications`.
- **N2: a closed NATS connection is never noticed.**
  - `notify/nats/broadcaster.go` `Listen` selects only on `ctx.Done()` and the `ChanSubscribe` channel.
  - nats.go never closes that channel when the connection closes, so `Listen`, and therefore `Hub.Run`, never returns, and `Running()` stays true.
  - nats.go v1.53.1 offers `Conn.StatusChanged(statuses...) chan Status` and `RemoveStatusListener`. These listen without replacing the host's own `ClosedHandler`.
  - Reproduction: `nats_test.go` `TestNATSListenEndsWhenConnectionCloses`. It has two cases: the host closes the connection, and reconnect attempts run out.
- **N3: open streams outlive the run.**
  - `Hub.endRun` resets the run state but leaves `subscriptions` untouched.
  - `Subscription` has no end signal, so the SSE loop in `notify/http.go` `stream` and the WebSocket `serve` loop keep heartbeating or answering.
  - Reproduction: `hub_test.go` `TestHubStopClosesOpenStreams`.
- **N6: followers use up the recipient's cap.**
  - `Hub.Subscribe(recipient)` counts every subscription keyed by recipient against `maxStreams`, whoever holds it.
  - Its only callers are `notify/http.go` `stream` and `notify/websocket/handler.go` `ServeHTTP`, both found with gopls references. Both know the actor.
  - Reproduction: `hub_test.go` `TestFollowersCannotExhaustRecipientsOwnStreamCap`.
- **N7: filter lists are unbounded.**
  - `ListQuery.Validate` (`notify/store.go`) checks recipient, limit, each state's validity and the cursor.
  - It does not check how many kinds or states there are, nor each kind's length.
  - SQL stores expand filters into bound parameters, so 70,000 kinds fail as a driver error: "too many SQL variables" on SQLite, and PostgreSQL's 65,535-parameter ceiling. That becomes a `500`.
  - Reproduction: `sqlstore_test.go` `TestListFilterCountIsValidated`.
  - **The HTTP half is refuted.** Go's `net/url` stops parsing at 10,000 parameters, so the HTTP route cannot deliver 70,000 kinds. It is not a separate defect. It is the cause of N7′.
- **N7′: filters are dropped.**
  - `notify/http.go` `list` uses `r.URL.Query()`, which discards the parse error and returns what it parsed so far, possibly nothing.
  - An over-long `kind=` query therefore lists unfiltered with `200`.
  - `stream`, and WebSocket `ServeHTTP`, read `recipient` the same way. When the recipient is dropped they fall back to the actor.
  - Reproduction: `sqlstore_test.go` `TestListHTTPDropsFiltersPastQueryParamLimit`.
- **N9 is an observation, and this change fixes it.**
  - `notify.SelfOnly` and `notify.AllowAll` (`notify/authorize.go`) are exported mutable `var`s.
  - `NewHandler` and `websocket.NewHandler` read `SelfOnly` as their default, so any package in the process can change every default-wired handler by reassigning it.
  - Assigning nil slips past the constructor's nil check, which covers only an explicit option, and fails at the first stream request.
  - Reproduction: `globals/globals_test.go` `TestNilDefaultPolicyIsRefusedAtConstruction`. It fails today, although the audit files it as an observation.
- **Tags:** nothing is tagged (`git tag` is empty), so breaking exported signatures is free under `docs/releasing.md`.

## Goals / Non-Goals

**Goals:**
- Permission to follow a recipient never implies permission to change their notifications.
- An instance that can no longer receive signals says so: `Run` returns, `Running()` is false, and every open stream is closed so clients reconnect elsewhere.
- No policy-permitted follower can deny a recipient their own stream.
- A listing either applies every filter the client sent or is refused with `400`. It never becomes an unfiltered `200` or a database `500`.
- A library default cannot be changed process-wide by assignment.

**Non-Goals:**
- **Redis connection-closed detection.** N2 was reproduced for NATS only. Whether `notify/redis` `Listen` returns when its client is closed is not verified here. That needs its own reproduction before it can be claimed (see Open Questions).
- **Bounding `CloseRequest.Kinds` and WebSocket `mark-read` identifier counts.** They are not reproduced as defects. WebSocket messages are already bounded by the read limit.
- **`tasknotify.DefaultRules`.** It is the same mutable-`var` shape as N9, but it lives in another module with a different capability (`task-notifications`). It is recorded for a follow-up change.
- **Per-actor global stream caps** across all recipients.

## Decisions

### 1. A separate mark policy, self-only by default (N1)

```go
// notify/authorize.go
type MarkAuthorizer interface {
    // AuthorizeMark returns nil to let actor mark recipient's notifications read.
    AuthorizeMark(ctx context.Context, actor, recipient string) error
}
type MarkAuthorizerFunc func(ctx context.Context, actor, recipient string) error
```

- The WebSocket handler keeps `actor` alongside `recipient` through `serve`, `read` and `answer`.
- Before calling `MarkRead` or `MarkAllRead`, `answer` calls `AuthorizeMark(ctx, actor, recipient)`.
- A refusal is answered with an error frame, code `forbidden`, carrying the policy's message and echoing `ref`. The connection stays open, and nothing is written.
- `errorReply` gains the `ErrUnauthorized` → `forbidden` mapping. The code is the one the HTTP contract uses for `403`.
- The mark still targets the connection's recipient when permitted, so a delegate policy works.

**Default:** self-only, meaning `actor == recipient` and a non-empty actor. It is the value `notify.SelfOnly()` returns (decision 5).

**Override:** `websocket.WithMarkAuthorizer(p)`, or `notify.AllowAll()` to permit every mark. `WithMarkAuthorizer(nil)` is a `ConfigurationError` from `NewHandler`, as `WithSubscriptionAuthorizer(nil)` is.

**Alternatives considered:**
- *Apply marks to the actor's own notifications on a follow connection.* It is safe, but the connection shows bob's signals while its marks silently act on alice's inbox. A `mark-all-read` on a follow connection would clear the wrong inbox without any error. Rejected: it reinterprets the request rather than refusing it.
- *Reuse `SubscriptionAuthorizer` for marks.* That is the defect. Follow and write are different permissions (library-design rule 5: the host owns the meaning).
- *One `AccessPolicy` with an operation argument.* It is more general, but a host has to switch on an enum to express "follow yes, write no". Two single-method ports are clearer and can be implemented by one type.

### 2. NATS `Listen` ends when its connection closes for good (N2)

- Before `ChanSubscribe`, `Listen` registers `status := b.conn.StatusChanged(natsgo.CLOSED)` and defers `b.conn.RemoveStatusListener(status)`.
- It then checks `b.conn.IsClosed()`. The status listener only reports transitions after registration, so this check closes the race.
- The receive loop selects on `ctx.Done()`, `messages` and `status`.
- On CLOSED, it drains nothing further and returns `fmt.Errorf("nats: listen on subject %q: %w", subject, natsgo.ErrConnectionClosed)`.
- A connection that is RECONNECTING is not CLOSED, so the existing reconnect behaviour and its test are unchanged.
- `Hub.Run` already defers `endRun`, so `Running()` becomes false as soon as `Listen` returns. Decision 3 then closes the streams.

**Default:** on. Every NATS broadcaster ends receiving when its connection closes.

**Override:** none, deliberately. A closed nats.go connection never reopens, so continuing would be the "open and silent" state the spec forbids. The host still controls the connection's lifetime, through `MaxReconnects`, `ReconnectWait`, its own `ClosedHandler` (not replaced, because `StatusChanged` is additive) and whether to rebuild the connection and call `Run` again.

**Alternatives considered:**
- *`conn.SetClosedHandler`.* Rejected, because it replaces the host's handler.
- *Polling `IsClosed` on a ticker.* Rejected: it adds latency and a timer to every listener.

### 3. Ending a run closes every subscription (N3)

- `Subscription` gains `done chan struct{}`, exposed as `Done() <-chan struct{}`. It is closed exactly once, by `Close` or by the hub.
- `Hub.endRun` takes `h.mu` and ends every open subscription. It closes each one's `done` and clears the map, after `running` is false, so no new subscription can be added to a stopped hub.
- A later `Close` from the transport is a harmless no-op.
- **SSE:** `stream` adds `case <-subscription.Done(): return`. Returning ends the response, the client's `EventSource` sees EOF and reconnects, and per spec it re-reads.
- **WebSocket:** `serve` adds a case that sends close status **1013 (try again later)**, with reason "the hub stopped receiving signals", then returns. It uses 1013 rather than 1001 (going away), so a client can tell "this instance's receiver stopped" from "server shutting down". Both mean reconnect.

**Default:** streams close when the run ends.

**Override:** none. The spec forbids a stream left silent while signals are not received. A host that wants streams to survive keeps `Run` running, or restarts it; `Run` may be called again.

**Alternative considered:** *Keeping streams open and resuming them on the next run.* Rejected: signals in the gap are lost with no event telling the client to re-read.

### 4. Two stream budgets per recipient (N6)

- **BREAKING:** `Hub.Subscribe(recipient)` becomes `Hub.Subscribe(actor, recipient string)`.
- The hub keeps the single map of subscriptions per recipient. Each subscription records whether it is a follower subscription (`actor != recipient`).
- `Subscribe` counts own and follower subscriptions separately against `maxStreams` and `maxFollowerStreams`.

| Budget | Default | Override | Validation |
| --- | --- | --- | --- |
| Own streams (actor == recipient) | `DefaultMaxStreamsPerRecipient = 8` (unchanged) | `WithMaxStreamsPerRecipient(n)` | `n < 1` → `ConfigurationError` |
| Follower streams (actor != recipient) | `DefaultMaxFollowerStreamsPerRecipient = 8` | `WithMaxFollowerStreamsPerRecipient(n)` | `n < 1` → `ConfigurationError` |

- Either refusal matches `ErrTooManyStreams` (`429`), and its message says which budget is full.
- `Hub.Subscribe` does not authorize: an empty actor is the transport's job, and both transports refuse it before subscribing.
- A follower budget below one is refused rather than meaning "no following". Disabling following is the subscription policy's job, and a zero here would make a permitted follow always answer `429`, which is a contradictory configuration (library-design rule 6).

**Alternatives considered:**
- *Key the cap by holder (actor) instead of recipient.* A supervisor following ten reports would hit 8 total, and one recipient could still be followed without bound.
- *Reserve N of the single cap for the recipient.* Its semantics are harder to state, and it needs two numbers anyway.
- *Add a `SubscribeAs` method and keep `Subscribe`.* It leaves a method that silently counts everything as "own". Untagged, so the signature changes instead.

### 5. Named policies are functions returning immutable values (N9, fixed)

- **BREAKING:** `var SelfOnly` and `var AllowAll` become `func SelfOnly() SelfOnlyPolicy` and `func AllowAll() AllowAllPolicy`.
- The returned types are empty structs, exported so their godoc is visible.
- Each type implements both `SubscriptionAuthorizer` and `MarkAuthorizer`, with the same rule for both.
- `NewHandler` and `websocket.NewHandler` default to `SelfOnlyPolicy{}` directly, not to package state.
- Refusals still match `ErrUnauthorized`, and messages are unchanged.
- **Call sites to migrate:**
  - `notify/http.go`, and the error detail that says "pass notify.AllowAll"
  - `notify/websocket/handler.go`
  - `notify/docs/realtime-operations.md`
  - `examples/notifications/main.go:782`
  - tests: `authorize_test.go`, `http_test.go`, and in `notify/websocket` `handler_test.go` and `refusal_test.go`

**Default:** self-only, for both follow and mark.

**Override:** unchanged in kind. A host passes its own policy, or `AllowAll()`, per handler.

**Stated scope:** `tasknotify.DefaultRules` has the same shape and is left for a follow-up change (Non-Goals).

**Alternative considered:** *Keep the `var`s and only stop constructors reading them.* That removes the nil hazard, but a host reading `notify.SelfOnly` still sees whatever some package assigned. Rejected, because the name would still lie.

### 6. Bounded list filters (N7)

- New constant `MaxListFilterValues = 100`.
- `ListQuery.Validate` adds these issues:
  - `/kinds` when `len(Kinds) > MaxListFilterValues`;
  - `/states` when `len(States) > MaxListFilterValues`;
  - `/kinds/<i>` for an empty kind, or one longer than `MaxKindBytes`, as `CloseRequest.Validate` already does.
- These come back as a `ValidationError`, which maps to `400`. `Service.List` already validates before touching the store.
- **Shared store suite:** `notifytest.Run` gains cases through `notify.New(store).List`:
  - 101 kinds and 101 states are each a `ValidationError`;
  - exactly 100 kinds succeeds and filters correctly.

  That holds every store to the limit, including a consumer's store and every SQL dialect.

**Default:** 100, which comfortably fits SQLite's historical 999 bound-parameter limit alongside the other filters.

**Override:** none. It is a stated limit (library-design rule 4), because a larger value would let a valid query fail on some dialects and not others. A host needing more kinds issues several queries, or groups kinds under a subject.

**Alternative considered:** *Chunking or array binding (`= ANY($1)`) in the SQL stores.* It is dialect-specific, doesn't help custom stores, and keeps pagination cursors unbounded. Rejected.

### 7. Parse the query strictly (N7′)

- A small helper in `notify`, `ParseQuery(r *http.Request) (url.Values, error)`, calls `url.ParseQuery(r.URL.RawQuery)`.
- On error it returns a `ValidationError{Subject: "request", Issues: [{Detail: "the query string could not be parsed: ..."}]}`.
- It is exported so the separate `notify/websocket` module can share it without duplicating the message.
- `list`, `stream` and WebSocket `ServeHTTP` use it before reading any parameter. A parse error is answered `400` through `WriteError`.
- The parse error covers:
  - Go's `urlmaxqueryparams` limit (10,000 by default);
  - malformed escapes;
  - semicolons, which Go already rejects.

**Default:** strict.

**Override:** none for the parse itself. The query-parameter limit is Go's own and is adjustable by the host with `GODEBUG=urlmaxqueryparams=N`, which godoc mentions. Tolerating a partial parse is exactly the silent degradation rule 4 forbids.

**Compatibility note:** a request that used to pass with a malformed escape, where the bad pair was dropped, is now `400`. Nothing is tagged, and this is recorded here as the decision.

## Risks / Trade-offs

- **[Supervisor UIs that marked reports' notifications over a follow socket break]** → They now get a `forbidden` reply. The fix is one option, `WithMarkAuthorizer`, and the docs show it.
- **[Closing streams on every `Run` end causes a reconnect storm when a host restarts `Run` quickly]** → Clients already reconnect after deploys. The 1013 status and SSE EOF are standard triggers, and browsers back off. It is documented in `realtime-operations.md`.
- **[`StatusChanged` channel semantics across nats.go versions]** → The adapter's `go.mod` pins nats.go. The ported reproduction runs against a real server via `RunTestNATS`, so an upgrade that changes behaviour fails the test.
- **[Two budgets double the worst-case streams per recipient, 8 + 8]** → Both are configurable, and the per-instance resource bound is still linear in the number of recipients.
- **[Breaking signatures (`Subscribe`, `SelfOnly`, `AllowAll`)]** → Untagged. The only callers are in-repo (gopls references), and they migrate in this change.

## Migration Plan

Nothing is tagged, so there is no deprecation period.

**Host migration:**
- `notify.SelfOnly` becomes `notify.SelfOnly()`, and `notify.AllowAll` becomes `notify.AllowAll()`.
- A custom transport calling `hub.Subscribe(r)` passes the actor: `hub.Subscribe(actor, r)`.
- A host that relied on follow-implies-mark adds `websocket.WithMarkAuthorizer(...)`.

Rollback is reverting the change.

## Open Questions

- Does `notify/redis` `Listen` return when its go-redis client is closed? It is not reproduced here. If a scratch reproduction fails, the fix follows decision 2's shape in a follow-up, and the spec text in this change already covers it ("a broadcaster whose connection has closed for good").

## Assumptions (recorded instead of asking)

- The WebSocket close status for decision 3 is 1013. The reproduction only asserts a close error, so any close status satisfies it.
- `MaxListFilterValues` applies to raw counts, duplicates included. It does not deduplicate, because deduplicating would change the cursor fingerprint of existing cursors.
- The mark policy is also consulted when `recipient == actor`. The default permits it. A host policy could, for example, refuse marks for read-only sessions.
