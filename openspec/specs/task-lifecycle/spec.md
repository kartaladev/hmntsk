# task-lifecycle Specification

## Purpose

Defines the human task aggregate, the ten states it can occupy, and the operations that move it between them, so that every caller observes one consistent set of rules about what a task is and what may happen to it next.

## Requirements

### Requirement: Task state set

The system SHALL represent a task's position in its lifecycle with exactly one of ten states: `CREATED`, `READY`, `RESERVED`, `IN_PROGRESS`, `SUSPENDED`, `COMPLETED`, `FAILED`, `ERROR`, `EXITED`, `OBSOLETE`. The states `COMPLETED`, `FAILED`, `ERROR`, `EXITED` and `OBSOLETE` SHALL be terminal: no operation may move a task out of them.

#### Scenario: Terminal state rejects further operations

- **WHEN** any lifecycle operation is invoked on a task in a terminal state
- **THEN** the operation fails with a conflict error, the stored task is unchanged, and no event is produced

#### Scenario: State is always exactly one value

- **WHEN** a task is read
- **THEN** its status is exactly one of the ten defined states

### Requirement: Status carries no business outcome

A task's status SHALL describe only its lifecycle position and never the result of the work. The outcome of the work SHALL be carried in the task's output payload.

#### Scenario: A rejected approval is a completion

- **WHEN** an actor completes an approval task with an output indicating the request was denied
- **THEN** the task's status is `COMPLETED`, not `FAILED`, and the denial is readable only from the output payload

#### Scenario: Consumers cannot infer outcome from status

- **WHEN** two tasks of the same type complete with opposite outputs
- **THEN** both have status `COMPLETED` and are distinguishable only by their output payloads

### Requirement: Transitions are restricted to the defined state machine

The system SHALL permit only the following transitions, and SHALL reject any other transition with a conflict error:

- `CREATED` → `READY`, `RESERVED` (single candidate), `ERROR`
- `READY` → `RESERVED`, `SUSPENDED`, `OBSOLETE`, `EXITED`
- `RESERVED` → `READY`, `IN_PROGRESS`, `RESERVED` (delegate), `SUSPENDED`, `OBSOLETE`, `EXITED`
- `IN_PROGRESS` → `RESERVED`, `COMPLETED`, `FAILED`, `ERROR`, `SUSPENDED`, `EXITED`
- `SUSPENDED` → the state the task occupied immediately before suspension, or `EXITED`

#### Scenario: Illegal transition is refused

- **WHEN** an actor attempts to complete a task in state `READY`
- **THEN** the operation fails with a conflict error and the task remains `READY`

#### Scenario: Legal transition is applied

- **WHEN** an eligible actor claims a task in state `READY`
- **THEN** the task's status becomes `RESERVED` and the actor is recorded as its assignee

### Requirement: Lifecycle operations are the only mutation path

All task state changes SHALL occur through the defined operations — `Create`, `Claim`, `Release`, `Start`, `SaveProgress`, `Complete`, `Fail`, `Delegate`, `Suspend`, `Resume`, `Escalate`, `Cancel` — each of which validates the transition, records history and produces events. No operation SHALL expose a way to set a task's status directly.

#### Scenario: Every accepted transition is recorded

- **WHEN** any lifecycle operation succeeds
- **THEN** a history record is appended capturing the task, the state before, the state after, the acting actor, the time, and any supplied comment

#### Scenario: History is append-only

- **WHEN** a task undergoes several transitions
- **THEN** all earlier history records remain readable and unmodified

### Requirement: Claim and release

An actor SHALL be able to reserve a task from the candidate pool, and the assignee SHALL be able to return it to the pool. Claiming a task that is already reserved by another actor SHALL fail.

#### Scenario: Two actors claim the same task concurrently

- **WHEN** two eligible actors claim the same `READY` task at the same time
- **THEN** exactly one succeeds and becomes the assignee, and the other receives a conflict error

#### Scenario: Release returns the task to the pool

- **WHEN** the assignee releases a `RESERVED` task
- **THEN** the task's status becomes `READY`, the assignee is cleared, and the candidate pool is unchanged

### Requirement: Work is started explicitly or implicitly

The assignee SHALL be able to move a `RESERVED` task to `IN_PROGRESS` explicitly. The first successful progress save on a `RESERVED` task SHALL perform the same transition implicitly.

#### Scenario: First progress save starts the task

- **WHEN** the assignee saves progress on a task in state `RESERVED`
- **THEN** the task's status becomes `IN_PROGRESS` and the progress data is persisted in the same operation

#### Scenario: Subsequent progress saves do not re-transition

- **WHEN** the assignee saves progress again on the same task
- **THEN** the task remains `IN_PROGRESS` and no additional transition record is written

### Requirement: Completion and failure are distinct terminal outcomes

Completion SHALL record the actor's output payload and move the task to `COMPLETED`. Failure SHALL record that the actor could not perform the work and move the task to `FAILED`. The `ERROR` state SHALL be reserved for faults originating in the system rather than the actor, such as failed assignment resolution or an output that cannot be validated.

#### Scenario: Actor cannot perform the work

- **WHEN** the assignee fails a task with a reason
- **THEN** the task's status becomes `FAILED`, the reason is recorded in history, and no output payload is required

#### Scenario: System fault during creation

- **WHEN** candidate resolution fails while creating a task
- **THEN** the task's status becomes `ERROR` and the fault is recorded in history

### Requirement: Suspend and resume

A task in `READY`, `RESERVED` or `IN_PROGRESS` SHALL be suspendable, and resuming it SHALL restore the exact state it occupied before suspension. A suspended task SHALL NOT be escalated.

#### Scenario: Resume restores the prior state

- **WHEN** an `IN_PROGRESS` task is suspended and later resumed
- **THEN** its status returns to `IN_PROGRESS`, with its assignee and saved progress intact

#### Scenario: Suspended tasks are not escalated

- **WHEN** a suspended task passes its deadline
- **THEN** no escalation occurs and the task remains `SUSPENDED`

### Requirement: Delegation reassigns without losing work

The assignee SHALL be able to delegate a `RESERVED` or `IN_PROGRESS` task to another eligible actor. The delegated task SHALL become `RESERVED` for the new assignee and retain any saved progress.

#### Scenario: Delegation preserves saved progress

- **WHEN** an `IN_PROGRESS` task with saved progress is delegated
- **THEN** the task becomes `RESERVED` for the new assignee and the saved progress is unchanged

#### Scenario: Delegation to an ineligible actor is refused

- **WHEN** the assignee delegates to an actor who is not eligible for the task
- **THEN** the operation fails, the assignee is unchanged, and no event is produced

### Requirement: Cancellation from any state a stored task can occupy

The task's owner SHALL be able to cancel a task in `READY`, `RESERVED`, `IN_PROGRESS` or `SUSPENDED`, moving it to `EXITED`.

`CREATED` is deliberately absent: it exists only within the creation operation, which resolves it to `READY`, `RESERVED` or `ERROR` before returning, so no stored task is ever observed in it and nothing can be cancelled from it. This matches the transition table above, which permits only the transitions it lists.

#### Scenario: Cancelling in-flight work

- **WHEN** a task in `IN_PROGRESS` is cancelled
- **THEN** its status becomes `EXITED`, the cancellation is recorded in history, and a cancellation event is produced

#### Scenario: Cancelling suspended work

- **WHEN** a task in `SUSPENDED` is cancelled
- **THEN** its status becomes `EXITED` and it is not resumable

### Requirement: Concurrent modification is detected, never silently lost

Every task mutation SHALL be conditional on the version the caller last observed. A mutation against a stale version SHALL fail with a conflict error and change nothing.

#### Scenario: Stale write is rejected

- **WHEN** two actors read the same task and both submit a change
- **THEN** the first succeeds and increments the task's version, and the second fails with a conflict error reporting the current version

#### Scenario: Conflict leaves state untouched

- **WHEN** a mutation fails with a conflict error
- **THEN** the stored task is byte-for-byte unchanged and no event is produced
