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
