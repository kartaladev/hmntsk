## Purpose

Defines delivery of task events to the per-task callback address a caller supplied at creation, including what the receiver can rely on to correlate, authenticate and de-duplicate what it receives.

## ADDED Requirements

### Requirement: Only tasks carrying a callback address are delivered

The system SHALL deliver an event to a callback address only when the task that produced it carries one. An event from a task with no callback address SHALL be treated as delivered by this sink without a network call.

#### Scenario: No callback address configured

- **WHEN** an event is relayed for a task created without a callback address
- **THEN** no HTTP request is made and the sink reports success

### Requirement: Reference parameters are echoed verbatim

The system SHALL return the caller's reference parameters unchanged with every delivery — same names, same values, no interpretation, no reordering of meaning, nothing added or dropped.

#### Scenario: Parameters arrive as supplied

- **WHEN** a task created with reference parameters produces a delivered event
- **THEN** the receiver observes exactly those names and values

#### Scenario: Parameters the engine does not understand still arrive

- **WHEN** reference parameters use names the engine has no knowledge of
- **THEN** they are delivered unchanged rather than filtered out

### Requirement: Deliveries carry correlation identifiers

Every delivery SHALL carry a stable identifier for the event and a distinct identifier for this delivery attempt, so a receiver can correlate a notification to the work that caused it and discard a repeat without keeping its own correlation store.

#### Scenario: A receiver de-duplicates a repeat

- **WHEN** the same event is delivered twice
- **THEN** both carry the same event identifier and different delivery identifiers

#### Scenario: Correlation data accompanies the notification

- **WHEN** a delivery arrives for a task created with correlation data
- **THEN** the owner type, owner reference and activity key are readable from the delivery

### Requirement: Deliveries are signed

The system SHALL sign every delivery with a keyed hash over the request body and a timestamp, and SHALL include both the signature and the timestamp with the request, so a receiver can verify the delivery originated from this engine and reject a replayed one.

#### Scenario: A receiver verifies a delivery

- **WHEN** a delivery arrives and the receiver recomputes the signature with the shared key
- **THEN** the computed signature matches the one sent

#### Scenario: A tampered body fails verification

- **WHEN** the body of a delivery is altered in transit
- **THEN** the signature no longer matches what the receiver computes

#### Scenario: A replayed delivery is detectable

- **WHEN** a delivery is captured and re-sent later
- **THEN** its timestamp is unchanged, so a receiver enforcing a freshness window can reject it

### Requirement: Caller-supplied addresses are constrained by policy

Because a callback address is supplied by whoever created the task, the system SHALL evaluate the resolved destination against a policy before connecting, and SHALL refuse delivery when the policy rejects it. The default policy SHALL reject loopback, link-local, private-range and cloud metadata addresses. A host SHALL be able to supply its own policy.

#### Scenario: An internal address is refused by default

- **WHEN** a task is created with a callback address resolving to a loopback, private-range or metadata address, and the default policy is in force
- **THEN** no request is made, the attempt fails permanently, and the reason is recorded

#### Scenario: A host may permit internal destinations

- **WHEN** a host supplies a policy that permits a specific internal destination
- **THEN** delivery to that destination proceeds

#### Scenario: A refused address is not retried

- **WHEN** delivery is refused by policy
- **THEN** the failure is permanent rather than retryable, because retrying cannot change the outcome

### Requirement: Response status determines the outcome

The system SHALL treat a 2xx response as delivered; 408 and 429 as retryable; any other 4xx as a permanent failure; and 5xx responses, timeouts and transport errors as retryable.

#### Scenario: The receiver accepts

- **WHEN** the receiver responds 2xx
- **THEN** the sink reports success and the event is not delivered to that address again

#### Scenario: The receiver rejects the request permanently

- **WHEN** the receiver responds 400
- **THEN** the failure is permanent, so the event is dead-lettered rather than retried

#### Scenario: The receiver is temporarily unavailable

- **WHEN** the receiver responds 503, or the request times out
- **THEN** the failure is retryable and the event is scheduled for another attempt

#### Scenario: The receiver asks for a slower rate

- **WHEN** the receiver responds 429
- **THEN** the failure is retryable

### Requirement: A delivery attempt is bounded in time

The system SHALL apply a timeout to every delivery attempt, so that an unresponsive receiver cannot hold a relay pass open indefinitely.

#### Scenario: An unresponsive receiver does not stall the relay

- **WHEN** a receiver accepts the connection and never responds
- **THEN** the attempt is abandoned at the timeout, is treated as retryable, and the pass continues with the remaining events
