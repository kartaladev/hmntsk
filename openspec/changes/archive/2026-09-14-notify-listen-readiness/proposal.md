## Why

`notify.Hub.Run` reports the hub as running before its broadcaster has subscribed, so a stream accepted in that window can miss a signal broadcast right after start. The NATS broadcaster also treats a subscription registered on the client as live, before the server knows about it. The usage examples (PR #14) had to probe instances before every scenario to work around this. "The hub is running" should mean "signals broadcast now will arrive".

## What Changes

- **BREAKING** `notify.Broadcaster.Listen` gains a `ready func()` parameter. A broadcaster calls it once, after its subscription is confirmed, and before it returns. A nil `ready` is a configuration error, as a nil `deliver` already is.
- `notify.Hub.Running` reports true only after the broadcaster has called `ready`. Until then `Hub.Subscribe` keeps refusing streams as unavailable, so the SSE and WebSocket transports answer `503` instead of opening a stream that could miss signals.
- New `notify.Hub.Ready() <-chan struct{}`, closed once the hub's run has subscribed. Hosts and tests can wait on it instead of polling.
- `InProcessBroadcaster` calls `ready` once its listener is registered.
- `notify/redis` calls `ready` after the broker confirms the subscription. The adapter already waited for that confirmation; it now reports it.
- `notify/nats` confirms the subscription with a server round trip before calling `ready`. The round trip is bounded by a new `DefaultSubscribeTimeout`, which `WithSubscribeTimeout` replaces. A subscription the server does not confirm in time is returned as an error, as the Redis adapter already does.
- `notify/notifytest` gains a broadcaster conformance suite. It runs against all three broadcasters, so every implementation is held to the ready contract.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `notification-realtime`: signals count as being received only once the broadcaster has confirmed its subscription. A signal broadcast after the hub reports ready reaches open streams.

## Impact

- **Code:** `notify` (`realtime.go`, `hub.go`, the generated broadcaster mock, and the test broadcasters in `service_test.go` and `service_ops_test.go`), `notify/redis`, `notify/nats`, `notify/notifytest`, and notify's docs and godoc.
- **API:** a breaking change to an exported interface that has not been tagged yet. Third-party broadcasters must add the parameter and call it. One that never calls it leaves the hub unavailable, which fails safe and shows up at once as `503`.
- **Split rule:** unchanged. `notify/**` still imports no `hmntsk` package.
- **PR #14:** `examples/realtime-scaling` can drop its readiness probes and wait on `Hub.Ready()`.
