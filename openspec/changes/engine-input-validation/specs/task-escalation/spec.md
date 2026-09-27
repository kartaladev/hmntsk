## MODIFIED Requirements

### Requirement: Escalation respects the task's working state

The system SHALL NOT escalate tasks in a terminal state or in `SUSPENDED`, and SHALL allow a type's policy to treat an actively worked task differently from an untouched one.

A policy's exemption of actively worked tasks SHALL govern sweeps only. An operator who escalates an `IN_PROGRESS` task directly SHALL have the escalation applied, because a direct escalation is an explicit decision about that task.

#### Scenario: Completed task is never escalated

- **WHEN** a sweep runs after a task's deadline but after it completed
- **THEN** the task is not escalated and its state is unchanged

#### Scenario: Actively worked task may be exempted

- **WHEN** a policy exempts `IN_PROGRESS` tasks and a task in that state passes its deadline
- **THEN** the task is not escalated

#### Scenario: A direct escalation overrides the in-progress exemption

- **WHEN** an operator directly escalates an `IN_PROGRESS` task whose policy exempts `IN_PROGRESS` tasks from sweeps
- **THEN** the escalation is applied and the task's escalation count increases

### Requirement: Repeated escalation of the same task is bounded

Widening a candidate pool SHALL NOT move the task's deadline, so an escalated task remains overdue. To stop it from being escalated on every subsequent sweep, the system SHALL retain the escalation lease rather than releasing it, so that a task becomes eligible for escalation again only once its lease expires. A task's escalation policy MAY additionally cap the total number of times that task is escalated.

The cap SHALL bound every escalation of the task, whether swept or direct. A direct escalation of a task that has reached its cap SHALL fail with an illegal-transition error, leaving the task, its history and its escalation count unchanged and producing no event.

#### Scenario: A widened task is not escalated again immediately

- **WHEN** a sweep escalates an overdue task by widening its pool, and another sweep runs before the lease expires
- **THEN** the task is not escalated a second time

#### Scenario: The next escalation waits for the lease to expire

- **WHEN** the lease on an escalated, still-overdue task expires and a sweep runs
- **THEN** the task is escalated again and its escalation count increases

#### Scenario: A policy caps total escalations

- **WHEN** a task whose policy permits at most two escalations has already been escalated twice
- **THEN** further sweeps do not escalate it, however long it remains overdue

#### Scenario: A direct escalation honours the cap

- **WHEN** an operator directly escalates a task whose policy permits at most one escalation and which has already been escalated once
- **THEN** the escalation fails with an illegal-transition error, the escalation count stays at one, and no event is produced

#### Scenario: An exempted task is not re-examined every sweep

- **WHEN** a sweep leaves a task alone because its policy exempts it
- **THEN** the task's lease is retained, so the following sweep does not reconsider it until the lease expires

## ADDED Requirements

### Requirement: A sweep reports escalations it could not apply

A sweep SHALL treat only a lost race on a claimed task as expected and silent. A lost race is a stale-version conflict, where the task changed between claiming and escalating. Every other failure to escalate a claimed task SHALL be reported to the host's sweep error hook, including a policy that asks for a transition the task's state does not permit. It SHALL identify the task and SHALL match the underlying error kind. The rest of the batch SHALL still be processed. The default hook SHALL discard reports, and a host SHALL be able to supply its own.

#### Scenario: A superseding policy on in-progress work is reported

- **WHEN** a sweep claims an overdue `IN_PROGRESS` task whose policy supersedes, and `IN_PROGRESS` to `OBSOLETE` is not a permitted transition
- **THEN** the host's sweep error hook receives an error that matches the illegal-transition error, and the task is unchanged

#### Scenario: A lost race stays silent

- **WHEN** a claimed task is changed by another actor between the claim and its escalation
- **THEN** the sweep skips it without reporting an error

### Requirement: A sweep stops when its caller cancels

A sweep SHALL stop processing its claimed batch once the caller's context is cancelled. It SHALL report the cancellation as its error, together with what it did before stopping. Tasks it claimed but did not reach SHALL keep their lease until it expires.

#### Scenario: Cancellation mid-batch stops the sweep

- **WHEN** a sweep has claimed three overdue tasks and its context is cancelled right after the first escalation commits
- **THEN** the sweep returns an error matching context cancellation, reports one escalation, and attempts neither remaining task

### Requirement: Sweeper configuration is validated at construction

Constructing a sweeper SHALL fail with a configuration error when it is given a lease duration or a batch size that is zero or negative. It SHALL NOT keep the default in place of the value supplied. A sweeper constructed with no options SHALL use the documented default lease duration and batch size.

#### Scenario: Default sweeper construction succeeds

- **WHEN** a host constructs a sweeper with no options
- **THEN** construction succeeds with the default lease duration and batch size

#### Scenario: A consumer lease override is applied

- **WHEN** a host constructs a sweeper with a lease duration of ten minutes
- **THEN** construction succeeds and the leases it takes last ten minutes

#### Scenario: A non-positive lease is refused

- **WHEN** a host constructs a sweeper with a lease duration of zero, or of minus one minute
- **THEN** construction fails with a configuration error

#### Scenario: A non-positive batch is refused

- **WHEN** a host constructs a sweeper with a batch size of zero, or of minus five
- **THEN** construction fails with a configuration error
