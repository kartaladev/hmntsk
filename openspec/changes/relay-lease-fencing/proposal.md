## Why

The relay's lease makes claiming exclusive, but nothing makes settling exclusive. `RecordAttempt`, `MarkAccepted` and `MarkDeadLettered` filter only on the event ID (`store/sqlcore/query.go`, `memstore/outbox.go`), and `AttemptRecord`, `Acceptance` and `DeadLetter` carry no owner, so the port cannot express a fence at all. A relay whose lease ran out can therefore overwrite the relay that legitimately reclaimed the event. The audit reproduced this on memstore, SQLite, PostgreSQL and MySQL: a stale write un-publishes a delivered event, resurrects a dead letter, rolls back the attempt count, and releases the live lease of another relay, which lets a third relay deliver concurrently.

A pass also ignores its own lease and its context:

- **Lease overrun.** It keeps offering events after its lease has expired. With the defaults (5-minute lease, 50 events, 10-second webhook timeout), 21 of 50 events were delivered by two relays.
- **Cancellation.** After cancellation it still offers events, loses the settlement of an event the sink already took because it settles on the cancelled context, and strands unattempted events until their lease expires.
- **Backoff.** It measures backoff from the start of the pass, so an event that fails late in a pass is due again before it failed.
- **Overflow.** A huge backoff ceiling overflows to a next attempt in the past.
- **Option validation.** `NewRelay` silently ignores meaningless option values, which breaks library-design rule 6.

Every item is backed by a failing reproduction (section D and C9 of the audit's verified findings).

## What Changes

- **BREAKING (unreleased):** settlement is fenced by the lease that claimed the event.
  - `AttemptRecord`, `Acceptance` and `DeadLetter` gain a `Lease` field, the owner and deadline the claim returned. Every `OutboxStore` settlement applies only while the row still carries that exact lease.
  - A settlement whose lease was superseded is refused with a new error matching `hmntsk.ErrLeaseLost`, which in turn matches `ErrConflict`. The relay counts that event as `Unsettled` and reports the error to the host.
  - A settlement that carries no lease is refused.
- A partial acceptance never clears an event's published time. An event that is delivered stays delivered.
- **New port method `OutboxStore.ReleaseLeases`**, fenced the same way. The relay uses it to hand back claimed events it did not attempt, without charging an attempt. `Service.ReleaseEvents` exposes it to hosts that drive their own relay loop.
- **A pass stays inside its lease:**
  - It offers no event, and no further sink, once the lease deadline has passed. The events it did not start are released.
  - By default the context each sink receives is bounded by the time left on the lease.
- **A pass stops when its context is cancelled:**
  - It offers nothing more.
  - It records the outcomes sinks already returned on a context detached from the cancellation, bounded by a settlement timeout. The default is 10 seconds, configurable with `WithSettleTimeout`.
  - It releases the events it had not started, and returns the result together with the context's error.
- `Result` gains `Released`. The counters still account for every claimed event.
- **Backoff and jitter:**
  - The backoff is measured from the moment the event's attempt finished, not from the start of the pass.
  - The computed wait is clamped before it is converted to a duration, so it can never overflow.
  - Jitter is drawn inside `[wait×(1−j), min(wait×(1+j), ceiling)]`, which makes the ceiling a hard upper bound.
- **Default change (unreleased):** `DefaultMaxAttempts` rises from 5 to 12.
  - Today a destination that is down for 7½ minutes gets its events dead-lettered, and the 1-hour `DefaultBackoffCeiling` is never reached.
  - With 12 attempts an event survives an outage of roughly 4½ to 5 hours, depending on the jitter draw, and the ceiling applies from the 8th wait onwards.
  - The resulting schedule is documented.
- **Meaningless relay options fail at construction.** `NewRelay` returns a `ConfigurationError` for any of these instead of ignoring them:
  - `WithRelayLease` ≤ 0, `WithRelayBatch` ≤ 0 or `WithMaxAttempts` ≤ 0;
  - `WithBackoff` with a base or ceiling ≤ 0, or a ceiling below the base;
  - `WithJitter` < 0 or > 1;
  - a nil sink in `WithSinks`, an empty `WithRelayOwner`, a nil `WithJitterSource` or a nil `WithRelayErrorHandler`;
  - `WithSettleTimeout` ≤ 0.

  The godoc that says these values are "ignored" is corrected.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `event-relay`: several requirements change.
  - Exclusive claiming now also covers settlement and the pass's lease deadline.
  - Retry scheduling measures the wait from the failure, never overflows, and treats the ceiling as a hard bound.
  - The default retry budget is stated.
  - Cancellation, releasing unattempted events and the validation of relay configuration are new requirements.

## Impact

- **Core module:**
  - `outbox.go` gains `OutboxLease`, the `Lease` field on the three settlement records, `LeaseRelease` and `OutboxStore.ReleaseLeases`.
  - `errors.go` gains `ErrLeaseLost` and `LeaseLostError`.
  - `relayport.go` gains `Service.ReleaseEvents` and the lease check on the settlement wrappers.
- **Relay:** `relay/relay.go` changes the pass loop, deadline and cancellation handling, the settle context, the schedule, option validation, `Result.Released`, `WithSettleTimeout` and `DefaultMaxAttempts`.
- **Stores:**
  - `store/sqlcore/query.go` fences the settlement builders, adds a release builder and never writes a nil `published_at` on a partial acceptance.
  - The `settleOutbox` helpers in `store/sql`, `store/pgx` and `store/gorm` report a lease that was lost.
  - `memstore/outbox.go` gets the same fence.
- **No schema change:** the fence uses the existing `locked_by` and `locked_until` columns.
- **Suites:**
  - `storetest` adds settlement-fencing cases to the store matrix and memstore.
  - `relaytest` adds `Fencing`, `LeaseDeadline` and `Cancellation` cases and extends `Scheduling` and `DeadLettering`. Every store runs them: memstore, and `sql`, `pgx` and `gorm` on SQLite, PostgreSQL and MySQL.
- **Docs:** `docs/delivery.md` covers the retry budget table, the lease sizing rule, cancellation behaviour and lease-lost reporting.
- **Hosts that call the settlement methods directly** must pass the lease from the claimed entry. Nothing is tagged, so no released consumer breaks.
