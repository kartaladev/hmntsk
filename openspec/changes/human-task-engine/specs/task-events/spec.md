## Purpose

Defines what the engine tells the outside world about its own state changes, and the delivery guarantees around it, so consumers can react to human tasks without the engine knowing anything about them.

## ADDED Requirements

### Requirement: The engine publishes, it does not invoke

The system SHALL communicate state changes only by producing events. It SHALL NOT call host business logic, workflow engines or use cases directly.

#### Scenario: Adding a consumer requires no engine change

- **WHEN** a host adds a second consumer reacting to task completion
- **THEN** no engine code or configuration internal to the engine changes

#### Scenario: Events carry correlation, not caller types

- **WHEN** a consumer receives an event
- **THEN** it can identify the originating owner type, unit of work and activity from the event's correlation data, and the event names no caller-specific type

### Requirement: The event catalogue is closed and complete

The system SHALL produce exactly one event per accepted lifecycle transition, drawn from a closed, documented set covering creation, claim, release, start, delegation, completion, failure, suspension, resumption, escalation, cancellation, obsolescence and error.

#### Scenario: Every accepted transition emits its event

- **WHEN** a lifecycle operation succeeds
- **THEN** exactly one event corresponding to that transition is produced

#### Scenario: Refused operations emit nothing

- **WHEN** a lifecycle operation is refused for any reason
- **THEN** no event is produced

### Requirement: Progress saves do not produce events

Saving partial progress SHALL NOT produce an event, because no consumer is blocked on it and the owning unit of work remains suspended either way.

#### Scenario: Repeated saves are silent

- **WHEN** the assignee saves progress several times
- **THEN** no events are produced by those saves

### Requirement: Events are durable before they are delivered

Events SHALL be recorded durably within the same transaction as the state change that produced them, and delivered to consumers only after that transaction commits.

#### Scenario: Consumers never observe uncommitted state

- **WHEN** a consumer receives a completion event
- **THEN** the corresponding completed task is readable from the database

#### Scenario: Rollback delivers nothing

- **WHEN** a transaction containing a task change is rolled back
- **THEN** no event from that transaction is delivered to any consumer

### Requirement: Delivery is at-least-once and survives restarts

The system SHALL retain recorded events until delivery is acknowledged, so that a crash between commit and delivery does not lose them. Consumers SHALL therefore tolerate receiving the same event more than once.

#### Scenario: Crash after commit, before delivery

- **WHEN** the process terminates after a transaction commits but before its events are delivered
- **THEN** those events are still available for delivery after restart

### Requirement: Delivery is not bound to the originating request

Delivery of events after commit SHALL NOT be abandoned because the request that caused the change has ended or been cancelled.

#### Scenario: Client disconnects immediately after completing

- **WHEN** a client cancels its request in the instant after a completion commits
- **THEN** the completion event is still delivered

### Requirement: A consumer that cannot guarantee durability is rejected at wiring time

The system SHALL refuse a configuration in which the durable, in-transaction event record is provided by something that cannot participate in the transaction.

#### Scenario: Misconfiguration fails at startup

- **WHEN** a host configures a non-transactional consumer as the durable event record
- **THEN** startup fails with a configuration error rather than losing events later

### Requirement: Callback targets are stored and echoed verbatim

A task MAY carry a callback target consisting of a delivery address and a set of caller-supplied reference parameters. The system SHALL treat those parameters as opaque, SHALL NOT interpret or alter them, and SHALL return them unchanged with any notification it delivers to that address.

#### Scenario: Reference parameters are returned untouched

- **WHEN** a task created with reference parameters produces a notification to its callback address
- **THEN** the delivered notification carries those parameters with identical names and values

#### Scenario: Callback target is optional

- **WHEN** a task is created without a callback target
- **THEN** the task is created successfully and its events are delivered to consumers only
