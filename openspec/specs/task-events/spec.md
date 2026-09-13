# task-events Specification

## Purpose

Defines what the engine tells the outside world about its own state changes, and the delivery guarantees around it, so consumers can react to human tasks without the engine knowing anything about them.

## Requirements

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

The system SHALL retain recorded events until every configured destination has accepted them, so that a crash between commit and delivery does not lose them. Consumers SHALL therefore tolerate receiving the same event more than once.

An event that cannot be delivered — its attempt limit exhausted, or a destination rejecting it permanently — SHALL be marked dead-lettered and retained for inspection rather than retried indefinitely or discarded. Retention is therefore until acceptance **or** dead-lettering, not until acceptance alone.

#### Scenario: Crash after commit, before delivery

- **WHEN** the process terminates after a transaction commits but before its events are delivered
- **THEN** those events are still available for delivery after restart

#### Scenario: An undeliverable event is retained, not discarded

- **WHEN** an event exhausts its delivery attempts
- **THEN** it is marked dead-lettered, is no longer attempted, and remains readable with its attempt count and last error

#### Scenario: Acceptance is tracked per destination

- **WHEN** an event is accepted by one configured destination and not another
- **THEN** it is not yet considered delivered, and a later attempt targets only the destination that has not accepted it

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

### Requirement: Events describe the task's audience at the moment of the transition

Every event SHALL carry a snapshot of the task's audience as it stood immediately after the transition that produced the event:

- the complete candidate pool after the transition, including its excluded actors;
- the previous holder, present only when the task was held before the transition and the transition changed who holds it;
- the actor who created the task.

The snapshot SHALL describe the transition as it happened, not the task as it is when the event is read or delivered, so that a delivery retried later still names the audience of the original transition. A consumer that has no use for the snapshot SHALL be unaffected by its presence.

#### Scenario: A pooled creation names its pool and its creator

- **WHEN** a task created by "owner" with candidate users "alice" and "bob" and candidate group "finance" is placed in the pool
- **THEN** the creation event carries a candidate pool of users "alice" and "bob" and group "finance", carries "owner" as the creator, and carries no previous holder

#### Scenario: Exclusions are part of the pool

- **WHEN** a task whose pool names group "finance" and excludes "carol" produces an event
- **THEN** the event's candidate pool lists "carol" among its exclusions

#### Scenario: A claim from the pool has no previous holder

- **WHEN** "alice" claims a pooled task that nobody held
- **THEN** the claim event names "alice" as the assignee and carries no previous holder

#### Scenario: A release names who released the task

- **WHEN** "alice" releases a task she held
- **THEN** the release event carries no assignee and names "alice" as the previous holder

#### Scenario: A delegation names the previous holder

- **WHEN** "alice" delegates a task she holds to "bob"
- **THEN** the delegation event names "bob" as the assignee and "alice" as the previous holder

#### Scenario: A transition that keeps the holder names no previous holder

- **WHEN** the holder of a task starts, completes or suspends it, or the task is cancelled while held
- **THEN** the event carries no previous holder

#### Scenario: Escalation by widening reports the widened pool

- **WHEN** a pooled task whose pool names group "finance" is escalated by a policy that adds group "managers"
- **THEN** the escalation event's candidate pool names both "finance" and "managers"

#### Scenario: A redelivered event keeps the original audience

- **WHEN** a claim event is recorded, the task is afterwards delegated to another actor, and the claim event is then delivered again
- **THEN** the redelivered claim event carries the same candidate pool, assignee and previous holder it carried when it was recorded

### Requirement: Every destination receives the audience snapshot

Events delivered to any configured destination SHALL carry the audience snapshot in their body, identically on every supported destination and every supported store. Routing hints delivered alongside the body, such as headers or top-level fields, SHALL NOT be required to carry it.

#### Scenario: The snapshot survives the durable record

- **WHEN** an event carrying a candidate pool, a previous holder and a creator is recorded and later claimed for delivery from any supported store
- **THEN** the claimed event carries the same candidate pool, previous holder and creator

#### Scenario: A webhook delivery carries the snapshot

- **WHEN** a delegation event is delivered to a webhook destination
- **THEN** the delivered body's event carries the candidate pool, the previous holder and the creator

#### Scenario: A broker delivery carries the snapshot

- **WHEN** a delegation event is published to a message broker destination
- **THEN** the published body carries the candidate pool, the previous holder and the creator
