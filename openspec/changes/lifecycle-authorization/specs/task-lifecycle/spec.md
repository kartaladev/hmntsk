## ADDED Requirements

### Requirement: Every actor-driven operation names its actor

The system SHALL refuse with an authorisation error any of `Create`, `Claim`, `Release`, `Start`, `SaveProgress`, `Complete`, `Fail`, `Delegate`, `Suspend`, `Resume`, `Escalate` or `Cancel` invoked without an acting actor.

The refusal SHALL come before the task is read. A caller without an actor SHALL therefore receive the same authorisation error whether the task exists or not, whatever version it supplied, and whatever state the task is in. The caller learns nothing about the task.

An empty actor in history and on events SHALL mean only that the system acted on its own behalf, as when it faults a task. No host policy SHALL be able to relax this requirement.

#### Scenario: A cancel without an actor is refused

- **WHEN** a `READY` task is cancelled with no acting actor
- **THEN** the operation fails with an authorisation error, the task remains `READY`, and no history record or event is produced

#### Scenario: An escalation without an actor is refused

- **WHEN** a `READY` task is escalated with no acting actor
- **THEN** the operation fails with an authorisation error and the candidate pool is unchanged

#### Scenario: A create without an actor is refused

- **WHEN** a task is created with no acting actor
- **THEN** the operation fails with an authorisation error and no task is stored

#### Scenario: A stale version reveals nothing to a caller without an actor

- **WHEN** a claim with no acting actor names an existing task and a version that is not current
- **THEN** the operation fails with an authorisation error, not a conflict error, and the error does not carry the current version

#### Scenario: An unknown task reveals nothing to a caller without an actor

- **WHEN** a claim with no acting actor names a task that does not exist
- **THEN** the operation fails with the same authorisation error as for an existing task

#### Scenario: A system fault is the only anonymous record

- **WHEN** creation cannot resolve any eligible actor and the task is faulted
- **THEN** the fault's history record and event carry an empty actor, and the task records the actor who created it

### Requirement: Who may cancel, suspend, resume or escalate is replaceable

The rules deciding which named actor may cancel, suspend, resume or escalate a task SHALL be one replaceable policy.

The system SHALL apply the policy inside the operation's transaction, after the state machine has accepted the move. It SHALL give the policy the operation, the actor, the task as read, and the engine's own eligibility check for that actor. The eligibility check SHALL resolve group membership only when the policy calls it.

By default the policy SHALL be the ownership rules this capability and the `task-assignment` capability describe. The host SHALL be able to replace the default wholesale, and it SHALL be able to call the default from its own policy.

Supplying an empty policy SHALL be refused when the engine is constructed.

The policy SHALL NOT govern claim, release, start, save progress, complete, fail or delegate. Those operations keep the assignee and eligibility rules, and no host policy can widen them.

#### Scenario: A host lets an administrator cancel any task

- **WHEN** the host supplies a policy permitting the actor "admin" to cancel every task, and "admin", who did not create the task, cancels it
- **THEN** the task's status becomes `EXITED` and history records "admin" as the actor

#### Scenario: A host forbids suspending pooled tasks

- **WHEN** the host supplies a policy refusing suspension of any task nobody holds, and the task's creator suspends a `READY` task
- **THEN** the operation fails with an authorisation error carrying the policy's reason, and the task remains `READY`

#### Scenario: A host policy cannot restore anonymous operations

- **WHEN** the host supplies a policy that permits every operation, and a task is cancelled with no acting actor
- **THEN** the operation still fails with an authorisation error

#### Scenario: A host policy cannot let a stranger complete work

- **WHEN** the host supplies a policy that permits every operation, and an actor who is not the assignee completes a reserved task
- **THEN** the operation fails with an authorisation error and the task is unchanged

#### Scenario: An empty policy is a wiring mistake

- **WHEN** the engine is constructed with an empty lifecycle policy
- **THEN** construction fails with a configuration error

#### Scenario: A directory failure is not a refusal

- **WHEN** a candidate suspends a pooled task under the default policy and group membership cannot be resolved
- **THEN** the operation fails with a group-resolution error, not an authorisation error, and the task is unchanged

## MODIFIED Requirements

### Requirement: Cancellation from any state a stored task can occupy

The task's creator SHALL be able to cancel a task in `READY`, `RESERVED`, `IN_PROGRESS` or `SUSPENDED`, moving it to `EXITED`.

By default the system SHALL refuse a cancellation by any other actor with an authorisation error: the assignee, a candidate or a stranger. The host SHALL be able to change who may cancel through the replaceable lifecycle policy.

`CREATED` is deliberately absent: it exists only within the creation operation, which resolves it to `READY`, `RESERVED` or `ERROR` before returning, so no stored task is ever observed in it and nothing can be cancelled from it. This matches the transition table above, which permits only the transitions it lists.

#### Scenario: Cancelling in-flight work

- **WHEN** the creator of a task in `IN_PROGRESS` cancels it
- **THEN** its status becomes `EXITED`, the cancellation is recorded in history under the creator, and a cancellation event is produced

#### Scenario: Cancelling suspended work

- **WHEN** the creator of a task in `SUSPENDED` cancels it
- **THEN** its status becomes `EXITED` and it is not resumable

#### Scenario: A stranger cannot cancel

- **WHEN** an actor who neither created, holds nor is a candidate for a reserved task cancels it
- **THEN** the operation fails with an authorisation error and the task remains `RESERVED`

#### Scenario: The assignee cannot cancel by default

- **WHEN** the assignee of an `IN_PROGRESS` task, who did not create it, cancels it
- **THEN** the operation fails with an authorisation error; the assignee can fail the task instead

#### Scenario: An illegal cancellation is still a conflict

- **WHEN** an actor who is not the creator cancels a `COMPLETED` task
- **THEN** the operation fails with a conflict error, because the state machine is checked before the actor's rights
