## Why

`notify-core` ships SSE and an in-process `Broadcaster`. That works for a single instance, with a stated limit. Real deployments run several instances behind a load balancer, where a user's connection is seldom on the instance that wrote the notification. Some clients also want a bidirectional channel. Both need dependencies the core must not carry.

## What Changes

- **`notify/websocket`:** a WebSocket handler with the same authorization, signals and heartbeat as the SSE stream.
  - Clients can acknowledge (mark read) over the connection.
  - The WebSocket library is its own module's dependency, and is chosen in design.
- **`notify/redis`:** a `Broadcaster` over Redis pub/sub.
- **`notify/nats`:** a `Broadcaster` over NATS core subjects.
- **Both broadcasters:**
  - carry only the unread-changed signal, never notification contents;
  - are best effort, because the store remains the source of truth;
  - document their channel or subject naming as constants the host can override.
- **Wiring safety:** a nil client, or an empty channel or subject, is a configuration error at construction.
- **These broadcasters are not the delivery sinks.** They are separate from `delivery/redis` and `delivery/nats`, which relay durable events, and the docs say so.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `notification-realtime`: adds a WebSocket transport, and cross-instance broadcasting over Redis and NATS as overrides of the in-process default.

## Impact

- **Modules:** new modules `notify/websocket`, `notify/redis` and `notify/nats`, all in `NOTIFY_MODULES`, each importing only `notify` and its client library.
- **Depends on:** `notify-core`.
- **Tests:** multi-instance fan-out against real Redis and NATS with testcontainers; slow-client drop and heartbeat behaviour; `goleak`.
- **Docs:** an operations section under `notify/docs/` covering proxy idle timeouts, buffering and sticky sessions.
