## Why

The engine records events durably inside the transaction that produces them, and `task-events` already promises they are "retained until delivery is acknowledged" — but nothing delivers them. `SelectOutbox` and the `published_at` update exist as primitives with no caller. A task completes, the outbox row lands, and it sits there.

That leaves the whole point of a per-task `CallbackTarget` unrealised: §5 of the architecture notes models it on the WS-Addressing Endpoint Reference precisely so an external caller can be notified without the engine knowing anything about it, and today the address is stored and never used. It is the first thing an adopter hits.

## What Changes

**A relay (`relay` package in the core module)**

- Claims undelivered outbox rows by time-bounded lease, so relays in several application instances do not deliver the same event twice.
- Retries with exponential backoff and jitter, capped by a maximum attempt count.
- **Dead-letters** an event that exhausts its attempts or fails permanently, recording the last error. **BREAKING against the current `task-events` wording**: retention is no longer "until delivery is acknowledged" without qualification — a dead-lettered event is retained for inspection, not for further delivery.
- Fans one event out to every configured sink, tracking success per sink so one failing sink does not re-deliver to a healthy one.
- Runs only when the host drives it, exactly as the escalation sweeper does. No implicit goroutine, timer or polling.

**A webhook sink (`delivery/webhook`)**

- POSTs to the task's `CallbackTarget.Address`, echoing `ReferenceParameters` verbatim — the Endpoint Reference contract from §5.
- Carries a stable delivery identifier and the originating event identifier, so a receiver can correlate and de-duplicate without its own correlation store. These are the plain-HTTP equivalents of `wsa:MessageID` and `wsa:RelatesTo`, closing a gap the first change left open.
- **Signs each request with an HMAC** over the body and a timestamp, so a receiver can verify the delivery came from this engine and reject replays.
- **Guards against SSRF.** `CallbackTarget.Address` is caller-supplied, so the engine would otherwise issue arbitrary POSTs from inside the host's network. Delivery is refused unless the resolved address passes a host-supplied policy, with a safe default that rejects loopback, link-local, private and metadata addresses.
- Maps response status to outcome: 2xx delivered, 4xx permanent failure (except 408 and 429), 5xx and transport errors retryable.

**A bus sink (`delivery/redis`)**

- Publishes each event to a Redis Stream with `XADD`, keyed so a consumer can filter by task type or correlation.
- Delivers §4.5's dual-publish: the bus for internal consumers, the webhook for external ones, from one relay pass.

**Outbox schema extension**

- `task_outbox` gains `attempts`, `next_attempt_at`, `last_error`, `locked_by`, `locked_until`, and per-sink delivery state, on all three dialects. **BREAKING to the published DDL** — free now, since no module is tagged and no adopter exists.

### Non-goals

- Kafka and NATS sinks. The sink interface is built to take them; they are separate changes.
- Consumer-side helpers. The relay is a producer; what a host does with a Redis Stream is its own concern.
- Exactly-once delivery, and cross-task ordering guarantees.
- A retry UI or an administrative API for replaying dead letters. The rows are queryable; tooling is out of scope.

## Capabilities

### New Capabilities

- `event-relay`: claiming undelivered events by lease, retry with backoff, dead-lettering, per-sink fan-out and per-sink success tracking, host-driven execution, and the ordering and idempotency guarantees consumers may rely on.
- `webhook-delivery`: delivery to a per-task callback address — verbatim reference-parameter echo, delivery and event identifiers for correlation and de-duplication, HMAC request signing, SSRF policy on caller-supplied addresses, and response-status-to-outcome mapping.
- `bus-delivery`: publication of events to a message bus as a relay sink, and what a consumer can rely on about payload shape and identifiers.

### Modified Capabilities

- `task-events`: the at-least-once retention requirement is qualified by dead-lettering, and delivery acknowledgement becomes per-sink rather than a single flag.

## Impact

- **New code**: a `relay` package in the core module, and two new modules `delivery/webhook` and `delivery/redis`, bringing the workspace to thirteen modules. A shared `relaytest` conformance suite follows the established pattern so both sinks are asserted by one set of cases.
- **Schema change**: `task_outbox` gains six columns across three dialects, with `VerifySchema` and the per-dialect DDL updated in step. Breaking to the published schema, and free only until the first tag.
- **New dependencies**: a Redis client, confined to `delivery/redis`. The webhook sink uses the standard library only. Neither reaches a host that does not import them.
- **CI**: a Redis container for the bus sink and an HTTP test server for the webhook sink, plus the relay suite across all seven store combinations — the relay's claiming is storage behaviour and must hold on every dialect.
- **Security surface**: this is the first component that makes outbound network calls to addresses supplied by callers. The SSRF policy and the signing scheme are load-bearing, not conveniences.
