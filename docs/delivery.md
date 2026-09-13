# Delivering events

The engine records an event durably inside the transaction that produced it.
Getting it out again is the relay's job, and the relay is opt-in: a host that
starts none sees no delivery at all.

```go
r, err := relay.NewRelay(svc,
    relay.WithSinks(hook, bus),
    relay.WithMaxAttempts(8),
)
go r.Run(ctx, 10*time.Second)   // yours to start, and yours to stop
```

Nothing starts on its own. Constructing a relay begins no goroutine, no timer
and no polling, exactly as constructing a sweeper does not.

Each pass claims the events that are due, by a time-bounded lease taken with a
conditional update, so relays in several application instances do not deliver
the same event twice — and so that claiming works identically on a database with
no row-level locking. Every claimed event is offered to every configured sink,
and acceptance is recorded **per sink**: a broker outage does not re-POST to a
webhook that already succeeded.

A retryable failure is rescheduled after `min(base × 2^attempts, ceiling)` plus
jitter. The jitter is not decoration: without it, a receiver that was down for
five minutes comes back to its entire backlog retrying in lockstep, which is how
a recovering service is knocked over a second time.

An event that exhausts its attempts, or that a sink rejects permanently, is
**dead-lettered**: it stops being attempted and is retained with its attempt
count and its last error. Nothing prunes dead letters and nothing alerts on
them; they are rows, and querying them is the interface.

---

# The webhook contract

This is what a receiver at a task's `CallbackTarget.Address` implements. It is
the contract, not an illustration: `delivery/webhook`'s `docs_test.go` asserts
that every header and range named below is the one the code actually produces,
and `ExampleVerifier_Verify` verifies a real signed delivery against it.

An event is delivered here only if the task that produced it carries a callback
address. A task created without one is treated as delivered by this sink,
without a network call.

## The request

```
POST <CallbackTarget.Address>
Content-Type: application/json; charset=utf-8
Accept: application/json
User-Agent: hmntsk-webhook/1
```

| Header | What it carries |
| --- | --- |
| `Hmntsk-Event-Id` | The event. **Stable across redeliveries** — this is the de-duplication key |
| `Hmntsk-Delivery-Id` | This attempt. Fresh every time, including for a redelivery of the same event. Minted by the relay, not the sink |
| `Hmntsk-Event-Type` | The catalogue member, such as `task.completed` |
| `Hmntsk-Task-Id` | The task the event is about |
| `Hmntsk-Task-Type` | The task's registered type name |
| `Hmntsk-Correlation-Owner-Type` | The host's correlation data, echoed unchanged |
| `Hmntsk-Correlation-Owner-Ref` | |
| `Hmntsk-Correlation-Activity-Key` | |
| `Hmntsk-Timestamp` | Seconds since the Unix epoch, in decimal. Part of the signed material |
| `Hmntsk-Signature` | `v1=<lowercase hex>` — see below |

The headers are a convenience for routing and de-duplication without parsing.
The body always carries the same data, and a correlation value containing bytes
a header cannot legally carry is omitted from the header rather than failing the
delivery. **Route on the body if you need a guarantee.**

## The body

```json
{
  "deliveryId": "0193f0a1-1c00-7000-8000-00000000000b",
  "deliveredAt": "2026-03-01T12:00:00Z",
  "event": {
    "id": "0193f0a1-1c00-7000-8000-000000000001",
    "type": "task.completed",
    "taskId": "0193f0a1-1c00-7000-8000-0000000000aa",
    "taskType": "approval",
    "status": "COMPLETED",
    "version": 4,
    "actor": "alice",
    "assignee": "alice",
    "candidates": { "users": ["alice", "bob"], "groups": ["loan-officers"] },
    "createdBy": "origination-service",
    "occurredAt": "2026-03-01T12:00:00Z",
    "output": { "approved": true }
  },
  "correlation": { "ownerType": "process", "ownerRef": "loan-4711" },
  "referenceParameters": { "tenant": "acme", "callbackToken": "…" }
}
```

`referenceParameters` are the caller's own bytes, echoed **verbatim** — same
names, same order, same number formatting, same whitespace, including names the
engine has never heard of. They are spliced in rather than re-encoded, because
re-encoding would reorder keys and round a 64-bit integer through a float.
If you supplied it, you get it back exactly.

The event carries its **audience at the moment of the transition**, so a
receiver can decide who to tell without reading the task back:

| Field | Present |
| --- | --- |
| `candidates` | The pool after the transition — users, groups and exclusions, exactly as the pool names them. Groups are never expanded into members |
| `previousAssignee` | Only on a release or a delegation: the holder the transition replaced |
| `createdBy` | Whenever the task was created by a named actor |

A receiver therefore sees the identifiers in a task's pool. The body already
carried `actor` and `assignee`; a host that must not disclose pools to a
particular receiver supplies its own `relay.Sink` or filters at the receiver.

## Verifying a delivery

```
signature  = "v1=" + hex(HMAC_SHA256(secret, timestamp + "." + body))
timestamp  = the decimal Unix seconds in Hmntsk-Timestamp
body       = the raw request bytes, before any parsing
```

The timestamp is *inside* the signed material, not merely beside it. Signing the
body alone would make every captured delivery replayable forever; with the
timestamp signed, an attacker cannot move it forward without invalidating the
signature, and a receiver enforcing a freshness window can reject a capture.

Two mistakes are easy to make here and quiet when made: comparing signatures
with `==`, which leaks the expected value a byte at a time, and skipping the
freshness check, which makes any capture valid forever. Use `hmac.Equal`, and
enforce the window.

Go receivers do not need to write it at all:

```go
verifier, _ := webhook.NewVerifier(secret)          // 5 minutes by default
if err := verifier.Verify(r.Header, body); err != nil {
    http.Error(w, "bad signature", http.StatusUnauthorized)
    return
}
```

## De-duplicating

Delivery is **at-least-once**, and deliberately not exactly-once: a relay that
crashes between a successful POST and recording that success will deliver again.
Two deliveries carrying one `Hmntsk-Event-Id` and two `Hmntsk-Delivery-Id`s are
the same event twice.

Keep the event identifiers you have handled and discard repeats. Answer a repeat
with a 2xx, not an error — the engine was right to retry, and a 2xx is how the
retrying stops.

Events are attempted oldest first, so a backlog drains in the order it
accumulated. That is **not** a per-task ordering guarantee: a failed event
retries later than one recorded after it, so a task's events can arrive out of
order. Order by `occurredAt`, or by the task version, never by arrival.

## What your response means

| You answer | The engine concludes | What happens next |
| --- | --- | --- |
| `2xx` | delivered | Never sent to you again |
| `408`, `429` | retryable | Retried after the backoff |
| Any other `4xx` | permanent | Dead-lettered without spending the remaining attempts |
| `5xx` | retryable | Retried after the backoff |
| Timeout, connection refused, TLS failure | retryable | Retried after the backoff |

`429` is retryable rather than permanent because it is the one 4xx that means
"later", not "no". Every other 4xx says the request itself is wrong, and sending
it again unchanged cannot make it right.

Each attempt is bounded by a timeout — 10 seconds by default, `WithTimeout` to
change it — so that one unresponsive receiver cannot hold a relay pass open.

---

# Delivering to an address a caller chose

`CallbackTarget.Address` is supplied by whoever created the task. Delivering to
it makes the engine an HTTP client aimed wherever a caller points it, from
inside the host's network — the textbook SSRF shape, and the first genuinely
dangerous surface in this codebase.

Three things stand between that and an incident:

1. **The policy is consulted at dial time, against the address the hostname
   actually resolved to** — not against the URL's text. A name that resolves to
   a public address when inspected and a private one microseconds later defeats
   any pre-flight check; judging the resolved address does not have that gap.
2. **Redirects are not followed.** A clean first hop followed by a `302` to
   `169.254.169.254` is the oldest trick in this family.
3. **A refusal is permanent, not retryable.** Retrying cannot change the verdict,
   so the event is dead-lettered with the reason recorded rather than retried
   until its attempts run out.

## What the default refuses

`DefaultPolicy` refuses every destination that is not a routable public address:

| | Refused |
| --- | --- |
| Loopback | `127.0.0.0/8`, `127.0.0.1`, `::1` |
| Link-local — **including cloud metadata at `169.254.169.254`** | `169.254.0.0/16`, `fe80::/10` |
| Private | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7` |
| Shared address space | `100.64.0.0/10` |
| Unspecified and broadcast | `0.0.0.0/8`, `::`, `255.255.255.255` |
| Multicast | including interface-local and link-local |
| Protocol assignments and benchmarking | `192.0.0.0/24`, `198.18.0.0/15` |

An IPv4 address wearing IPv6 clothing is unwrapped and judged as the IPv4
address it is — IPv4-mapped (`::ffff:127.0.0.1`), NAT64 (`64:ff9b::/96`,
`64:ff9b:1::/48`) and 6to4 (`2002::/16`). Without that, every rule above is one
notation away from being bypassed.

What it cannot refuse is a *public* address that your own network routes
somewhere private. Split-horizon DNS and transparent proxies are things your
network knows and this policy does not. If you run either, supply your own.

## Overriding it

The default is safe, not mandatory. An internal callback address is a legitimate
configuration when the host has decided so:

```go
sink, err := webhook.New(secret,
    webhook.WithDestinationPolicy(webhook.PolicyFunc(
        func(ctx context.Context, dest webhook.Destination) error {
            if dest.IP.String() == "10.4.0.7" {
                return nil                       // our own notification service
            }
            return webhook.DefaultPolicy{}.Allow(ctx, dest)
        },
    )),
)
```

`webhook.AllowLoopback()` permits loopback and nothing else. It exists for tests
whose receiver is an `httptest.Server` on `127.0.0.1`, which the default
correctly refuses.

A policy is consulted once **per dialled address**, so a dual-stack name that
resolves to both an allowed and a refused address is judged on the one actually
being connected to.

---

# Bounding the Redis stream

`delivery/redis` appends every event to one stream with `XADD`. The message
contract — the fields, and de-duplicating on `eventId` — is in the package
documentation (`go doc github.com/kartaladev/hmntsk/delivery/redis`). This
section is about how long the stream keeps what it is given, because every
choice here either loses messages or stops publishing when it is made wrongly.
`delivery/redis`'s `docs_test.go` asserts that the names, the version and the
error quoted below are the ones the code and a real broker use.

**By default the stream is unbounded.** Nothing is ever removed, and the stream
grows until Redis runs out of memory. That default does not change unless you
set a bound.

## Setting a bound

```go
sink, err := redis.New(client,
    redis.WithMaxLen(1_000_000),          // XADD hmntsk.events MAXLEN ~ 1000000 * ...
)

sink, err := redis.New(client,
    redis.WithMaxAge(7*24*time.Hour),     // XADD hmntsk.events MINID ~ <now − 7d, in ms>-0 * ...
)
```

The bound rides on the publish itself; there is no separate trim command and no
job to schedule. Choose one: `New` refuses both at once, a non-positive bound,
and a trim mode with no bound.

**Trimming is always approximate, and approximate means "at least".** Redis
stores a stream in nodes of up to `stream-node-max-entries` entries (100 by
default) and approximate trimming only removes whole nodes. The stream therefore
never holds fewer entries than `WithMaxLen` allows, and may hold up to about one
node more: a bound of 10 on a 251-entry stream keeps 51 at the default node
size. Likewise `WithMaxAge` never removes an entry newer than the cutoff, and may
leave some older ones until their node is wholly expired.

**One publish trims at most 100 × `stream-node-max-entries` entries** — 10,000 at
the default. On a new stream that never matters. Set a bound on an existing
stream of fifty million entries, and it shrinks by that much per publish rather
than on the first one.

**The age cutoff uses the sink's clock; entry IDs use the broker's.** Skew
between the two shifts retention by the skew. `WithClock` supplies the clock the
cutoff is computed from, the host's system clock by default.

## Trim modes: what trimming does to consumer groups

A bound deletes entries whether or not anyone has read them. `WithTrimMode`
decides what that means for the consumer groups your consumers use:

| Mode | Entries a group has not acknowledged | Those entries' IDs in the group's pending list |
| --- | --- | --- |
| _unset_ (Redis's default, which on 8.2 is `KEEPREF`) | trimmed | kept, with no content behind them |
| `TrimKeepRef` (`KEEPREF`) | trimmed | kept, with no content behind them |
| `TrimDelRef` (`DELREF`) | trimmed | removed |
| `TrimAcked` (`ACKED`) | **kept**: trimming stops at the first entry any group has not acknowledged | untouched |

```go
sink, err := redis.New(client,
    redis.WithMaxLen(1_000_000),
    redis.WithTrimMode(redis.TrimAcked),  // XADD hmntsk.events ACKED MAXLEN ~ 1000000 * ...
)
```

What each costs you:

- **Unset and `TrimKeepRef` lose unread messages.** A consumer that falls further
  behind than the bound finds IDs in its pending list whose entries are gone:
  reading its history returns the ID with no fields, and claiming it returns
  nothing. Consumer code must tolerate a pending entry with an empty body.
- **`TrimDelRef` loses them silently.** The IDs vanish from the pending list too,
  so a consumer never learns that anything was lost.
- **`TrimAcked` can stop trimming altogether.** An entry a group has never read is
  an entry that group has not acknowledged. One stale group — created by a
  consumer you no longer run, or one that is stuck — holds every entry after its
  position, and the stream is unbounded again, silently. Delete groups you no
  longer run (`XGROUP DESTROY`), check `XINFO GROUPS` for lag, and alert on
  `XLEN`. With no groups at all, `TrimAcked` trims exactly as an unset mode does.

## Trim modes need Redis 8.2 or newer

**Every trim mode requires Redis 8.2+**, including `TrimKeepRef`, which only
names what 8.2 does anyway. On an older server, every publish from a sink with a
trim mode fails with:

```
ERR Invalid stream ID specified as stream command argument
```

The error names neither trimming nor modes. The sink classifies it as retryable,
exactly as it does every broker rejection — the sink cannot tell a server that
will never accept the command from one that is briefly refusing writes — so the
relay retries each event with backoff until the attempt limit, and then
**dead-letters it**. Nothing reaches the stream in the meantime.

A bound **without** a trim mode sends no mode keyword and works on every Redis
version the sink supports. `New` does not dial, so it cannot check the server for
you. Check before you set a mode:

```sh
redis-cli INFO server | grep redis_version     # must be 8.2.0 or later
```

Removing a bound or a mode takes effect on the next publish. Entries already
trimmed are gone.

---

# Publishing to NATS

`delivery/nats` publishes every event to NATS, for internal consumers, in one of
two modes. Choose by what "delivered" has to mean for you: the modes do not
promise the same thing. Like the Redis sink it is a producer only, and creates
no subscription, consumer or stream. `delivery/nats`'s `docs_test.go` asserts
that the names, values and error quoted below are the ones the code uses.

| | `NewSink` — plain subjects | `NewJetStreamSink` — JetStream |
| --- | --- | --- |
| Takes | the host's `*nats.Conn` | a `jetstream.JetStream` on the host's connection |
| Default sink name | `nats` | `jetstream` |
| Delivered when | the server has received the message | a stream has stored it |
| No subscriber, or no stream | **delivered, and lost** | retryable, then dead-lettered |
| A redelivery | reaches subscribers again: de-duplicate on `Hmntsk-Event-Id` | discarded by the stream inside its duplicate window |

```go
plain, err := hmntsknats.NewSink(conn)                   // plain subjects
stored, err := hmntsknats.NewJetStreamSink(js)           // JetStream: you create the stream
```

Neither constructor dials, and neither closes what it is given: servers,
credentials, TLS and reconnect policy belong to the connection you build.

## Plain subjects: delivered means received

`NewSink` publishes and then flushes: it waits for the server to answer a ping
sent after the message, which the server can only do once it has read the
message. While the client is reconnecting it buffers publications and reports
success; the flush is what notices, and the attempt is retryable when no answer
arrives inside the timeout. If the client later reconnects and sends the
buffered message after all, the next pass publishes the event again — the
at-least-once duplicate every consumer already handles.

**Delivered does not mean anyone received it.** Plain NATS has no
acknowledgement from subscribers and keeps no history. An event published while
nothing subscribes to its subject is delivered, is never offered again, and no
subscriber ever sees it. If that is not acceptable, use JetStream.

## JetStream: delivered means stored

`NewJetStreamSink` makes one publication per attempt, and reports delivered only
when a stream acknowledges storing it.

**The sink never creates, updates or deletes a stream.** Subjects, retention,
replicas, storage and the duplicate window are your decisions, made once. Create
a stream capturing the prefix before you start the relay:

```go
_, err := js.CreateStream(ctx, jetstream.StreamConfig{
    Name:       "HMNTSK_EVENTS",
    Subjects:   []string{"hmntsk.events.>"},
    Duplicates: 2 * time.Minute,                         // the duplicate window
})
```

With no stream capturing the subject, every publish fails with:

```
nats: no response from stream
```

The sink classifies it as retryable, because the same answer comes back briefly
while a stream elects a new leader, and a permanent verdict would dead-letter a
backlog over that blip. The cost: **a forgotten stream retries each event until
the attempt limit, and then dead-letters it.** Nothing is stored in the meantime.

**One publication per attempt.** Left to its default, the client answers that
error with two more publications 250 ms apart. The sink turns that off: a retry
inside an attempt multiplies the relay's attempt budget by a number the relay
cannot see.

**Redeliveries are discarded by the stream — for a while.** Every publication
carries the event identifier as its `Nats-Msg-Id`, and the stream discards a
second message with that identifier inside its duplicate window (2 minutes
unless the stream says otherwise). The acknowledgement of a discarded duplicate
is reported delivered, because the stream holds the event. A redelivery after
the window is stored again, so consumers still de-duplicate on
`Hmntsk-Event-Id`.

`WithExpectStream` names the stream every publication must land in, for hosts
whose streams have overlapping subjects. It is off by default. A publication the
server would store in any other stream is refused before it is stored, and is
retryable: reconfiguring the stream fixes it without redeploying.

## The subject

```
<prefix>.<event type>          hmntsk.events.task.completed
```

The prefix is `hmntsk.events` by default, and `WithSubjectPrefix` changes it.
Event types are fixed and dot-separated, so the server filters for you:
`hmntsk.events.>` selects everything, `hmntsk.events.task.escalated` one type.

**The task type is not in the subject.** A task type may be any non-empty name,
including `.`, `*`, `>` and spaces: a wildcard would make the server refuse the
publication, and a dot would change the subject's depth. Filter on the
`Hmntsk-Task-Type` header instead. The prefix itself is checked at construction
against the rules for a publish subject — non-empty, no empty token, no
wildcard, no whitespace — so a bad prefix fails when the host starts, not on
every event.

## The message

| Header | What it carries |
| --- | --- |
| `Hmntsk-Event-Id` | The event. **Stable across redeliveries** — the de-duplication key |
| `Hmntsk-Delivery-Id` | This attempt. Fresh every time, minted by the relay |
| `Hmntsk-Attempt` | Which attempt this is, counting from one |
| `Hmntsk-Event-Type` | The catalogue member, such as `task.completed` |
| `Hmntsk-Task-Id` | The task the event is about |
| `Hmntsk-Task-Type` | The task's registered type name |
| `Hmntsk-Correlation-Owner-Type` | The host's correlation data, echoed unchanged; each omitted when empty |
| `Hmntsk-Correlation-Owner-Ref` | |
| `Hmntsk-Correlation-Activity-Key` | |
| `Hmntsk-Schema` | `hmntsk.nats.event.v1`, the version of this header-and-body contract |
| `Content-Type` | `application/json` |

The names and values are the webhook's, so a receiver of either already knows
them. The schema is not the Redis sink's `hmntsk.event.v1`, because the shape is
not the same.

The body is the whole event as JSON, exactly what the Redis sink carries in its
`event` field, including the output, the callback target, the transition and
the audience snapshot (`candidates`, `previousAssignee`, `createdBy`). Neither
the NATS headers nor the Redis flat fields carry the audience: a pool has no
size bound, and a header or flat field is a routing hint, not the record.

**Headers are routing hints; the body is authoritative.** A header value cannot
carry a line break, so the NATS client replaces `\r` and `\n` in one with a
space. A task type or correlation value containing a line break is still
delivered, with the sanitised value in the header and the original in the body.
Route on the headers; read the body when the exact value matters.

## Failures

| Failure | Verdict |
| --- | --- |
| The event has no identifier, or will not marshal | permanent |
| The message is larger than the server's `max_payload` (1 MB by default; the body grows with the task's candidate pool) | permanent |
| A subject the client refuses — impossible for a valid prefix and a catalogue event type | permanent |
| A timeout, a lost or reconnecting connection, a cancelled pass | retryable |
| `nats: headers not supported by this server` | retryable |
| `nats: no response from stream`, a publication bound for another stream, any other JetStream refusal | retryable |

The headers error is retryable although it sounds permanent: the client also
answers it for every message on a connection that has not finished connecting
for the first time, such as one built with `RetryOnFailedConnect` while the
server is down. A server that genuinely cannot carry headers — older than 2.2 —
therefore retries each event until it is dead-lettered.

Each attempt is bounded by a timeout — 5 seconds by default, `WithTimeout` to
change it — so that one unresponsive server cannot hold a relay pass open. A
shorter deadline on the relay's own context still wins.

## Switching modes, and the sink name

The relay records acceptance **per sink name**, for every event it is still
delivering. That is why the modes default to different names, `nats` and
`jetstream`: a host that replaces the plain sink with the JetStream sink has the
JetStream sink offered every event still in the outbox, including those the
plain sink already took. Give both the same name with `WithName`, and the relay
would count the plain sink's acceptance as the JetStream sink's: those events
would never be stored.

The other direction is the relay's general rule: renaming a sink redelivers
every event that sink has already taken. That is at-least-once, not a bug — but
a stream only discards the redeliveries that fall inside its duplicate window.
