# realtime-scaling

Notification change signals across several application instances, and over
WebSocket.

A signal is produced on the instance that stored a change. The person it is for
may have their stream open on any instance, and the broadcaster carries the
signal between them.

## What it shows

- **The default, `notify.NewInProcessBroadcaster`:**
  - two instances, A and B, share one notification store;
  - a notification published through A signals a stream on A, but never one on
    B;
  - the notification is stored, and B can still read it;
  - this is the documented single-instance limit.
- **Override: `notify/redis`.**
  - `redis.NewBroadcaster(client)` is set with `notify.WithBroadcaster` on
    every instance.
  - A notification published through A signals bob's stream on B.
  - The signal carries only the kind of change and when it happened, never a
    title, link or payload. The client re-reads over HTTP.
- **Override: `notify/nats`.** The same, over a NATS subject.
- **The WebSocket endpoint, `notify/websocket`:**
  - by default it accepts only the request's own host as a browser origin, so a
    socket opened from a foreign page is refused with 403;
  - configured with `WithOriginPatterns`, it accepts the host's own app origin
    and speaks the `notify.v1` subprotocol;
  - it delivers change signals, and takes `mark-read` requests over the same
    connection.

## Context

- **The domain:** the shared invoice-approval domain. alice and bob approve
  invoices, carol is a finance manager, dave audits.
- **No task engine:** notifications are published directly through `notify`,
  so it's clear none of this is task-specific.
- **One store for both instances:** a single in-memory store stands in for the
  database that every instance of a real application shares. What instances
  don't share by default is the broadcaster.
- **Readiness probes:** before relying on a broker, the scenario sends a probe
  signal until it arrives on the other instance. A hub reports running before
  its broadcaster has subscribed, so an early signal could otherwise be lost and
  the output would vary between runs.

## What it leaves out

- **Single-instance streams:** the server-sent event stream, subscription
  policies, retention and email are in `notifications`.
- **Notifications without tasks:** idempotency, coalescing and closing are in
  `notify-standalone`.
- **A browser page:** that is `inbox-ui`.
- **Gin and Fiber:** they are in `http-frameworks`. WebSocket is not available
  on Fiber.

## Services needed

Redis and NATS, both real servers:

```sh
docker run --rm -p 6379:6379 redis:8.2.9-alpine
docker run --rm -p 4222:4222 nats:2.12.7-alpine
```

| Setting | Example |
| --- | --- |
| `HMNTSK_REDIS_ADDR` | `127.0.0.1:6379` |
| `HMNTSK_NATS_URL` | `nats://127.0.0.1:4222` |

Without them, the program exits and tells you which setting is missing and how
to start the service.

## Run it

From the `examples/` directory:

```sh
HMNTSK_REDIS_ADDR=127.0.0.1:6379 HMNTSK_NATS_URL=nats://127.0.0.1:4222 go run ./realtime-scaling
go test ./realtime-scaling
```

The test needs Docker and nothing else: it starts both brokers in containers
with notify's own test helpers.

## Read next

- [Running realtime notifications](../../notify/docs/realtime-operations.md)
  covers choosing a broadcaster, WebSocket and proxies.
- [Notifications](../../notify/docs/notifications.md)
