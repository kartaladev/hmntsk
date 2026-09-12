# task-persistence Specification

## Purpose

Defines the storage contract a host must satisfy and the guarantees the engine gives in return — transaction ownership, conflict detection, and identical observable behaviour across every supported database driver and dialect.

## Requirements

### Requirement: Task state lives in the host's database

The system SHALL store task state in the same database as the host's own data, so that a task change and a host change can be committed together.

#### Scenario: Task and business data commit together

- **WHEN** a host writes a business record and completes a task within one transaction
- **THEN** either both are durable or neither is

### Requirement: Whoever begins a transaction commits it

The system SHALL NOT commit or roll back a transaction it did not begin. When the host provides an already-open transaction, the engine SHALL join it and leave its disposition to the host.

#### Scenario: Engine-led transaction

- **WHEN** a lifecycle operation is invoked outside any transaction
- **THEN** the engine begins a transaction, applies the change, and commits it before returning

#### Scenario: Host-led transaction is not committed by the engine

- **WHEN** a lifecycle operation is invoked inside a transaction the host began
- **THEN** the change is applied within that transaction and the engine neither commits nor rolls it back

### Requirement: Nested transaction scopes join rather than nest

When a transactional scope is entered while one is already active, the system SHALL join the active transaction rather than beginning a second one or creating a savepoint.

#### Scenario: Operation invoked from within another operation

- **WHEN** one lifecycle operation invokes another
- **THEN** both changes apply within a single transaction and commit together

#### Scenario: Inner failure aborts the whole scope

- **WHEN** an inner operation fails inside an outer transactional scope
- **THEN** the entire scope is rolled back and no partial change is durable

### Requirement: Concurrency control is portable across all supported dialects

The system SHALL detect concurrent modification by conditional update on a version value carried by every task, and SHALL NOT depend on row-level locking being available.

#### Scenario: Conflict detected without row locks

- **WHEN** two concurrent mutations target the same task on a database that offers no row-level locking
- **THEN** exactly one succeeds and the other fails with a conflict error

#### Scenario: Version advances on every mutation

- **WHEN** any mutation to a task succeeds
- **THEN** the task's version differs from the value the caller supplied

### Requirement: State changes and their events are written atomically

A task state change and the events it produces SHALL be written within one transaction, so that a durable state change is always accompanied by its durable events.

#### Scenario: Crash between state and event write is impossible

- **WHEN** a completion is committed
- **THEN** the completion event is durable in the same commit

#### Scenario: Rolled-back change leaves no events

- **WHEN** a transaction containing a task change is rolled back
- **THEN** neither the state change nor its events are durable

### Requirement: A failed transaction leaves the caller's task value unchanged

Transitions SHALL produce a new task value rather than mutating the caller's. A caller SHALL observe a new state only after the change is durable.

#### Scenario: Rolled-back claim does not appear to have happened

- **WHEN** a claim is applied and the transaction then fails
- **THEN** the error is returned and the task value held by the caller still shows the pre-claim state

### Requirement: Behaviour is identical across every supported driver and dialect

The system SHALL behave identically across the supported combinations of driver and dialect, and SHALL assert this with one shared suite of behavioural cases executed against every combination rather than per-driver tests.

#### Scenario: One suite, every combination

- **WHEN** the shared storage suite is executed against each supported driver and dialect combination
- **THEN** every case passes with identical observable results

#### Scenario: Identifier comparison is case-sensitive everywhere

- **WHEN** an actor identifier differing only in letter case from a stored candidate is evaluated for eligibility
- **THEN** it does not match, on every supported dialect

#### Scenario: Timestamps round-trip without loss

- **WHEN** a deadline is stored and read back
- **THEN** the value is identical to microsecond precision, on every supported dialect

### Requirement: Filterable data is not hidden inside payloads

The system SHALL keep every value used for filtering, sorting or correlation in its own queryable field, and SHALL treat input and output payloads as opaque for query purposes.

#### Scenario: Correlation lookup does not inspect payloads

- **WHEN** a caller queries tasks by their owning unit of work
- **THEN** the query matches on stored correlation fields and inspects no payload content

### Requirement: The host owns schema migration

The system SHALL publish the schema it requires, per supported dialect, in a form the host can apply through its own migration pipeline. The system SHALL NOT apply schema changes automatically in normal operation.

#### Scenario: Schema is obtainable for a chosen dialect

- **WHEN** a host requests the schema definition for a supported dialect
- **THEN** the complete set of statements required by the engine is returned

#### Scenario: No automatic migration

- **WHEN** the engine starts against a database whose schema is missing or outdated
- **THEN** the engine does not alter the schema

### Requirement: The live schema is verified at startup

The system SHALL offer a verification that compares the live database schema against what its queries require, and SHALL report every discrepancy rather than failing on first use.

#### Scenario: Missing table is reported before traffic

- **WHEN** verification runs against a database missing a required table
- **THEN** verification fails and names the missing table

#### Scenario: Verification passes on a correct schema

- **WHEN** verification runs against a database whose schema matches the published definition
- **THEN** verification succeeds

### Requirement: Table names are host-configurable

The system SHALL allow the host to apply a prefix to every table it owns, so that the engine can be embedded in a database that already uses those names.

#### Scenario: Prefixed deployment

- **WHEN** a host configures a table prefix and applies the corresponding schema
- **THEN** all engine operations read and write the prefixed tables and none of the unprefixed ones
