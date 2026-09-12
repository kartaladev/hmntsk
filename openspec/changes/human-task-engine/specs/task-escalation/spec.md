## Purpose

Defines what happens when a task is not finished in time — how deadlines are tracked, how overdue work is found and acted on without interfering with the request path, and how multiple application instances avoid escalating the same task twice.

## ADDED Requirements

### Requirement: Deadlines are evaluated off the request path

The system SHALL detect overdue tasks through a background sweep rather than checking deadlines while serving task operations.

#### Scenario: Reading a task does not escalate it

- **WHEN** an overdue task is read before any sweep has run
- **THEN** the task is returned unchanged and no escalation occurs

#### Scenario: Sweep finds overdue work

- **WHEN** a sweep runs while tasks are past their deadline
- **THEN** those tasks are selected for escalation and tasks within their deadline are not

### Requirement: Escalation candidates are claimed exclusively by lease

The system SHALL claim overdue tasks for escalation by taking a time-bounded lease recorded with the task, so that concurrent sweeps in different instances do not process the same task.

#### Scenario: Two instances sweep simultaneously

- **WHEN** two application instances sweep for overdue tasks at the same time
- **THEN** each overdue task is escalated exactly once

#### Scenario: Crashed sweeper does not block a task forever

- **WHEN** a sweeper takes a lease and terminates without releasing it
- **THEN** the task becomes available for escalation again once the lease expires

#### Scenario: Lease claiming requires no lock primitive

- **WHEN** a sweep runs against a database offering no row-level locking
- **THEN** exclusive claiming still holds and each task is escalated exactly once

### Requirement: Escalation is an ordinary lifecycle operation

Escalation SHALL be invocable directly as well as by the sweep, and SHALL pass through the same transition validation, history recording and event production as every other operation.

#### Scenario: Manual escalation

- **WHEN** an operator escalates a task directly
- **THEN** the escalation is applied, recorded in history and produces an escalation event, exactly as a swept escalation would

### Requirement: Escalation widens reach and announces itself

Escalating a task SHALL apply the escalation policy configured for it — widening its candidate pool — and SHALL produce an escalation event. The system SHALL NOT send notifications itself.

#### Scenario: Candidate pool is widened

- **WHEN** a task whose policy widens to a manager group is escalated
- **THEN** the manager group is added to the task's candidate groups and previously eligible actors remain eligible

#### Scenario: Notification is the host's decision

- **WHEN** an escalation event is produced
- **THEN** the engine sends no message to any notification channel, and delivery is left to consumers of the event

### Requirement: Escalation respects the task's working state

The system SHALL NOT escalate tasks in a terminal state or in `SUSPENDED`, and SHALL allow a type's policy to treat an actively worked task differently from an untouched one.

#### Scenario: Completed task is never escalated

- **WHEN** a sweep runs after a task's deadline but after it completed
- **THEN** the task is not escalated and its state is unchanged

#### Scenario: Actively worked task may be exempted

- **WHEN** a policy exempts `IN_PROGRESS` tasks and a task in that state passes its deadline
- **THEN** the task is not escalated

### Requirement: Superseded tasks become obsolete

When escalation replaces a task rather than widening it, the superseded task SHALL move to `OBSOLETE` and produce an obsolescence event.

#### Scenario: Superseded task is closed out

- **WHEN** an escalation supersedes a `READY` task
- **THEN** the superseded task's status becomes `OBSOLETE`, it accepts no further operations, and an obsolescence event is produced

### Requirement: The host controls when sweeps run

The system SHALL NOT start background activity on its own. Sweeping SHALL be driven by the host, whether from a long-running goroutine, a scheduled job, or a manual invocation.

#### Scenario: No implicit background work

- **WHEN** an engine is constructed and no sweep is started by the host
- **THEN** no background goroutine, timer or database polling begins
