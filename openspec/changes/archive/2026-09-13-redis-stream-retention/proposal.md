## Why

The Redis sink appends every event to one stream and never trims it, so the stream grows until the broker runs out of memory. Retention was left to the host (event-delivery D9), but the sink gives the host no way to apply it on the write path, and `XADD` can trim in the same command at almost no cost. This is the sharpest operational edge left by the event-delivery change, and it is worth closing before the first tag fixes the sink's option surface.

## What Changes

- **Bound the stream by length.** A new option caps the stream at roughly N entries, trimmed as part of each publish.
- **Bound the stream by age.** A new option keeps roughly the last duration's worth of entries, measured from the sink's clock, trimmed as part of each publish. It cannot be combined with the length bound, because the broker accepts only one per command.
- **Trimming is always approximate.** The stream keeps at least the configured bound and may keep more; exact trimming is not offered.
- **Choose how trimming treats consumer groups.** A new trim-mode option selects between: remove entries regardless and leave their references in consumer groups' pending lists; remove entries and their references; or remove only entries every consumer group has acknowledged. Leaving the mode unset sends no mode to the broker at all, so a bound without a mode works on any Redis version.
- **A clock option** for the sink, matching the webhook sink's, so the age cutoff is testable and follows the host's time source.
- **Construction rejects contradictory or meaningless retention settings**: a non-positive bound, both bounds at once, a trim mode with no bound, or an unknown trim mode.
- **Operational constraints are documented** alongside the delivery docs and kept honest by a test: the Redis version a trim mode requires and the misleading error older servers return, that a stale consumer group stops acknowledged-only trimming entirely, that trimming can drop entries a consumer has not read, what approximate means in entry counts, clock skew under the age bound, and how much a single publish can trim from an existing backlog.
- Default behaviour is unchanged: with no retention option the stream is unbounded, exactly as today. Not breaking.

## Capabilities

### New Capabilities

_None._

### Modified Capabilities

- `bus-delivery`: adds requirements for an optional, host-configured bound on the published stream — by length or by age, approximate, with a selectable policy towards consumer groups — for rejecting invalid retention configuration at construction, and for the stream staying unbounded when no bound is configured.

## Impact

- **Code:** `delivery/redis` only — new options, a trim-mode type, a clock, validation in `New`, and the trim arguments on the existing `XADD`. No change to core, the relay, the outbox or the webhook sink.
- **API:** additive exported surface in `delivery/redis`; no existing signature changes.
- **Compatibility:** a bound without a trim mode works on every Redis version the sink supports today. Any trim mode requires Redis 8.2 or newer; on an older server every publish fails, and the sink's existing rule classifies that as retryable.
- **Tests:** construction cases in the existing `TestNew` table; trimming behaviour against real Redis 8.2 containers; a Redis 7 container asserting both that an unmoded bound works and that a moded one fails retryably. The container helper gains a way to set server configuration so approximate trimming is deterministic under test.
- **Docs:** `docs/delivery.md` gains a Redis stream retention section with a drift test in `delivery/redis`; the package doc and README mention retention.
- **Dependencies:** none new. The pinned go-redis already supports trim bounds and modes.
