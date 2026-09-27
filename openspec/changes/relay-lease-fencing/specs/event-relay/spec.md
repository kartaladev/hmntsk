## MODIFIED Requirements

### Requirement: Undelivered events are claimed exclusively by lease

The system SHALL claim events for delivery by taking a time-bounded lease recorded with the event, so that relays running in several application instances do not deliver the same event twice. The lease SHALL identify both the relay that took it and the claim itself, so that a later claim of the same event, including one by a relay with the same owner name, is distinguishable from an earlier one.

#### Scenario: Two relays run simultaneously

- **WHEN** two application instances run a relay pass at the same time over the same undelivered events
- **THEN** each event is attempted by exactly one of them

#### Scenario: A crashed relay does not strand an event

- **WHEN** a relay claims an event and terminates without completing or releasing it
- **THEN** the event becomes claimable again once its lease expires

#### Scenario: Claiming requires no lock primitive

- **WHEN** a relay pass runs against a database offering no row-level locking
- **THEN** exclusive claiming still holds and each event is attempted exactly once per pass

#### Scenario: A reclaimed event stays with the relay that reclaimed it

- **WHEN** relay A's lease on an event expires, relay B claims the event and is still delivering it, and relay A then tries to settle it
- **THEN** relay B's lease is still held, and a third relay's pass does not claim the event

### Requirement: Failed delivery is retried with increasing delay

A retryable failure SHALL schedule the event for another attempt after a delay that grows with the number of attempts already made, with a random component so that many events failing together do not retry in lockstep.

- **Measurement:** the delay SHALL be measured from the moment that event's attempt finished, not from the start of the pass.
- **Ceiling:** no wait, random component included, SHALL exceed the configured ceiling.
- **Range:** a next-attempt time SHALL NOT fall before the failure, whatever base and ceiling are configured.
- **Default budget:** base 30 seconds, ceiling 1 hour, jitter 0.2, maximum 12 attempts.

The consumer SHALL be able to replace each of these values.

#### Scenario: A retryable failure is rescheduled, not dropped

- **WHEN** a sink reports a retryable failure
- **THEN** the event remains undelivered, its attempt count increases, and it is not attempted again before its next-attempt time

#### Scenario: The delay grows between attempts

- **WHEN** an event fails repeatedly
- **THEN** each successive wait is longer than the one before it, up to a configured ceiling

#### Scenario: An event is not attempted before it is due

- **WHEN** a relay pass runs while an event's next-attempt time is in the future
- **THEN** that event is not claimed by the pass

#### Scenario: A failure late in a long pass still waits the full backoff

- **WHEN** an earlier event in the same pass takes two minutes to deliver and a later event then fails retryably
- **THEN** the later event's next-attempt time is at least the base delay after the moment it failed, and a pass run at that same moment does not claim it

#### Scenario: The largest ceiling never schedules into the past

- **WHEN** the backoff base and ceiling are both the largest representable duration, or the ceiling is the largest representable duration and an event has failed enough times for the doubled base to exceed it
- **THEN** every next-attempt time is after the failure that scheduled it

#### Scenario: Jitter never pushes a wait past the ceiling

- **WHEN** base and ceiling are both one minute, jitter is 0.2, and the random draw is at the top of its range
- **THEN** the event's wait is at most one minute

#### Scenario: The default budget tolerates an outage of hours

- **WHEN** a relay with no retry options configured delivers to a destination that refuses every attempt retryably, and the random draw is fixed at its midpoint
- **THEN** the event is dead-lettered on its 12th attempt, 4 hours 39 minutes 30 seconds after the first failure (waits of 30 s, 1, 2, 4, 8, 16 and 32 minutes, then four waits of 54 minutes drawn below the one-hour ceiling), and no wait exceeds one hour

#### Scenario: The consumer replaces the retry budget

- **WHEN** a relay is configured with 3 maximum attempts, a 1-second base, a 4-second ceiling and jitter switched off, and its sink fails retryably every time
- **THEN** the waits are 1 second and then 2 seconds, and the event is dead-lettered on its third attempt

## ADDED Requirements

### Requirement: Settlement is fenced by the lease that claimed the event

Recording an attempt, an acceptance or a dead letter SHALL take effect only while the event still carries the exact lease under which it was claimed. The lease is the owner and deadline that the claim returned.

- **Refused settlement:** when that lease has been superseded by another claim, or the event was already settled, the store SHALL change nothing. It SHALL report an error that is distinguishable as a lost lease and that also matches the engine's conflict error.
- **Relay reporting:** the relay SHALL count such an event as unsettled and report the error through its error handler.
- **Expired, unclaimed lease:** a settlement whose lease has expired but has not been superseded SHALL still take effect.
- **Missing lease:** a settlement that carries no lease SHALL be refused before the store is written.
- **Published time:** a partial acceptance SHALL NOT clear an event's published time.

This behaviour has no opt-out. An unfenced settlement is exactly the defect this requirement removes.

#### Scenario: A stale partial acceptance does not undo the peer's delivery

- **WHEN** relay A's lease expires while it is delivering an event to two sinks, relay B reclaims the event and both sinks accept it, and relay A then records that only one sink accepted it
- **THEN** the event is still delivered, its accepted sinks are still both sinks, and relay A's settlement is reported as a lost lease and counted as unsettled

#### Scenario: A stale retry does not resurrect the peer's dead letter

- **WHEN** relay B reclaims an event and dead-letters it after a permanent failure, and relay A, whose lease had expired, then records a retryable failure for it
- **THEN** the event is still dead-lettered

#### Scenario: A stale retry does not regress the attempt count

- **WHEN** relay B reclaims an event and records two attempts, and relay A, whose lease had expired, then records its own first attempt
- **THEN** the event's attempt count is still two

#### Scenario: A stale settlement does not release the peer's live lease

- **WHEN** relay A's lease expires, relay B claims the event and is mid-delivery, and relay A then settles the event
- **THEN** the event is still leased by relay B, and a third relay's pass claims nothing

#### Scenario: A late settlement against an expired but unclaimed lease still lands

- **WHEN** a relay's lease on an event has expired, no other relay has claimed the event, and the relay then records that every sink accepted it
- **THEN** the event is marked delivered and the settlement is counted as delivered

#### Scenario: Store-level fencing holds on every adapter

- **WHEN** relay-a claims an event, its lease expires, relay-b claims the event, and relay-a's attempt record is then written directly through the store port
- **THEN** the write returns an error matching the lost-lease error and the conflict error, the event is still leased by relay-b with its attempt count unchanged, and a claim by relay-c returns nothing

#### Scenario: A settlement without a lease is refused

- **WHEN** a host that drives its own relay loop records an attempt with no lease
- **THEN** the engine returns a validation error and the event is unchanged

### Requirement: A pass stays within its lease

A relay pass SHALL NOT offer an event, or a further sink for an event, once the lease it claimed under has expired by the engine's clock.

- **Events not started** SHALL be released without charging an attempt.
- **Partly offered events:** for an event that some sinks were offered and others were not, the sinks not offered SHALL be treated as not yet attempted, not as failures.
- **Sink deadline:** by default the context each sink receives SHALL carry a deadline no later than the lease deadline. The consumer extends how long a delivery may run by lengthening the lease, the one knob that keeps delivery inside the lease.

#### Scenario: The default configuration does not deliver an event twice under a slow receiver

- **WHEN** relay A runs with the default lease and batch against 50 due events, each delivery takes the webhook sink's default timeout, and relay B runs a pass as soon as relay A's lease has expired
- **THEN** no event is delivered by both relays, and the events relay A did not reach are released rather than left leased

#### Scenario: A sink's context is bounded by the lease

- **WHEN** a relay with a one-minute lease offers an event to a sink
- **THEN** the context the sink receives carries a deadline no later than the lease deadline

#### Scenario: The consumer gives deliveries more time by lengthening the lease

- **WHEN** a relay is configured with a 30-minute lease
- **THEN** the context each sink receives carries a deadline up to 30 minutes after the claim, and events keep being offered until then

### Requirement: A cancelled pass stops offering and settles what it has

When the pass context is cancelled, the relay SHALL do three things:

- **Stop offering:** offer no further event or sink.
- **Record received outcomes:** record every outcome a sink has already returned. It SHALL do so on a context that is not cancelled, bounded by a settlement timeout. The default timeout is 10 seconds, and the consumer can replace it.
- **Release unstarted events:** release the events it claimed but did not start, without charging an attempt.

The pass SHALL return its result together with the cancellation error. The result's delivered, retried, dead-lettered, unsettled and released counts SHALL together account for every claimed event.

#### Scenario: Nothing is offered after cancellation

- **WHEN** the host cancels the pass context while a sink is accepting the first of two claimed events
- **THEN** the second event is not offered to any sink

#### Scenario: An outcome received before cancellation is recorded

- **WHEN** the host cancels the pass context while a sink is accepting an event, and the sink then reports it delivered
- **THEN** the event is recorded as delivered and is not offered again

#### Scenario: An unattempted event is released, not stranded

- **WHEN** the host cancels the pass context before the second of two claimed events is offered
- **THEN** the second event is not leased once the pass returns, its attempt count is unchanged, and the pass reports it as released

#### Scenario: The consumer replaces the settlement timeout

- **WHEN** a relay configured with a 2-second settlement timeout is cancelled and the store takes longer than that to record an outcome
- **THEN** the relay abandons the settlement after 2 seconds, counts the event as unsettled and reports the timeout through its error handler

### Requirement: Relay configuration is validated at construction

Constructing a relay SHALL fail with a configuration error, before any pass runs, when an option is given a value that is meaningless or contradictory. The failing values are:

- a lease, batch size, maximum attempts, backoff base, backoff ceiling or settlement timeout that is not positive;
- a backoff ceiling below the base;
- a jitter fraction below 0 or above 1;
- a nil sink;
- an empty owner name;
- a nil jitter source or a nil error handler.

An option that is not given SHALL leave its documented default in place.

#### Scenario: Defaults need no configuration

- **WHEN** a relay is constructed with one named sink and no other option
- **THEN** construction succeeds with the documented defaults

#### Scenario: A meaningless option is refused

- **WHEN** a relay is constructed with any one of: a lease of 0 or −1 second, a batch of 0, 0 maximum attempts, a backoff of 0 and 0, a backoff base of −1 second, a jitter of −0.5, or a nil sink beside a real one
- **THEN** construction fails with an error matching the configuration error

#### Scenario: A contradictory backoff is refused

- **WHEN** a relay is constructed with a backoff base of one hour and a ceiling of one minute
- **THEN** construction fails with an error matching the configuration error, rather than raising the ceiling silently
