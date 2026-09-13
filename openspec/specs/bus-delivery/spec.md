# bus-delivery Specification

## Purpose

Defines publication of task events to a message bus as a relay sink, for internal consumers that subscribe to a stream rather than exposing an endpoint of their own.

## Requirements

### Requirement: Every event is published, regardless of callback address

The system SHALL publish every relayed event to the bus, whether or not the task carries a callback address. The bus sink serves internal consumers and is independent of the per-task callback mechanism.

#### Scenario: A task with no callback address still reaches the bus

- **WHEN** an event is relayed for a task created without a callback address
- **THEN** it is published to the bus

#### Scenario: Both sinks receive the same event

- **WHEN** an event is relayed for a task that does carry a callback address, with both sinks configured
- **THEN** the receiver at that address and the bus consumer each receive it once

### Requirement: Published events carry their identity and correlation

A published event SHALL carry its event identifier, its task identifier, its task type, its event type, the time it occurred, and the task's correlation data, so that a consumer can filter and route without reading the database.

#### Scenario: A consumer routes without a database lookup

- **WHEN** a consumer receives a published event
- **THEN** the owner type, owner reference, activity key and task type are all readable from the message itself

#### Scenario: A consumer de-duplicates a repeat

- **WHEN** the same event is published twice because a crash lost the record of the first success
- **THEN** both messages carry the same event identifier

### Requirement: Publication failure is retryable

The system SHALL treat a broker that is unreachable, timing out or rejecting a write as a retryable failure, so the event is attempted again rather than lost or dead-lettered on a transient outage.

#### Scenario: The broker is unavailable

- **WHEN** the broker cannot be reached during a relay pass
- **THEN** the failure is retryable, the event remains undelivered, and it is attempted again after the backoff

#### Scenario: A broker outage does not dead-letter the backlog

- **WHEN** the broker is unavailable for several consecutive passes but returns before the attempt limit is reached
- **THEN** the pending events are published on the next successful pass

### Requirement: The engine publishes but does not consume

The system SHALL act only as a producer. It SHALL NOT create consumer groups, track consumer offsets, or provide consumer-side helpers; what a host does with the stream is the host's concern.

#### Scenario: No consumer machinery is created

- **WHEN** the bus sink publishes events
- **THEN** the engine creates no consumer group and tracks no consumer position

### Requirement: A delivery attempt is bounded in time

The system SHALL apply a timeout to every publish attempt, so that an unresponsive broker cannot hold a relay pass open indefinitely.

#### Scenario: An unresponsive broker does not stall the relay

- **WHEN** the broker accepts the connection and never acknowledges the write
- **THEN** the attempt is abandoned at the timeout, treated as retryable, and the pass continues

### Requirement: The stream is unbounded unless the host bounds it

The system SHALL NOT remove any entry from the published stream unless the host has configured a retention bound. With no bound configured, publishing SHALL behave exactly as it did before retention existed, including on broker versions that predate trim modes.

#### Scenario: No bound configured

- **WHEN** the bus sink is constructed without a retention bound and publishes many events
- **THEN** every published entry remains on the stream

#### Scenario: An unconfigured sink works on an older broker

- **WHEN** a sink with no retention bound publishes to a broker older than the first version supporting trim modes
- **THEN** the event is delivered

### Requirement: The host can bound the stream by length

The system SHALL allow the host to configure a maximum stream length. Each publish SHALL trim the stream towards that length. Trimming SHALL be approximate: after a publish the stream SHALL hold at least the configured number of entries whenever at least that many have been published, and MAY hold more.

#### Scenario: A length bound trims the oldest entries

- **WHEN** a length bound of N is configured and more than N events are published
- **THEN** the oldest entries are removed and the most recent N remain on the stream

#### Scenario: Trimming never goes below the bound

- **WHEN** a length bound of N is configured and events are published
- **THEN** the stream never holds fewer than N entries once N have been published

#### Scenario: A length bound without a trim mode works on an older broker

- **WHEN** a length bound is configured without a trim mode and the broker predates trim modes
- **THEN** the event is delivered and the stream is trimmed

### Requirement: The host can bound the stream by age

The system SHALL allow the host to configure a maximum entry age. Each publish SHALL trim entries whose stream identifier is older than the sink's current time minus that age. Trimming SHALL be approximate: no entry newer than the cutoff SHALL be removed, and entries older than the cutoff MAY remain.

#### Scenario: An age bound trims entries older than the cutoff

- **WHEN** an age bound is configured and the stream holds entries older and newer than the cutoff
- **THEN** a publish removes the older entries and keeps the newer ones

#### Scenario: An age bound follows the sink's clock

- **WHEN** the sink is given a clock and an age bound
- **THEN** the cutoff is computed from that clock rather than from the host's wall time

### Requirement: The host chooses how trimming treats consumer groups

The system SHALL allow the host to select a trim mode when a bound is configured, with these meanings:

- **keep references**: trimmed entries are removed even if a consumer group has not read or acknowledged them, and their identifiers remain in those groups' pending lists with no content;
- **delete references**: trimmed entries are removed even if unread or unacknowledged, and their identifiers are removed from every consumer group's pending list;
- **acknowledged only**: trimming removes entries from the oldest end only up to the first entry that some consumer group has not acknowledged.

When no trim mode is selected, the system SHALL send no mode to the broker, leaving the broker's own default in effect.

#### Scenario: Keep references leaves dangling pending entries

- **WHEN** a consumer group has read but not acknowledged entries, and a publish under keep-references trims them
- **THEN** the entries are gone from the stream and their identifiers remain pending in the group

#### Scenario: Delete references clears pending entries

- **WHEN** a consumer group has read but not acknowledged entries, and a publish under delete-references trims them
- **THEN** the entries are gone from the stream and from the group's pending list

#### Scenario: Acknowledged-only stops at the first unacknowledged entry

- **WHEN** a consumer group has acknowledged the oldest entries but not the ones after them, and a publish under acknowledged-only would otherwise trim past them
- **THEN** only the acknowledged entries are removed

#### Scenario: A consumer group that never reads stops acknowledged-only trimming

- **WHEN** a consumer group exists that has read nothing, and events are published under acknowledged-only with a bound
- **THEN** no entry is removed and the stream grows past the bound

#### Scenario: Acknowledged-only with no consumer groups trims normally

- **WHEN** no consumer group exists and events are published under acknowledged-only with a bound
- **THEN** the stream is trimmed to the bound

#### Scenario: A trim mode on a broker that does not support it

- **WHEN** a trim mode is configured and the broker predates trim modes
- **THEN** every publish fails as a retryable publication failure, and the event remains undelivered

### Requirement: Invalid retention configuration is rejected at construction

The system SHALL reject, when the bus sink is constructed and before any event is published, a retention configuration that is contradictory or meaningless: a length bound that is not positive, an age bound that is not positive, a length bound and an age bound together, a trim mode with no bound, or a trim mode that is not one of the defined modes.

#### Scenario: Both bounds configured

- **WHEN** the sink is constructed with both a length bound and an age bound
- **THEN** construction fails with a configuration error and no sink is returned

#### Scenario: A trim mode without a bound

- **WHEN** the sink is constructed with a trim mode and no bound
- **THEN** construction fails with a configuration error and no sink is returned

#### Scenario: A non-positive bound

- **WHEN** the sink is constructed with a length bound or an age bound of zero or less
- **THEN** construction fails with a configuration error and no sink is returned

#### Scenario: An undefined trim mode

- **WHEN** the sink is constructed with a trim mode that is not one of the defined modes
- **THEN** construction fails with a configuration error and no sink is returned
