## ADDED Requirements

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
