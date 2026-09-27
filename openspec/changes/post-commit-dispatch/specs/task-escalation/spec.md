## ADDED Requirements

### Requirement: A sweep inside a host transaction dispatches only after the host commits

A sweep invoked on a context that carries a transaction the host began SHALL NOT dispatch escalation events to in-process consumers during the sweep.

By default, the sweep result SHALL carry the withheld events and report them as pending, so that the host can dispatch them after it commits. When the host opened a deferred-dispatch scope before beginning its transaction, the withheld events SHALL be collected in that scope instead, and the sweep result reports nothing pending.

A sweep with no host transaction SHALL keep its current behaviour: each escalation commits on its own and is dispatched right after its commit.

#### Scenario: Default hand-back on the sweep result

- **WHEN** a host sweeps overdue tasks inside its own transaction with no deferred-dispatch scope
- **THEN** no in-process consumer runs during the sweep, the sweep result reports the escalation events as pending, and dispatching the sweep result after the host commits delivers them

#### Scenario: Host rolls back a sweep

- **WHEN** a host sweeps inside its own transaction and then rolls it back
- **THEN** no in-process consumer was told about any escalation, and the tasks are not escalated

#### Scenario: Host override collects a sweep into a deferred-dispatch scope

- **WHEN** a host opens a deferred-dispatch scope, sweeps inside its transaction, commits, and dispatches the scope
- **THEN** the escalation events are delivered only at that dispatch, and the sweep result reports nothing pending

#### Scenario: Engine-led sweep is unchanged

- **WHEN** a sweep runs with no host transaction
- **THEN** each escalated task's events are dispatched to in-process consumers after that task's own escalation commits, and the sweep result reports nothing pending

#### Scenario: A consumer error from a sweep hand-back reaches the dispatch error hook

- **WHEN** the host dispatches a sweep result after committing and an in-process consumer returns an error
- **THEN** the host's dispatch error hook receives the error and the dispatch call returns it
