# Running realtime notifications

`notify` tells a client that its notifications changed, as a signal: the kind of
change and when it happened, never the notification itself. The client then
re-reads its unread count or list from the store, which is the source of truth.
This guide covers the choices a deployment makes: which broadcaster carries
signals between instances, which transport reaches the browser, and what the
infrastructure in between has to allow.

## Choosing a broadcaster

A signal is produced on the instance that stored the change. A client may be
connected to any instance. The broadcaster is what carries the signal from one
to the other.

| Deployment | Broadcaster | Wiring |
| --- | --- | --- |
| One instance | `notify.NewInProcessBroadcaster()` (the default) | nothing |
| Several instances, Redis available | `notify/redis` | `redis.NewBroadcaster(client)` |
| Several instances, NATS available | `notify/nats` | `nats.NewBroadcaster(conn)` |

**Stated limit:** with the in-process default, a signal reaches only connections
on the instance that produced it. A deployment of more than one instance needs a
cross-instance broadcaster, on every instance.

Redis:

```go
client := goredis.NewClient(&goredis.Options{Addr: "redis:6379"})

broadcaster, err := redis.NewBroadcaster(client,
    redis.WithDecodeErrorHandler(logError),
)
svc, err := notify.New(store,
    notify.WithBroadcaster(broadcaster),
    notify.WithSignalErrorHandler(logError),
)
hub, err := notify.NewHub(svc.Broadcaster())
go hub.Run(ctx)
```

NATS:

```go
conn, err := natsgo.Connect("nats://nats:4222",
    natsgo.ErrorHandler(logSlowConsumer), // slow-consumer drops are reported here
)

broadcaster, err := nats.NewBroadcaster(conn, nats.WithDecodeErrorHandler(logError))
```

| Setting | Default | Override |
| --- | --- | --- |
| Redis channel | `notify.signals` | `redis.WithChannel` |
| Redis publish timeout | 5s | `redis.WithPublishTimeout` |
| NATS subject | `notify.signals` | `nats.WithSubject` (no wildcards, whitespace or empty tokens) |
| NATS subscribe timeout | 5s | `nats.WithSubscribeTimeout` |
| Signals per message | 500 | none: a larger broadcast is split into several messages |

Instances see each other's signals only when they share a channel or subject.
Two applications on one broker use different ones.

The NATS subscription uses no queue group, on purpose: a queue group hands each
message to one instance, and every instance has to receive every signal.

Before the hub reports ready, the NATS broadcaster confirms its subscription
with a round trip to the server, within the subscribe timeout. A server that
does not answer in time, such as one unreachable when the hub starts, ends
`hub.Run` with an error rather than leaving the hub refusing streams with no
explanation; the host decides when to run it again.

## Best effort, and what that means

- A broker that cannot be reached never fails the notification write. The
  notification is stored, and the broadcast error goes to the service's signal
  error handler (`notify.WithSignalErrorHandler`), which is silent by default.
- Signals broadcast while an instance is disconnected are not replayed to it.
  Both clients reconnect and resubscribe on their own, without the host
  restarting anything.
- A message in a format version this library does not know, or one that cannot
  be decoded, delivers nothing and goes to the broadcaster's decode error
  handler. Receiving continues.
- A client that reconnects, or that missed a signal for any reason, loses
  nothing: it re-reads the store.

A message carries recipients, changes and times, and nothing else: never a
notification's title, links, data, kind or subject. **Recipient identifiers do
travel on the broker.** Where they are sensitive, run the broker on a private
network with TLS and authentication, configured on the client the host passes
in.

These broadcasters are not the task engine's `delivery/redis` and
`delivery/nats` sinks. Those deliver durable events to a stream or subject and
retry them; these carry ephemeral signals on a channel or subject of their own,
and the default names do not overlap.

## Choosing a transport

| Transport | Endpoint | Frameworks |
| --- | --- | --- |
| Server-sent events | `GET /v1/notifications/stream` (`notify.NewHandler`) | net/http, Gin, Fiber |
| WebSocket | `GET /v1/notifications/socket` (`websocket.NewHandler`) | net/http, Gin |

Both authorize the subscription with the same policy (`notify.SelfOnly` by
default), refuse while the hub is not running, and count against the same
per-recipient cap of 8 connections per instance. A WebSocket client can also
mark notifications read over its connection:

| Direction | Message |
| --- | --- |
| server to client | `{"type":"unread-changed","change":"created","at":"2026-09-14T10:00:00.000000Z"}` |
| client to server | `{"type":"mark-read","ref":"r1","ids":["..."]}` |
| client to server | `{"type":"mark-all-read","ref":"r2","through":"..."}` (`through` optional) |
| server to client | `{"type":"marked","ref":"r1","marked":1}` |
| server to client | `{"type":"error","ref":"r1","code":"not_found","message":"..."}` |

WebSocket defaults:

| Setting | Default | Override |
| --- | --- | --- |
| Browser origins | the request's own host only | `websocket.WithOriginPatterns`, or `websocket.WithAnyOrigin` |
| Largest client message | 4096 bytes (the connection is closed with 1009 beyond it) | `websocket.WithReadLimit` |
| Ping interval | the hub's heartbeat, 25s | `websocket.WithPingInterval` |
| Write timeout | the hub's write timeout, 10s | `websocket.WithWriteTimeout` |
| Subprotocol offered | `notify.v1` | none |

Mounting:

```go
mux.Handle(handler.Pattern(), handler)                          // net/http
router.GET("/v1/notifications/socket", gin.WrapH(handler))      // Gin
```

**Stated limit: WebSocket is not available on Fiber.** Fiber's adaptor cannot
hand over the hijacked connection a WebSocket needs. Fiber hosts use the
server-sent event stream, which carries the same signals.

## Proxies and load balancers

- **Idle timeouts** must be longer than the 25s heartbeat, or the proxy closes
  quiet connections: nginx `proxy_read_timeout`, the AWS ALB idle timeout (60s
  by default), and their equivalents.
- **WebSocket upgrades** need the `Upgrade` and `Connection` headers forwarded
  (nginx: `proxy_http_version 1.1`, `proxy_set_header Upgrade $http_upgrade`,
  `proxy_set_header Connection "upgrade"`).
- **Server-sent events** need buffering and compression off. The stream sends
  `X-Accel-Buffering: no` for nginx; other proxies need it configured.
- **HTTP/2** is preferable for server-sent events: browsers allow only six
  HTTP/1.1 connections per origin, and a stream holds one open.
- **Sticky sessions are not required** once a cross-instance broadcaster is
  configured. Without one, only a single-instance deployment receives every
  signal.
- **Redis** disconnects a pub/sub subscriber that falls behind its
  `client-output-buffer-limit pubsub`. The hub delivers without waiting on
  clients, so a listener keeps up; if the limit is still reached, the client
  reconnects.

## Shutting down

`http.Server.Shutdown` does not close hijacked connections, and every WebSocket
is one. Cancel the server's base context as well, which closes each WebSocket
with status 1001 (going away) and releases its slot:

```go
baseCtx, cancelBase := context.WithCancel(context.Background())
server := &http.Server{
    Handler:     mux,
    BaseContext: func(net.Listener) context.Context { return baseCtx },
}
server.RegisterOnShutdown(cancelBase)
```

A client that was closed reconnects, to this instance or another, and re-reads
the store.
