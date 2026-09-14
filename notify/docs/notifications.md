# Notifications

`notify` is a durable inbox of notifications, each addressed to one recipient,
that knows nothing about the domain publishing into it. A publisher, such as the
task engine's `tasknotify` adapter, decides who is told what; `notify` stores
it, keeps it in step with its subject, bounds how much is kept, and tells each
recipient's connected clients that something changed.

This guide goes through it in the order a host usually meets it. Each part says
what you get with no configuration and how to change it. The schema of the SQL
store is in [schema.md](schema.md).

Email is optional and documented in [email.md](email.md). A host that emails
applies one more table, `notify_email_deliveries`; one that does not needs none of
it.

## The model

A `Notification` belongs to exactly one recipient and is in one of three states:

| State | Meaning |
| --- | --- |
| `ACTIVE` | the recipient has not read it and nothing has closed it |
| `READ` | the recipient has read it |
| `CLOSED` | its subject moved on; `ClosedReason` says why |

A notification never returns to `ACTIVE`. It is about a `Subject` at a
`SubjectVersion`, has a `Kind`, and carries a `Title`, `Links` by relation name
and a JSON `Data` payload. Kinds, titles, links and data belong to the
publisher: they are stored and returned exactly as published, the payload byte
for byte, and nothing interprets them. `InactiveAt` records when a notification
first left `ACTIVE`, by being read or closed.

## Defaults and overrides

Everything goes through a `Service`, built with `notify.New(store, options...)`:

```go
svc, err := notify.New(notify.NewMemoryStore())
```

| Concern | Default | Override |
| --- | --- | --- |
| Storage | none: a store is required | `NewMemoryStore()` for a single process; `notify/sqlstore` for PostgreSQL, MySQL or SQLite; any `Store` that passes `notify/notifytest` |
| Time | the system clock | `WithClock` |
| Identifiers | UUIDv7, sorting in the order they were minted | `WithIDGenerator` |
| Change signals | `InProcessBroadcaster`, which reaches only its own process | `WithBroadcaster`, such as notify/redis or notify/nats |
| Broadcast failures | ignored, silently | `WithSignalErrorHandler` — supply one that logs |

A nil option is ignored and keeps the default. A nil store is a configuration
error from `New`, before any traffic.

## Publishing and closing

`Publish(ctx, drafts...)` creates notifications. Every draft is validated before
anything is written, so one invalid draft publishes nothing.

- **Idempotent per source.** A notification is identified by its `SourceID`
  together with its recipient. Publishing the same source for the same recipient
  again creates nothing and leaves the existing notification as it is, even if it
  was read. The result counts it as a duplicate. Publishers deliver at least once;
  this is what makes that safe.
- **Coalescing.** A draft with `Coalesce` set creates nothing when its recipient
  already has an `ACTIVE` or `READ` notification of the same kind on the same
  subject. A task adapter uses it so that widening a task's pool does not offer it
  twice to the same person.

`Close(ctx, request)` closes a subject's notifications:

- of some kinds, or of every kind when `Kinds` is empty;
- at or below a `Version`;
- sparing one recipient named by `Except`.

It moves `ACTIVE` and `READ` notifications to `CLOSED` with the given reason, and
reports which recipients it closed. A read notification keeps the time it was
read.

**The watermark.** A close remembers, per subject and kind, the highest version
it closed at. A later publish of that kind below that version is suppressed and
counted, not created. That is what stops a source retried an hour late, after a
newer close, from reopening the subject. Suppression is strictly below the
watermark while a close reaches up to its version, so one event can close a kind
for everyone but one recipient and publish that kind at the same version.
Publishing and closing one subject are serialised, so the guarantee holds even
when they race on different instances.

**Successors.** A close can name a `Successor`: a notification published, in the
same transaction, to every recipient the close moved to `CLOSED`, except those in
`SuccessorSkip`. A task adapter uses it to tell the other candidates "taken" when
someone claims a task. A retried close closes nothing further and creates no
second successor; a successor below a newer watermark is suppressed like any
late publish.

## Retention

Nothing is deleted unless the host runs a `Pruner`:

```go
pruner, err := notify.NewPruner(svc)
go pruner.Run(ctx, time.Hour) // or pruner.Prune(ctx) from a scheduler
```

| Bound | Default | Override |
| --- | --- | --- |
| Notifications per recipient | 500 | `WithMaxPerRecipient`, or `WithoutMaxPerRecipient` |
| Age of inactive notifications | 90 days after they became inactive | `WithMaxAge`, or `WithoutMaxAge` |
| Strategy | `EvictOldestActive` | `WithRetentionStrategy(notify.RetainActive)` |
| Close records of empty subjects | 7 days | `WithWatermarkRetention` |
| Rows per delete | 1000 | `WithPruneBatch` |
| Failed passes in `Run` | ignored, silently | `WithPruneErrorHandler` |

- **Age never deletes an `ACTIVE` notification.** It is measured from when a
  notification became inactive, so one created months ago and read yesterday
  stays.
- **The strategy decides whether the count bound may evict unread ones.**
  Inactive notifications always go first, oldest first.
  - `EvictOldestActive` (the default): when a recipient is still over the bound
    after that, their oldest `ACTIVE` notifications are evicted. Unread
    notifications can be lost. Each pass reports evictions separately and signals
    the recipients affected.
  - `RetainActive`: an `ACTIVE` notification is never deleted, and a recipient
    whose unread notifications alone exceed the bound keeps all of them. Choose it
    when a notification carries an obligation.
- **Bounds are approximate.** They hold as of the end of a pass: between passes a
  recipient may exceed the count and an inactive notification may outlive the age
  bound. Passes on several instances at once are safe.
- A zero or negative bound, a bound both set and removed, both bounds removed, or
  an unknown strategy is a configuration error from `NewPruner`.
- A source redelivered after its subject's close record expired can create
  notifications again. Seven days is far beyond the relay's retry horizon.

## Realtime

A `Hub` routes change signals to connected clients. It receives only while the
host runs it, and accepts streams only once its broadcaster has confirmed its
subscription:

```go
hub, err := notify.NewHub(svc.Broadcaster())

runErr := make(chan error, 1)
go func() { runErr <- hub.Run(ctx) }()

select {
case <-hub.Ready(): // streams are accepted from here
case err := <-runErr: // the broadcaster could not subscribe
}
```

Waiting on `Ready` is optional; a host that starts serving at once has its first
streams refused as unavailable until the hub is ready, and clients retry.

| Concern | Default | Override |
| --- | --- | --- |
| Heartbeat on an idle stream | 25s | `WithHeartbeat` |
| Closing a client that accepts no writes | after 10s | `WithWriteTimeout` |
| Streams per recipient on one instance | 8, counted across SSE and WebSocket | `WithMaxStreamsPerRecipient` |
| Who may follow whose signals | `SelfOnly`: a user follows only their own | `WithSubscriptionAuthorizer` on the handler; `AllowAll` to permit everything |

- **A signal says that something changed, never what.** It reaches a client as an
  `unread-changed` event carrying only the kind of change — `created`, `read`,
  `closed` or `pruned` — and when. A client re-reads its unread count or list from
  the store, which stays the source of truth.
- **A slow client never slows a publisher.** Each stream holds at most one pending
  signal; newer signals replace it.
- **No replay.** Signals missed while a client was disconnected are not replayed.
  On connecting or reconnecting, a client must re-read its count and any open
  list before relying on signals.
- **The default broadcaster reaches a single instance.** With more than one
  instance, a client connected to instance B misses signals for changes written on
  instance A. Supply a broadcaster that crosses instances.
- A stream requested while the hub is not running is refused as unavailable
  rather than opened and left silent. The hub counts as running only once its
  broadcaster has confirmed its subscription, so a signal broadcast after
  `Running` reports true, or after `Ready` closes, reaches the instance's streams.
- **After a broker connection drops**, the broadcaster's client resubscribes on
  its own and `Running` stays true meanwhile; signals in that gap are lost, and
  clients recover by re-reading.
- **A broadcaster of your own** implements `Listen(ctx, deliver, ready)`: it calls
  `ready` once its subscription is confirmed, and never calling it leaves the hub
  refusing every stream. `notifytest.RunBroadcasterSuite` checks it against the
  contract.

## HTTP and mounting

`NewHandler(svc, hub, options...)` serves the contract as a standard library
`http.Handler`. It authenticates nobody: `WithActor` is required, and says how the
acting user is read from a request.

| Route | Does |
| --- | --- |
| `GET /v1/notifications` | the caller's notifications, newest first; filters `state`, `kind`, `subject`; `limit` 50 by default, at most 500; `cursor` continues |
| `GET /v1/notifications/count` | `{"count": n}` of the caller's `ACTIVE` notifications |
| `POST /v1/notifications/{id}/read` | marks one read and answers it |
| `POST /v1/notifications/read-all` | marks everything read, up to the optional `{"through": RFC 3339}` |
| `GET /v1/notifications/stream` | a server-sent event stream of change signals |

- `WithBasePath` replaces `/v1`.
- Every endpoint acts on the acting user's own notifications. No parameter names
  another recipient, except the stream's `recipient`, which only a host
  subscription policy can permit.
- The stream's policy defaults to `SelfOnly`. `WithSubscriptionAuthorizer`
  replaces it, for example with one that lets a supervisor follow a team member;
  pass `AllowAll` to permit everything. A nil policy is a configuration error
  from `NewHandler`.
- The stream opens with `: connected`, sends each change as
  `event: unread-changed` with `data: {"change":"created","at":"..."}`, and writes
  `: heartbeat` comments while idle. It sets `Content-Type: text/event-stream`,
  `Cache-Control: no-cache` and `X-Accel-Buffering: no`.
- Errors use one body, `{"error":{"code","message"}}`, in the same vocabulary as
  the task HTTP contract. `WriteError` is exported so that other transports answer
  the same way.

| Condition | Status | Code |
| --- | --- | --- |
| a malformed request | 400 | `validation_failed` |
| no acting user, or a refused subscription | 403 | `forbidden` |
| an unknown notification, or someone else's | 404, identically | `not_found` |
| too many streams for one recipient | 429 | `too_many_streams` |
| a stream while the hub is not running | 503 | `unavailable` |
| anything else | 500, with no internal detail | `internal` |

**Mounting.** On the standard library:

```go
handler, err := notify.NewHandler(svc, hub, notify.WithActor(currentUser))
mux.Handle("/v1/notifications", handler)
mux.Handle("/v1/notifications/", handler)
```

On Gin, wrap the same handler with `gin.WrapH`, registering the bare path and
everything under it:

```go
r.Any("/v1/notifications", gin.WrapH(handler))
r.Any("/v1/notifications/*path", gin.WrapH(handler))
```

On Fiber, which is not built on the standard library's HTTP types, mount it
through Fiber's adaptor, `github.com/gofiber/fiber/v3/middleware/adaptor`:

```go
app.All("/v1/notifications/*", adaptor.HTTPHandler(handler))
```

The event stream does stream through Fiber v3's adaptor: checked with Fiber
v3.5.0, the connected comment and a change event reach the client while the
stream is open. A different Fiber major version should be checked again.

**Behind a proxy.** Server-sent events are ordinary long-lived HTTP responses:

- disable response buffering for the stream route (`X-Accel-Buffering: no` does
  this for nginx; others need their own setting);
- keep the proxy's idle timeout well above the heartbeat;
- prefer HTTP/2 to the client: browsers limit HTTP/1.1 connections per origin;
- sticky sessions are not needed — a stream subscribes on whichever instance it
  lands on — but more than one instance needs a cross-instance broadcaster.

## Stated limits

- **A notification write does not join the caller's transaction.** Each store
  method runs in a transaction of its own, even when the caller holds one on the
  same executor; a notification survives the caller's rollback. Publishers deliver at
  least once and publishing is idempotent, so a shared transaction would buy
  nothing.
- **The default broadcaster serves a single instance.** See Realtime.
- **Retention bounds are approximate** between pruning passes, and
  `EvictOldestActive` can delete unread notifications.
- **No signal replay.** A reconnecting client must re-read.
- **A group join after a notification was written does not deliver it.**
  Notifications are per recipient; a publisher expands groups when it publishes.
