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
