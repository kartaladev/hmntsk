## Purpose

Defines how durably recorded events get out of the database and to their destinations — how they are claimed without duplicating work across instances, retried when delivery fails, and abandoned when it cannot succeed.

## ADDED Requirements

### Requirement: The host decides when the relay runs

The system SHALL NOT start relay activity on its own. A relay pass SHALL run only when the host invokes it, whether from a long-running goroutine, a scheduled job or a direct call.

#### Scenario: No implicit background work

- **WHEN** a relay is constructed and the host starts nothing
- **THEN** no goroutine, timer or database polling begins

#### Scenario: A single pass is invocable directly

- **WHEN** the host invokes one relay pass
- **THEN** the relay claims and attempts the events due at that moment and returns a result describing what it did

### Requirement: Undelivered events are claimed exclusively by lease

The system SHALL claim events for delivery by taking a time-bounded lease recorded with the event, so that relays running in several application instances do not deliver the same event twice.

#### Scenario: Two relays run simultaneously

- **WHEN** two application instances run a relay pass at the same time over the same undelivered events
- **THEN** each event is attempted by exactly one of them

#### Scenario: A crashed relay does not strand an event

- **WHEN** a relay claims an event and terminates without completing or releasing it
- **THEN** the event becomes claimable again once its lease expires

#### Scenario: Claiming requires no lock primitive

- **WHEN** a relay pass runs against a database offering no row-level locking
- **THEN** exclusive claiming still holds and each event is attempted exactly once per pass

### Requirement: Events are attempted oldest first

The system SHALL attempt events in the order they occurred, so that a backlog drains in the order it accumulated.

#### Scenario: Backlog drains in order

- **WHEN** a relay pass runs against several undelivered events recorded at different times
- **THEN** they are attempted oldest first

### Requirement: Failed delivery is retried with increasing delay

A retryable failure SHALL schedule the event for another attempt after a delay that grows with the number of attempts already made, with a random component so that many events failing together do not retry in lockstep.

#### Scenario: A retryable failure is rescheduled, not dropped

- **WHEN** a sink reports a retryable failure
- **THEN** the event remains undelivered, its attempt count increases, and it is not attempted again before its next-attempt time

#### Scenario: The delay grows between attempts

- **WHEN** an event fails repeatedly
- **THEN** each successive wait is longer than the one before it, up to a configured ceiling

#### Scenario: An event is not attempted before it is due

- **WHEN** a relay pass runs while an event's next-attempt time is in the future
- **THEN** that event is not claimed by the pass

### Requirement: An event that cannot be delivered is dead-lettered

When an event exhausts its attempt limit, or a sink reports a permanent failure, the system SHALL stop attempting it, mark it dead-lettered, and retain it with the last error recorded.

#### Scenario: Attempts are exhausted

- **WHEN** an event reaches its configured maximum attempts and fails again
- **THEN** it is marked dead-lettered, no further relay pass attempts it, and the last error is readable

#### Scenario: A permanent failure skips the remaining attempts

- **WHEN** a sink reports a failure as permanent on the first attempt
- **THEN** the event is dead-lettered immediately rather than retried

#### Scenario: Dead letters remain inspectable

- **WHEN** an event has been dead-lettered
- **THEN** it remains stored, distinguishable from both delivered and pending events, and carries its attempt count and last error

### Requirement: Delivery is tracked per sink

When more than one sink is configured, the system SHALL record delivery success for each sink independently, so that a failure in one does not cause redelivery to another that already succeeded.

#### Scenario: One sink fails, the other has already succeeded

- **WHEN** an event is delivered to one sink and the other reports a retryable failure
- **THEN** the retry attempts only the sink that failed, and the successful sink receives the event once

#### Scenario: An event is complete only when every sink has taken it

- **WHEN** an event has been accepted by some but not all configured sinks
- **THEN** it is not marked delivered, and remains claimable once its next-attempt time passes

### Requirement: Consumers must tolerate duplicates

The system SHALL guarantee at-least-once delivery and SHALL NOT guarantee exactly-once. Every delivery SHALL carry a stable identifier for the event and a distinct identifier for the delivery attempt, so that a consumer can de-duplicate.

#### Scenario: A redelivered event carries the same event identifier

- **WHEN** an event is delivered twice because a crash lost the record of the first success
- **THEN** both deliveries carry the same event identifier and different delivery identifiers

### Requirement: A failing sink does not stop the pass

An error from one event's delivery SHALL NOT abandon the remaining events in the pass, and SHALL be reported to the host rather than silently swallowed.

#### Scenario: One event fails, the rest still go

- **WHEN** delivery of one event fails during a pass over several events
- **THEN** the remaining events are still attempted and the failure is reported through the configured error handler

### Requirement: Relay behaviour is identical across every supported driver and dialect

Claiming, retry scheduling and dead-lettering are storage behaviour, and SHALL behave identically across every supported combination of driver and dialect, asserted by one shared suite rather than per-adapter tests.

#### Scenario: One suite, every combination

- **WHEN** the shared relay suite is executed against each supported driver and dialect combination
- **THEN** every case passes with identical observable results
