## Context

See proposal.md for why. The current state, checked in the code:

- **`Hub.Run`** (`notify/hub.go:122-130`) does `running.CompareAndSwap(false, true)`, defers `running.Store(false)`, then calls `broadcaster.Listen(ctx, h.deliver)`. `Running()` is true for the whole call, including the time before `Listen` has subscribed. `Hub.Subscribe` accepts streams on `Running()`.
- **The gap exists for all three broadcasters,** because it opens before `Listen` is called:
  - in-process: the listener is registered inside `Listen`, so the gap is tiny;
  - `notify/redis`: `Subscribe` followed by `Receive` already waits for the broker's confirmation, but only after `Running()` became true;
  - `notify/nats`: `ChanSubscribe` returns once the subscription is registered on the client. The server learns about it asynchronously.
- **nats.go `FlushWithContext`** sends a PING and waits for the PONG, which proves the server has processed every earlier protocol line, the SUB included. It returns `ErrNoDeadlineContext` when the context has no deadline, and the hub's context normally has none.
- **go-redis `Client.Subscribe(ctx, ch)`** sends SUBSCRIBE and ignores the write error. The first `PubSub.Receive` returns the `*Subscription` confirmation or the error.
- **The archived notify-realtime-adapters design** accepted the gap in its risks section as "best effort permits". PR #14 showed the cost: every multi-instance example and test has to probe or poll first.
- **Implementations to update:** `InProcessBroadcaster`, `notify/redis`, `notify/nats`, the generated `MockBroadcaster` (`realtime_mock_test.go`), and hand-written test broadcasters at `notify/service_test.go:16` and `notify/service_ops_test.go:337`.
- **`notify/notifytest`** has suites for stores and email, but none for broadcasters.

## Goals / Non-Goals

**Goals:**
- `Hub.Running()` true means a signal broadcast from now on reaches this instance's streams, within the broadcaster's best effort.
- Every broadcaster, including a consumer's own, is held to that contract by one conformance suite.
- Hosts and tests wait for readiness with a channel instead of polling.

**Non-Goals:**
- **Tracking readiness across broker reconnects.** After a connection drops, both client libraries resubscribe on their own, and `Running()` stays true in the meantime. Signals in that gap are lost, which the best-effort contract allows, and clients re-read on reconnect. This is stated in godoc.
- **Replaying signals broadcast before readiness.**
- **Changing `Broadcast`.**

## Decisions

### 1. Readiness is a `ready func()` parameter on `Listen`

```go
type Broadcaster interface {
    Broadcast(ctx context.Context, signals []Signal) error
    // Listen subscribes, calls ready once the subscription is confirmed, then calls
    // deliver with every signal until ctx is done, and returns ctx's error.
    Listen(ctx context.Context, deliver func(Signal), ready func()) error
}
```

**The contract for implementations:**
- call `ready` at most once;
- call it from within `Listen`, after the subscription is confirmed by whatever the transport offers, and before `Listen` returns;
- never call it after returning an error.

A nil `deliver` or a nil `ready` is an error matching `notify.ErrConfiguration`, returned before subscribing. The in-process broadcaster returns `*notify.ConfigurationError`. The Redis and NATS adapters return their own `*ConfigurationError`, which now unwraps to both the adapter's `ErrConfiguration` and `notify.ErrConfiguration`, so existing `errors.Is`/`errors.As` checks on the adapter types keep matching.

**Default:** the three shipped broadcasters implement it.

**Override:** a consumer's own broadcaster decides what "confirmed" means for its transport. `notifytest.RunBroadcasterSuite` checks the observable part.

**Alternatives considered:**
- *A two-step `Subscribe(ctx) (Listener, error)` followed by `Listener.Receive(ctx, deliver)`.* An implementation couldn't forget readiness, but every implementation would need restructuring, it would need two contexts (subscribe and receive) with confusing ownership, and it would add a second exported type. Rejected as more API for the same guarantee.
- *An optional `ReadyListener` interface, detected by type assertion.* Non-breaking, but a broadcaster without it would silently keep the race. That breaks the rule that a limit is stated rather than quietly relaxed, and the interface is untagged, so breaking it costs nothing. Rejected.
- *`Hub.Run` taking a ready channel.* That moves the signal to the hub's caller, but the hub still couldn't know when the broadcaster had subscribed. Rejected.

### 2. The hub's run states and `Hub.Ready()`

The hub goes through three run states, protected by its mutex:

```
          Run()                  ready()
 idle ----------> starting -----------------> receiving
  ^                  |                            |
  +------------------+----------------------------+
          Listen returns (error or ctx done)
```

- **`Running()`** is true only in `receiving`. `Subscribe` refuses with `ErrUnavailable` in `idle` and `starting`, exactly as it does in `idle` today.
- **A second `Run`** while `starting` or `receiving` is the existing `ConfigurationError`.
- **Each run gets its own ready function,** bound to a token identifying that run. It does nothing unless its run is the current one and the hub isn't already receiving. A repeated call, or one after that run's `Listen` has returned, is therefore ignored, so a misbehaving broadcaster can't mark a finished run, or a later one, as receiving.
- **`Hub.Ready() <-chan struct{}`** returns a channel that closes when the current run, or the next one if none is in progress, reaches `receiving`. The hub creates it in `NewHub`. When a run that reached `receiving` ends, the hub replaces the closed channel with a fresh open one, so `Ready()` called after a run stops waits for the next run. A run that fails before readiness leaves its channel open, so a host already waiting on it is woken by the next run that subscribes instead of waiting forever.
- **Stated limit:** a receive from `Ready()` says the hub reached `receiving` at some point after the call. It does not say the hub is still receiving; `Running()` answers that.
- **Stated limit:** when `Run` fails before readiness, `Ready()` never closes. The godoc shows the host pattern: `select` on `Ready()` and on the `Run` goroutine's error.

**Default:** a hub is unavailable until its broadcaster confirms.

**Override:** none. This is the hub's guarantee, and a host that wants streams accepted earlier would be asking for silently lost signals. A broadcaster that calls `ready` as its first statement opts out, knowingly, for its own transport.

**Alternative considered:** *`Hub.WaitReady(ctx) error`.* A channel composes with `select` and the `Run` error, and a context-taking method can be written on top of it in one line. Rejected as the primitive.

### 3. What each shipped broadcaster waits for

- **In-process:** calls `ready` right after it registers the listener, while holding no lock. `Broadcast` already sees every registered listener.
- **Redis:** after `Subscribe`, `Receive(ctx)` must return a `*goredis.Subscription` for the channel. Anything else, or an error, is returned as `redis: subscribe to channel %q: ...`, as today. It then calls `ready` and starts draining `Channel()`. Only the `ready` call and the type check are new.
- **NATS:** after `ChanSubscribe`, it calls `conn.FlushWithContext` with a context bounded by the subscribe timeout, derived from the listen context so cancellation still wins.
  - **Success:** it calls `ready`.
  - **Failure:** it unsubscribes and returns `nats: confirm subscription to subject %q: %w`. When the listen context itself was cancelled, it returns the context's error instead.
  - **Default:** `DefaultSubscribeTimeout = 5 * time.Second`.
  - **Override:** `WithSubscribeTimeout(d)`. A value that is not positive is a `ConfigurationError` from `NewBroadcaster`.
  - **Why not wait forever:** a hub that never becomes ready and never returns hides a broker outage behind `503`s. Returning the error hands the decision to the host, which already owns restarting `Run`, and matches Redis, where a subscription that can't be made is an error.
  - **Alternative considered:** *call `ready` without flushing*, relying on nats.go resubscribing. Rejected, because that is the bug.

### 4. A broadcaster conformance suite in `notifytest`

`notifytest.RunBroadcasterSuite(t, newPair func(t *testing.T) (publisher, listener notify.Broadcaster))` asserts:
1. a nil `deliver` or a nil `ready` is refused with a `*notify.ConfigurationError`;
2. `ready` is called exactly once before `Listen` returns;
3. a signal broadcast on the publisher from inside `ready`, before it returns, is delivered to the listener with no sleep and no retry. That makes "hold nothing `Broadcast` needs while calling `ready`" part of the contract;
4. `Listen` returns the context's error after cancellation, and delivers nothing afterwards.

For in-process, publisher and listener are the same value. For Redis and NATS they are two broadcasters on one broker, each with its own client, which the existing `testutils.go` helpers provide. Case 3 runs many iterations with fresh listeners, so a race shows up as a failure rather than a rare flake.

**Default:** shipped adapters run it.

**Override:** a consumer runs it against their own broadcaster.

## Risks / Trade-offs

- **[Breaking interface]** → It is untagged, so the break is free under the releasing rules. The proposal marks it BREAKING, and the godoc and docs show the one-line migration.
- **[A third-party broadcaster never calls `ready`]** → The hub stays unavailable and every stream gets `503`, which is loud and safe. The conformance suite catches it in the consumer's own tests.
- **[The NATS flush fails while the server is briefly away at startup]** → `Run` returns an error, and the host's supervisor restarts it. `WithSubscribeTimeout` lengthens the wait for slow networks.
- **[`Ready()` channel from an earlier run]** → Documented. Only a caller that holds on to the channel across a stop can see a stale close, and `Running()` gives the current answer.
- **[Reconnect gap stays]** → Stated as a non-goal and in godoc. Delivery stays best effort by spec.

## Migration Plan

Nothing is tagged, so there is no deprecation period. A broadcaster adds `ready func()` to `Listen` and calls it once subscribed. Hosts change nothing, and tests that polled `Running()` may wait on `Ready()` instead. Rollback is reverting the change.
