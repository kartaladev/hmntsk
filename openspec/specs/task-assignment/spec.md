# task-assignment Specification

## Purpose

Defines who may act on a task — how candidate pools are expressed, how group membership is resolved against the host's identity system, and how eligibility is enforced on every operation that names an actor.

## Requirements

### Requirement: Candidate pools are expressed as users, groups and exclusions

A task SHALL carry a set of candidate users, a set of candidate groups, and a set of excluded users. An actor SHALL be eligible for a task when they are a candidate user or a member of a candidate group, and are not excluded.

#### Scenario: Group member is eligible

- **WHEN** an actor who belongs to one of a task's candidate groups attempts to claim it
- **THEN** the claim is permitted

#### Scenario: Exclusion overrides inclusion

- **WHEN** an actor is both a candidate user and an excluded user for the same task
- **THEN** the actor is not eligible and any operation they attempt on the task is refused

### Requirement: Group membership is resolved through a host-supplied port

The system SHALL resolve group membership through a port implemented by the host, and SHALL NOT embed any directory, role store or membership model of its own.

#### Scenario: Resolution failure surfaces as a fault

- **WHEN** group resolution fails while evaluating eligibility
- **THEN** the operation fails with an error distinguishable from an eligibility denial, and the task is unchanged

#### Scenario: Resolution is substitutable

- **WHEN** the host substitutes a different group resolution implementation
- **THEN** eligibility decisions change accordingly with no change to lifecycle behaviour

### Requirement: A task with exactly one candidate is reserved on creation

When a newly created task resolves to exactly one eligible actor, the system SHALL reserve it for that actor rather than leaving it in the pool.

#### Scenario: Single candidate is auto-reserved

- **WHEN** a task is created whose candidate pool resolves to one actor
- **THEN** the task's status is `RESERVED` and that actor is its assignee

#### Scenario: Multiple candidates remain in the pool

- **WHEN** a task is created whose candidate pool resolves to more than one actor
- **THEN** the task's status is `READY` and it has no assignee

### Requirement: A task with no eligible actor is faulted, not orphaned

When a newly created task resolves to no eligible actor, the system SHALL move it to `ERROR` and record the reason rather than leaving it unclaimable in `READY`.

#### Scenario: Empty candidate pool

- **WHEN** a task is created whose candidate pool resolves to no actors
- **THEN** the task's status is `ERROR`, the reason is recorded in history, and an error event is produced

### Requirement: Eligibility is enforced on every actor-driven operation

The system SHALL verify the acting actor's eligibility on claim and delegate, and SHALL verify that the acting actor is the current assignee on release, start, save progress, complete, fail, suspend and resume.

#### Scenario: Non-assignee cannot complete

- **WHEN** an actor who is not the assignee attempts to complete a reserved task
- **THEN** the operation is refused with an authorisation error and the task is unchanged

#### Scenario: Ineligible actor cannot claim

- **WHEN** an actor outside the candidate pool attempts to claim a `READY` task
- **THEN** the operation is refused with an authorisation error and the task remains `READY`

### Requirement: Assignment behaviour is substitutable

The system SHALL expose assignment as a port so that candidate resolution can be replaced without modifying lifecycle behaviour.

#### Scenario: Static assignment for tests

- **WHEN** the host supplies a fixed, in-memory assignment implementation
- **THEN** all lifecycle operations behave identically to running against a live directory

### Requirement: Inbox queries reflect eligibility

The system SHALL support querying the tasks an actor may act on, distinguishing tasks reserved for that actor from tasks they are merely eligible to claim.

#### Scenario: Claimable and assigned tasks are distinguishable

- **WHEN** an actor queries their inbox while holding one reserved task and being eligible for two pooled tasks
- **THEN** the response identifies which task is assigned to them and which are available to claim

#### Scenario: Excluded tasks are absent

- **WHEN** an actor queries their inbox and is excluded from a task in a group they belong to
- **THEN** that task does not appear in the response
