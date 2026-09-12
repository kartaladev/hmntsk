## Context

The engine already writes events durably inside the producing transaction and exposes `SelectOutbox` (undelivered, oldest first) and a `published_at` update. Those are relay primitives with no caller. See `proposal.md` for why that matters and the three delta specs for what is being committed to.

This change inherits decisions from `human-task-engine` and must not contradict them: D2 (one core, N thin bindings), D6 (durable inside the transaction, dispatched after it), D10 (optimistic CAS is the portable concurrency mechanism; no dialect has usable row locks), D14 (shared conformance suites), D20 (escalation backs off by holding its lease). Where this design repeats a shape, that is deliberate reuse, not coincidence.

## Goals / Non-Goals

**Goals**

- Make the existing at-least-once promise real rather than aspirational.
- A sink interface narrow enough that adding Kafka or NATS later is a new module and nothing else.
- Safe by default when POSTing to an address a caller supplied.

**Non-Goals (design level)**

- Exactly-once delivery, and cross-task ordering.
- Consumer-side helpers for the bus.
- Administrative tooling for replaying dead letters. The rows are queryable; that is the interface.
- A circuit breaker per destination. Backoff plus the attempt cap is the v1 answer; a breaker is worth adding only once a real deployment shows one slow receiver starving the pass.

## Decisions

### D1. The relay lives in core; sinks are their own modules

```
                +--------------------------------+
                |   hmntsk/relay  (core module)  |
                |  claim, backoff, dead-letter,  |
                |  fan-out, per-sink accounting  |
                +----+----------------------+----+
                     |          Sink        |
          +----------v------+      +--------v---------+
          | delivery/webhook |      |  delivery/redis  |
          |  net/http only   |      |  redis client    |
          +------------------+      +------------------+
```

Same shape as D2. The relay owns every decision; a sink only knows how to attempt one delivery and classify the result. A sink is therefore about as large as a transport binder, and Kafka or NATS costs one module with no relay change.

The relay is in the core module rather than its own because it is pure storage behaviour over ports the core already defines — it needs `Repository`/`Transactor`, and splitting it out would either duplicate those ports or force core to export internals.

### D2. The `Sink` interface classifies outcomes rather than returning bare errors

```go
type Sink interface {
    Name() string
    Deliver(ctx context.Context, ev hmntsk.Event, task hmntsk.Task) Outcome
}

type Outcome struct {
    Status OutcomeStatus // Delivered | Retryable | Permanent
    Err    error
}
```

*Why not `error`:* the relay must distinguish "try again in a minute" from "this will never work" — a 400 and a 503 are both errors and must be treated oppositely. Encoding that in sentinel errors would put the classification in the sink's error values and leave the relay pattern-matching on them; a returned status makes it part of the contract, and makes a sink that forgets to classify a compile-time problem rather than a silent "retry forever".

`Deliver` receives the task as well as the event because the webhook sink needs `CallbackTarget`, which lives on the task, and re-reading it per event inside the sink would be a second query the relay has already paid for.

### D3. Claiming reuses the escalation lease, not a queue

Undelivered rows are claimed by conditional update on `locked_by` / `locked_until`, exactly as the escalation sweep claims tasks (D20). The same reasoning applies unchanged: SQLite has no row locks at all, so `FOR UPDATE SKIP LOCKED` cannot be the foundation, and a lease self-heals when a relay crashes mid-pass.

*Alternative rejected:* an external queue (Redis, SQS) holding pending deliveries. It would reintroduce the dual-write problem the outbox exists to solve — the event would have to be written to the database *and* the queue.

### D4. Backoff is exponential with jitter, and the ceiling matters more than the curve

`next_attempt_at = now + min(base * 2^attempts, ceiling) ± jitter`.

The jitter is the load-bearing part. Without it, a receiver that goes down for five minutes comes back to its entire backlog retrying in lockstep, which is how a recovering service is knocked over a second time. The ceiling matters because unbounded doubling turns a day-long outage into a week-long backlog drain.

### D5. Dead-lettering is a state, not a separate table

An exhausted or permanently failed event keeps its row and gains a dead-letter marker, its attempt count and its last error. A separate table would need the same columns plus a join for every "what failed?" question, and would let a row exist in both places.

This is the one place this change modifies an existing spec: `task-events` promised retention "until delivery is acknowledged", which a dead letter never is. Retention is now until acceptance *or* dead-lettering. Without that amendment the engine would be required to retry an unroutable webhook forever.

### D6. Per-sink accounting, because one flag cannot express two destinations

`published_at` as a single timestamp is only correct for one sink. With two, a webhook success followed by a broker outage would either redeliver the webhook on every retry or mark the event delivered with the bus never having seen it. The outbox therefore records which sinks have accepted an event, and a retry targets only the ones that have not.

*Alternative considered:* one outbox row per sink, fanned out at write time. Rejected because it puts sink configuration inside the producing transaction — adding a sink would then only affect events produced after the change, and the writer would need to know the delivery topology.

### D7. SSRF policy is a default-deny check on the resolved address, not the URL string

`CallbackTarget.Address` is supplied by whoever created the task. Delivering to it makes the engine an HTTP client aimed wherever a caller points it, from inside the host's network — the textbook SSRF shape, and the first genuinely dangerous surface in this codebase.

The check runs against the **resolved IP at connection time**, via the dialer, not against the hostname before resolution. Parsing the URL is not enough: a name that resolves to a public address on inspection and a private one microseconds later defeats any pre-flight check (DNS rebinding), and redirects can hop to an internal address after a clean first hop.

Default policy rejects loopback, link-local (including `169.254.169.254`), private ranges, unique-local IPv6 and unspecified addresses. Redirects are not followed by default. A host can supply its own policy, because an internal callback address is a legitimate configuration when the host has decided so — the default is safe, not mandatory.

A policy rejection is **permanent, not retryable**: retrying cannot change the verdict.

### D8. Signing is HMAC over timestamp and body, with the timestamp inside the signed material

The receiver needs to know a delivery came from this engine and is not a replay. The signature covers `timestamp || body`, and both are sent; a receiver enforcing a freshness window can then reject a captured delivery. Signing the body alone would make every capture replayable forever.

Symmetric HMAC rather than an asymmetric signature: the receiver is configured by the same operator who configures the engine, so key distribution is not the problem asymmetric signing solves, and HMAC needs no key management story to ship.

### D9. Redis Streams, and what that does and does not commit us to

The bus sink is a **producer only** — it `XADD`s and nothing more. Consumer groups, offsets and acknowledgement are the host's side of the line.

That is worth stating plainly because Redis Streams has weaker delivery semantics than JetStream or Kafka, and that difference lives almost entirely on the consumer side. On the producing side the requirement is "did the broker accept the write", which Redis answers as well as any broker. The relay's own durability comes from the outbox, not from the broker.

One stream for all events, with task type and correlation in the message fields for consumer-side filtering, rather than a stream per task type — a stream per type would make a consumer that wants everything subscribe to a set that changes whenever a task type is registered.

### D10. Ordering is oldest-first per pass, and that is not an ordering guarantee

Events are claimed oldest first, so a backlog drains in the order it accumulated. That is not a per-task ordering *guarantee*: a failed event retries later than an event recorded after it, so a consumer can observe a task's events out of order. Consumers order by the event's occurrence time or by task version, not by arrival.

This deliberately leaves `human-task-engine`'s open question on per-task ordering open. Guaranteeing it would mean blocking a task's later events behind a failed earlier one — head-of-line blocking that turns one unroutable webhook into a stalled task.

## Risks / Trade-offs

- **SSRF is the real risk in this change.** → Default-deny on the resolved IP at dial time, redirects off by default, policy rejection permanent, and conformance cases covering loopback, private range, metadata IP and a redirect to an internal address.
- **A slow receiver starves the pass.** One destination timing out repeatedly consumes the batch. → Per-attempt timeout plus a bounded batch; a per-destination circuit breaker is noted as future work rather than guessed at now.
- **The outbox schema change is breaking.** → Free only until the first tag; the DDL, `VerifySchema` and the three dialect files move together, and this lands before any module is tagged.
- **Dead letters accumulate silently.** Nothing prunes or alerts on them. → They are queryable and the relay result reports counts per pass; retention policy is the host's, and is documented rather than implemented.
- **Signing key rotation is unaddressed.** A single shared secret is configured at construction. → Adequate for one operator configuring both ends; if rotation is needed the signature format has room for a key identifier, and that is cheaper to add than to retrofit asymmetric signing.
- **Redis as the first bus sink may not match an adopter's broker.** → The sink interface is deliberately narrow, and Kafka or NATS is a module with no relay change.

## Migration Plan

No deployed instance exists and no module is tagged, so there is nothing to migrate. Adopters apply the updated DDL through their own pipeline and `VerifySchema` reports the new columns if they do not. The relay is opt-in: a host that starts no relay sees no behaviour change beyond the wider outbox table.

## Open Questions

- **Per-destination circuit breaking.** Whether a repeatedly failing destination should be skipped wholesale for a cooldown rather than retried per event. Deferrable: it is a scheduling refinement over the same state, changing no spec and no sink.
- **Dead-letter retention and alerting.** Whether the engine should prune dead letters after a period, or surface a metric. Deferrable, and tangled with `human-task-engine`'s open question on built-in instrumentation.
- **Signing key rotation.** Whether to carry a key identifier so two keys can be valid during a rotation window. Deferrable: additive to the signature header.
- **Kafka and NATS sinks.** Expected to be additive modules; the sink interface is designed for them but has only been proven against two.
