## Context

See proposal.md — Why. `relay.Sink` is `Name()` plus `Deliver(ctx, Attempt) Outcome`. It must respect the deadline on `ctx` and must not retry internally. Two sinks implement it today, `delivery/webhook` and `delivery/redis`, each a module of its own. The facts below come from reading `github.com/nats-io/nats.go` v1.53.1 and `testcontainers-go/modules/nats` v0.44.0 during exploration. A locally pulled `nats:2-alpine` image reports server v2.12.7.

**Plain publish (`nats.go`)**
- `Conn.PublishMsg` takes no context. It validates the subject (`ErrBadSubject`, unless the host set `SkipSubjectValidation`).
- It rejects headers the server does not support (`ErrHeadersNotSupported`), a closed or draining connection, and a payload over the server's maximum (`ErrMaxPayload`).
- While reconnecting it appends to a buffer (8 MB by default) and returns nil. It returns `ErrReconnectBufExceeded` only once that buffer is full.

**Flush (`context.go:174`)**
- `Conn.FlushWithContext` requires a deadline (`ErrNoDeadlineContext` otherwise).
- It sends a PING and waits for the PONG. During a reconnect the PONG cannot arrive, so it ends with the context error.

**JetStream publish (`jetstream/publish.go`)**
- `PublishMsg` publishes via `RequestMsgWithContext`, so it respects the context. It applies a 5 s default only when the context has no deadline.
- On `nats.ErrNoResponders` it **retries internally**: `DefaultPubRetryAttempts = 2`, `DefaultPubRetryWait = 250ms`. It then returns `ErrNoStreamResponse`.
- `WithMsgID` sets `Nats-Msg-Id`. The `PubAck` reports `Duplicate`. `WithExpectStream` sets `Nats-Expected-Stream`.

**Headers and names**
- Header values have `\r` and `\n` replaced with spaces by the client (`nats.go:980-988`).
- `hmntsk.Registry.Register` rejects only an empty type name (`registry.go:117`), so a task type may contain `.`, `*`, `>` or whitespace.
- All thirteen event types have the form `task.<verb>`.

**Test container**
- `nats.Run(ctx, img, ...)` starts the server with `-DV -js`: JetStream on, debug and trace logging on.

## Goals / Non-Goals

**Goals:**

- One module giving hosts both ways NATS is used, with each mode's delivery guarantee stated exactly, not implied.
- A message contract a NATS consumer can route on using server-side subject filtering and headers, without a database.
- The sink contract kept exactly: context deadlines respected, no internal retries, every failure classified.
- The bus-delivery specs reorganised so each broker's specifics live in its own capability.

**Non-Goals:**

- **Creating or managing streams, consumers or subscriptions.** Producer only, as for Redis (event-delivery D9).
- **Confirming that a plain-subject subscriber received a message.** Plain NATS has no such acknowledgement. Request/reply would need every consumer to answer, which is a consumer-side protocol this library does not impose.
- **Signing messages.** Authentication and TLS belong to the host's NATS connection, which the sink never builds.
- **JetStream async publishing, KV or object store.** Each attempt is one synchronous publish.
- **Retention options on the NATS sinks.** Retention is stream configuration (D7).

## Decisions

### D1. Both plain subjects and JetStream

hmntsk is a library, and its hosts use NATS both ways. The two modes differ in what "delivered" can mean, and each keeps its own meaning:

| | Plain-subject sink | JetStream sink |
|---|---|---|
| Delivered when | the server has received it | a stream has stored it |
| No subscriber or stream | delivered, and lost | retryable |
| Redelivery | consumer de-duplicates on `Hmntsk-Event-Id` | stream discards it within its duplicate window |

_Alternative:_ JetStream only. Rejected: it would leave hosts with plain subscribers to write their own sink, which is the gap this change closes.

### D2. Two sink types with distinct default names

`NewSink(conn *nats.Conn, opts ...Option)` defaults to the name `nats`. `NewJetStreamSink(js jetstream.JetStream, opts ...JetStreamOption)` defaults to the name `jetstream`.

The inputs differ: a JetStream context already carries the domain and API prefix the host chose, and a plain connection has neither. Some options only mean something for one mode (expected stream, message ID).

The names matter most. The relay records acceptance per sink name, so one type with a mode option and one default name would make a switch from plain to JetStream skip every event the plain sink had already accepted. None of them would ever be stored.

Shared options (name, subject prefix, timeout) are declared once and accepted by both constructors. JetStream-only options are a separate type, so passing one to `NewSink` does not compile.

_Alternative:_ one `New(conn, WithJetStream())`. Rejected for the name trap and for the options that are meaningless in the other mode.

### D3. Subject is `<prefix>.<event type>`; task type stays out of it

The default prefix is `hmntsk.events`, and `WithSubjectPrefix` changes it. Event types are fixed, dot-separated, valid tokens, so `hmntsk.events.>` captures everything and `hmntsk.events.task.escalated` selects one type.

The task type would be the obvious next token, but the registry allows any non-empty name. A `*` or `>` in it would publish to a wildcard, which `ErrBadSubject` rejects, turning every event of that type into a failure. A `.` would silently change the subject's depth.

Encoding the task type into a token was considered and rejected. It would give consumers an escaping scheme to reverse, for filtering they can already do on the `Hmntsk-Task-Type` header.

The prefix is validated in the constructors with the same rules NATS applies to a publish subject: non-empty, no empty tokens, no leading or trailing dot, no wildcards, no whitespace. A bad prefix therefore fails at wiring, not on every event.

_Contrast with Redis (event-delivery D9):_ one Redis stream carries everything because a Redis consumer cannot filter on the server. NATS subjects are the filter, so NATS uses them.

### D4. Headers for routing, the whole event as the body

Headers use the webhook sink's names and values, which a receiver of either already knows:
- `Hmntsk-Event-Id`, `Hmntsk-Delivery-Id`, `Hmntsk-Event-Type`, `Hmntsk-Task-Id`, `Hmntsk-Task-Type`;
- `Hmntsk-Correlation-Owner-Type`, `-Owner-Ref` and `-Activity-Key`, each omitted when empty;
- plus `Hmntsk-Attempt` (as the Redis sink carries) and `Hmntsk-Schema`.

`delivery/nats` cannot import `delivery/webhook`, so it declares the same constant values itself. The docs test pins both against `docs/delivery.md`.

The schema value is `hmntsk.nats.event.v1`. It names this header-and-body contract, which is not the Redis sink's field contract (`hmntsk.event.v1`), so a consumer cannot mistake one for the other.

The body is `json.Marshal(event)`, exactly as the Redis sink's `event` field. The content-type header is `application/json`.

**Header values are not rejected for CR/LF.** The client already replaces them with spaces, so framing cannot break. The sink states that the body is authoritative and headers are routing hints. Rejecting such events as permanent would dead-letter every event for a task whose host-supplied correlation value happens to contain a line break.

_Alternative:_ Redis-style flat fields in a JSON body with no headers. Rejected: NATS has headers, and header-based routing is what consumers and gateways use without parsing the payload.

### D5. Plain-subject delivery is publish, then flush within the attempt's deadline

`Deliver` renders the message, calls `PublishMsg`, then `FlushWithContext` under a context bounded by the sink's timeout. The default timeout is 5 s, matching the Redis sink's reasoning: one unresponsive broker must not hold the pass open.

Only a nil flush is delivered:
- A publish that landed in the reconnect buffer returns nil, but its flush cannot complete. It ends at the deadline and is retryable.
- If the buffered bytes are flushed after reconnecting, the next pass republishes the event. That is the at-least-once duplicate the event identifier exists for.

`FlushWithContext` refuses a context without a deadline, and the sink always sets one. The relay's own deadline still applies when shorter.

One flush per event costs one round trip per delivery. That is accepted: the relay delivers in passes, and batching publishes before one flush would make one event's outcome depend on another's.

### D6. JetStream publishes once, with the event ID, and a duplicate is delivered

`Deliver` calls `js.PublishMsg(ctx, msg, jetstream.WithRetryAttempts(0), jetstream.WithMsgID(event.ID), [jetstream.WithExpectStream(name)])` under the sink's timeout.

- **`WithRetryAttempts(0)` is required, not tuning.** The client's default two retries at 250 ms would be an internal retry the `Sink` contract forbids, and would multiply the relay's attempt budget.
- **The message ID is the event ID, not the delivery ID.** A redelivery is the same event, and the stream should discard it.
- **A `PubAck` with `Duplicate` set is delivered.** The stream already holds the event.

The duplicate window is the stream's own (commonly 2 minutes). A redelivery after the window is stored again, so consumers still de-duplicate on the event ID, as with every sink.

### D7. The sink never touches streams; a missing stream is retryable

A stream's subjects, retention, replicas, storage and duplicate window are storage decisions a host makes once. Creating streams from a sink would make them on every host's behalf and race between relay instances. So the JetStream sink creates, updates and deletes nothing.

With no stream bound to the subject, the publish returns `ErrNoStreamResponse`. This stays retryable, like every broker rejection in `bus-delivery`, because the same error appears briefly during a stream leader election. A permanent verdict would dead-letter a backlog over a blip. The cost is that a host that forgot to create the stream retries until the attempt limit and then dead-letters. The docs say so, and name the error.

`WithExpectStream(name)` is offered for hosts with overlapping stream subjects. A publication that would land elsewhere is rejected by the server before storage and is retryable, since a stream reconfiguration fixes it without redeploying.

### D8. Classification

| Failure | Mode | Verdict |
|---|---|---|
| event has no ID, or will not marshal | both | permanent (`ErrInvalidEvent`) |
| `nats.ErrMaxPayload` | both | permanent: same event, same size |
| `nats.ErrBadSubject` | both | permanent: cannot occur for a validated prefix and a catalogue event type, but classified for completeness |
| `nats.ErrHeadersNotSupported` | both | retryable: the client also returns it for every headered message on a connection that has not completed its first connect (`RetryOnFailedConnect`, no server INFO yet), so a permanent verdict would dead-letter every event published before the first connect. A genuinely header-less server (older than 2.2) retries until dead-lettered, the same trade-off as a forgotten stream. _Changed during apply, with the user's approval; originally permanent._ |
| closed or draining connection, `ErrReconnectBufExceeded`, flush or publish deadline, context cancelled | both | retryable |
| `jetstream.ErrNoStreamResponse`, expected-stream mismatch, any other JetStream API error | JetStream | retryable |

Every retryable error is wrapped in a `PublishError` (subject, event ID, cause) matching `ErrPublish`, mirroring `delivery/redis`'s error taxonomy.

### D9. The specs split into a shared contract and one capability per broker

`bus-delivery` keeps the five requirements every bus sink meets. Its five retention requirements describe Redis trim modes and consumer groups, so they move word for word to a new `redis-delivery` capability, and `nats-delivery` is new.

A future Kafka sink is then one new capability with no edit to another broker's spec. Moving the text unchanged keeps the Redis behaviour and its tests exactly as they are.

_Alternative:_ keep one `bus-delivery` and scope requirements with "the Redis sink…" and "the NATS sink…". Rejected: the shared contract would grow broker special cases with every sink.

### D10. Tests against a real server, JetStream enabled, quiet

`delivery/nats/testutils.go` exposes `RunTestNATS(t, opts ...TestOption) *nats.Conn`, per the `use-testcontainers` rule:
- pinned `nats:2.12.7-alpine`;
- the command overridden to `-js`, dropping `-DV`, whose trace output buries test failures;
- termination registered immediately.

JetStream tests build `jetstream.New(conn)` and create the streams they need themselves. They are standing in for the host, which the sink never does.

Reconnect and outage cases put a switchable TCP proxy between client and server, as `delivery/redis`'s `TestBacklogSurvivesAnOutage` does. Restarting a container would not come back on the same port.

## Risks / Trade-offs

- [Plain mode loses an event with no subscriber, and reports it delivered] → Stated in the spec, the constructor's godoc and `docs/delivery.md`. The mode exists for hosts who already accept plain NATS semantics; JetStream is the answer for those who do not.
- [A forgotten JetStream stream dead-letters the backlog after the attempt limit] → Documented with the exact error. The same trade-off as `delivery/redis` trim modes (retryable over permanent), for the same leader-election reason.
- [The duplicate window is finite] → Documented; consumers de-duplicate on `Hmntsk-Event-Id` regardless.
- [Header and body can disagree for values with line breaks] → The body is authoritative, documented; a test pins the behaviour.
- [A future nats.go changes its no-responders retry default or behaviour] → A test asserts one publication per attempt against a real server, so a dependency bump that reintroduces retries fails loudly.
- [One flush round trip per plain-subject event] → Accepted (D5); relay passes are bounded batches.
- [The spec move rewrites capabilities archived today] → Text is moved unchanged; the Redis sink and its tests are untouched.

## Migration Plan

Additive for code: a new module that no host imports until it chooses to.

The spec reorganisation changes only where five requirements live. Archiving the change syncs `bus-delivery` (removals), `redis-delivery` (new) and `nats-delivery` (new) together.

Like the other satellites, the module resolves core through `go.work` until the core is tagged with the `relay` package.
