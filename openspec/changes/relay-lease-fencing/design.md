## Context

See proposal.md, section "Why". This is the current state, confirmed with gopls and by the scratch reproductions listed in tasks.md.

**Claiming and settlement:**
- **Claiming is fenced.** `Builder.ClaimEvent` (`store/sqlcore/query.go`) repeats the due predicate in the `UPDATE`, and `memstore.ClaimDueEvents` does the same check under its transaction. The claimed entry comes back with `LockedBy = claim.Owner` and `LockedUntil = claim.Now + claim.Duration`. SQL normalises that deadline. Memstore normalises `Now` only, and this change aligns the two.
- **Settlement is not fenced.** `Builder.RecordAttempt`, `MarkAccepted` and `MarkDeadLettered` end in `WHERE id = ?`, and memstore's `editOutbox` edits by ID. `AttemptRecord`, `Acceptance` and `DeadLetter` (`outbox.go`) have no owner or lease field.
- **Zero rows affected.** The `settleOutbox` helpers in `store/sql`, `store/pgx` and `store/gorm` treat zero rows affected as "look the row up and report `NotFound` if it is gone", because MySQL reports an unchanged row as zero affected.
- **Partial acceptances un-publish.** `MarkAccepted` always writes `published_at`, including `NULL` for a partial acceptance. That is how a stale partial acceptance un-publishes an event.

**The pass:**
- **One instant for the whole pass.** `Relay.Relay` reads the clock once (`now`) and uses it for the claim, every `nextAttemptAt`, and the published stamp. It then ranges over the claimed entries with the caller's `ctx`, checking neither the lease nor `ctx.Err()`, and settles on that same `ctx`.
- **The schedule overflows.** `nextAttemptAt` computes `math.Ldexp(float64(base), n)`, caps it at the ceiling, multiplies by the jitter factor and converts to `time.Duration`. When the float is at or above 2^63 ns, that conversion is implementation-defined, and on amd64 it yields `MinInt64`.
- **Options are silently ignored.** Every option ignores a non-positive, nil or empty value, and `WithBackoff` silently raises a ceiling that is below the base.

**Constraints:**
- Nothing is tagged, so changing a port or a default is free now. It is recorded here under library-design rule 7.
- SQLite has no row locks. Every fence has to be a conditional `UPDATE`.

## Goals / Non-Goals

**Goals:**
- A settlement lands only for the claim that took the event, on every store.
- A pass never works past its lease or its context, and it never strands what it claimed.
- A retry schedule that is correct for any configuration the constructor accepts.
- Constructor validation that turns every meaningless option into a `ConfigurationError`.

**Non-Goals:**
- Exactly-once delivery. At-least-once stays the contract, and the fence only removes duplicates that no crash caused.
- Lease renewal (heartbeating) during a pass. The lease stays a fixed budget; see Decision 3.
- `NewSweeper` option validation, which is also audit item C9. It belongs to the sweeper and is out of scope here.
- Replay tooling for dead letters.
- Sink hardening (audit section E) and store robustness (section F).

## Decisions

### 1. The fence is the claim's (owner, deadline) pair, carried on every settlement

**The new type.** `hmntsk.OutboxLease{Owner string; Until time.Time}` is added.
- `AttemptRecord`, `Acceptance` and `DeadLetter` each gain `Lease OutboxLease`.
- The relay fills it from the claimed entry's `LockedBy` and `*LockedUntil`.

**The stores.**
- SQL adds `AND locked_by = ? AND locked_until = ?` to each settlement `UPDATE`.
- Memstore compares the row's lease fields before editing.

**When the fence does not match.** The store changes nothing and returns `*hmntsk.LeaseLostError{EventID, Owner}`. That error unwraps to a new sentinel, `ErrLeaseLost`, which is declared as `fmt.Errorf("%w: lease lost", ErrConflict)` and so matches `ErrConflict` too.

**Telling a lost lease from a missing row.** In SQL, zero rows affected is followed by the existing lookup:
- no row: `OutboxNotFoundError`;
- a row is there: `LeaseLostError`.

MySQL's zero-rows quirk cannot hide a successful fenced write: every settlement also releases the lease (`locked_by` goes from the owner to `NULL`), so a matching row always changes.

**Why (owner, deadline) is unique per claim.** A second claim of the same row can happen only after the first lease has expired (`until ≤ now₂`), so its deadline `now₂ + d` is strictly later, because `d > 0` is now enforced (Decision 7). Owner and deadline together are therefore unique per claim. That holds even when two instances are misconfigured with the same `WithRelayOwner`. It needs no new column, so there is no migration.

**Expired but not reclaimed.** A lease that has expired but has not been superseded still matches, and the settlement lands. Nobody else holds the event, and recording the real outcome prevents a needless redelivery.

**A settlement without a lease.** A zero `Lease` (empty owner or zero `Until`) is refused by `Service.RecordDeliveryAttempt`, `MarkEventAccepted` and `MarkEventDeadLettered` with a `ValidationError`, before the store is touched. The stores also refuse it, as defence in depth, because a store is a public port.

**Settling twice.** Once the first settlement has released the lease, a second identical settlement no longer matches and is reported as lost. That is the correct answer: the event is no longer this claim's. The "recording twice is harmless" godoc on `Acceptance` is reworded. Idempotency is across crash and reclaim, which the new claim's lease covers, not repeated writes under one lease.

- **Default:** always fenced.
- **Override:** none, and deliberately so (library-design rule 4). An unfenced settlement is the defect. A host that drives its own loop gets the lease from `ClaimDueEvents` and passes it back. A host that implements its own `OutboxStore` must fence, and the `storetest` cases enforce it.
- **Alternatives considered:**
  - *A random lease-token column.* It is equally strong, but needs a migration on four dialects and a schema-verification change for no extra guarantee.
  - *Owner only.* Rejected: two passes of one relay, or two instances with the same configured owner, would satisfy each other's fence.
  - *Also requiring `locked_until > now`.* Rejected: it throws away a correct outcome that nobody contests.

### 2. Settled outcomes are monotonic in the store

`MarkAccepted` writes `published_at` only when `PublishedAt` is set. A partial acceptance leaves it untouched.

The fence already stops a stale relay from reaching a delivered or dead-lettered row, because settlement released the lease. This rule is the second line, so that no write through the port can turn "delivered" back into "pending".

- **Default:** always.
- **Override:** none. Un-publishing an event is not an operation the port offers.

### 3. The pass stays inside its lease, and the sink context is bounded by it

**The deadline.** After claiming, the relay computes `deadline = claimed lease Until`, taken from the entries, which all share one. Before each event, and before each sink within an event, it reads `engine.Clock().Now()`. When the clock is at or past the deadline it stops offering.

**What happens to the events it stopped at:**
- An event that was never offered is released (Decision 5).
- An event where some sinks were offered is settled with the acceptances it has. The sinks not offered are not failures:
  - If none of the offered sinks failed, it is a partial acceptance, and the next attempt is due at the settlement instant, so the remaining sinks go next pass.
  - If some sink failed, it is scheduled normally.

**The sink context.** Each sink gets `context.WithTimeout(ctx, deadline − clock.Now())`. The remaining time is measured by the engine clock and turned into a wall-clock timeout, because the context package only knows wall time. A sink that runs out of time returns whatever it classifies that as, usually retryable. That is recorded as a genuine attempt, because the destination may have received it.

- **Default:** the lease bounds both the pass and each sink.
- **Override:** `WithRelayLease`. A host that needs longer deliveries lengthens the lease, which is the only knob that keeps delivery exclusive (rule 4). There is deliberately no option that lets a sink outlive the lease: that is the R2 duplication.
- **The default contradiction is removed, not re-tuned.** 50 × 10 s is more than 5 minutes, so under a slow receiver a default pass now stops at about event 30 and releases the rest to the next pass. The batch and lease defaults stay as they are. `docs/delivery.md` states the sizing rule: lease ≥ batch × sinks × the slowest sink's timeout, or accept a partial pass.
- **Alternatives considered:**
  - *Lease heartbeat or renewal.* It needs a new port method and a timer, and it only moves the problem, because a stuck sink renews forever.
  - *Only a between-events check without a sink deadline (R2a only).* Rejected: a single hung delivery still outlives the lease. R2b is adopted, and this decision resolves the judgement call.

### 4. Cancellation: stop offering, settle what arrived, release the rest

Before each event and sink, the relay checks `ctx.Err()`, and once it is set it stops offering. Outcomes already returned by sinks are settled on `context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)`. Unstarted events are released on the same detached, bounded context. `Relay.Relay` then returns `(result, ctx.Err())`, and `Run` already returns on `ctx.Err()`.

- **Default:** `DefaultSettleTimeout = 10 * time.Second`.
- **Override:** `WithSettleTimeout(d)`. A value of 0 or less is a `ConfigurationError`.
- **Why a timeout:** a host shutting down cannot wait forever on a hung database, but it should get the chance to record outcomes that a destination has already acted on. Losing them turns a clean shutdown into guaranteed duplicates.
- **Changed contract:** "only claiming returns an error" becomes "claiming, or the pass was cut short by its context". The godoc for `Relay.Relay` is updated.

### 5. Releasing unattempted events is a fenced port method

The new port method:

```go
type LeaseRelease struct { EventIDs []string; Lease OutboxLease }
// OutboxStore
ReleaseLeases(ctx context.Context, release LeaseRelease) error
```

The service wrapper is `Service.ReleaseEvents(ctx, LeaseRelease)`. It clears `locked_by` and `locked_until` for the listed rows that still carry the lease, and it leaves the attempt count, next attempt and last error untouched.

Rows that no longer carry the lease are skipped silently: releasing something you do not hold is a no-op, not a conflict. Result accounting still has to be exact, so the store returns no per-row outcome, and the relay counts every event it asked to release as `Released`. A stale relay's request changes nothing, and a stale relay has already had its settled events counted as unsettled.

`Result.Released` is added, and `Claimed = Delivered + Retried + DeadLettered + Unsettled + Released`.

- **Default:** the relay releases every claimed event it did not start, whether the pass stopped at the deadline or on cancellation.
- **Override:** none needed. A host with its own loop calls `Service.ReleaseEvents` or lets leases expire.
- **Alternative considered:** reusing `RecordAttempt` with the entry's unchanged values. Rejected: it rewrites `last_error` and `next_attempt_at` from possibly stale reads, and it reads as an attempt that never happened.

### 6. The schedule is measured from the failure, clamped, and hard-capped by the ceiling

`settle` reads `NormalizeTime(clock.Now())` after the event's fan-out and uses that instant for its `nextAttemptAt` and its `PublishedAt`. The pass-start instant remains only the claim's `Now`.

The schedule is computed in float64:
- `d = min(base × 2^n, ceiling)`;
- `lo = d × (1 − j)` and `hi = min(d × (1 + j), ceiling)`;
- `wait = lo + (hi − lo) × r`;
- the result is clamped to `[0, maxDuration]` before it is converted, where `maxDuration` is `math.MaxInt64` ns expressed as a float compared with `>=`.

`now.Add(wait)` is then safe, because any wait the constructor admits is at most `MaxInt64` ns.

- **Default:** as described. Below the ceiling the expected wait is unchanged. At the ceiling the draw falls in `[0.8 × ceiling, ceiling]`, so a backlog stuck at the ceiling still spreads out rather than colliding at one value.
- **Override:** `WithBackoff`, `WithJitter` (0 turns jitter off) and `WithJitterSource`.
- **Jitter versus ceiling (the R9 observation, decided):** the ceiling becomes a hard bound, matching the spec's "up to a configured ceiling".
  - *Alternative: jitter first, then clamp.* Rejected: about half of all draws would land exactly on the ceiling, which brings back lockstep.
  - *Alternative: keep the jitter symmetric above the ceiling.* Rejected: it contradicts the spec wording.
- **Trade-off:** events that failed together no longer share one schedule instant. Jitter already spread them, so nothing depended on it.

### 7. Constructor validation, and the retry-budget default

**Validation.** Each option records its own error on the relay instead of silently ignoring a value, and `NewRelay` returns the joined errors as one `*hmntsk.ConfigurationError` that names every bad option. After all options have been applied, the constructor checks `ceiling ≥ base`, so that option order does not matter. The rejected values are the ones listed in the spec. `WithJitter(0)` stays legal, meaning jitter is off. Godoc wording that says a value is "ignored" is replaced by "is a configuration error".

**Retry budget (the R4 observation, decided).** `DefaultMaxAttempts` goes from 5 to 12.
- **Why:** dead letters have no replay tooling in the engine. A receiver deployment or a short incident that outlasts 7½ minutes currently turns into manual operator work, and the documented 1-hour ceiling is dead code under the defaults. With 12 attempts the budget covers roughly 4½ to 5 hours and actually uses the ceiling.
- **Default:** 12, recorded here as a default change before the first tag.
- **Override:** `WithMaxAttempts` and `WithBackoff`.
- **Alternative considered:** keep 5 and only document it. Rejected: the safe default is the one that does not silently hand routine outages to a manual process. `docs/delivery.md` gets the full schedule table either way.

## Risks / Trade-offs

- **[Risk]** Timestamp equality in the fence could miss because of precision differences between what the claim wrote and what the relay passes back.
  - **Mitigation:** the relay passes back exactly the `LockedUntil` that the store returned from the claim. Memstore normalises `until` as SQL does. A `storetest` case claims and then settles on every dialect, including MySQL `DATETIME(6)` and SQLite text timestamps.
- **[Risk]** The sink deadline is wall-clock while the lease is engine-clock. A host with a skewed or injected clock could get a surprising timeout.
  - **Mitigation:** the remaining time is computed on the engine clock and only then turned into a timeout. Tests drive a hand clock and assert the deadline value, not elapsed time.
- **[Trade-off]** A pass that runs out of lease reports fewer events handled, so throughput under a slow receiver drops to what the lease allows.
  - **Mitigation:** this is the correct behaviour, and it is documented along with the sizing rule.
- **[Trade-off]** `Relay.Relay` now returns an error on cancellation. A host that treated any error as "claim failed" will log a shutdown.
  - **Mitigation:** the error is `ctx.Err()`, which is matchable, and `Run` already returns it.
- **[Risk]** A higher default attempt count delays dead-letter visibility for permanently broken receivers.
  - **Mitigation:** a `4xx` or permanent outcome still dead-letters immediately, and only retryable failures consume the budget.

## Migration Plan

Nothing is released. One pull request changes the port, the four stores, the relay and the suites together. Hosts that call the settlement methods themselves (none are known outside `examples`) pass `entry.LockedBy` and `*entry.LockedUntil` as `Lease`. Rollback is a revert, because there is no schema change.

## Open Questions

None that change the specs or tasks. These assumptions were made instead of asking:
- R2b: bound the sink context by the lease, with no opt-out.
- R4: raise the default to 12 attempts.
- R9: hard ceiling, with the draw window narrowed at the ceiling.
- Cancellation returns `ctx.Err()`.
- The settlement timeout defaults to 10 seconds.
