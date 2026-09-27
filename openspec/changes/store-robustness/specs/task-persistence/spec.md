## MODIFIED Requirements

### Requirement: Concurrency control is portable across all supported dialects

The system SHALL detect concurrent modification by conditional update on a version value carried by every task, and SHALL NOT depend on row-level locking being available.

A conflict SHALL be reported as an error matching the conflict error on every store, whether the engine's conditional update or the database itself detected it. By default:
- A duplicate key on task creation SHALL be reported as a conflict on the task's identifier. This includes a duplicate the database detects after two concurrent transactions both saw no existing row.
- A serialization failure, a deadlock, or a lock the database could not grant SHALL be reported as a conflict. The driver's original error SHALL remain reachable through the returned error.

When the store reports the version a losing writer must re-read, that version SHALL be the latest committed version. It SHALL NOT be a value from the losing transaction's own snapshot. When the version cannot be read, it SHALL be reported as zero.

A host MAY replace how a store classifies driver errors. A classification it supplies SHALL be used in place of the default.

#### Scenario: Conflict detected without row locks

- **WHEN** two concurrent mutations target the same task on a database that offers no row-level locking
- **THEN** exactly one succeeds and the other fails with a conflict error

#### Scenario: Version advances on every mutation

- **WHEN** any mutation to a task succeeds
- **THEN** the task's version differs from the value the caller supplied

#### Scenario: Concurrent creation of the same identifier

- **WHEN** two transactions each create a task with the same caller-supplied identifier, the second insert waits on the first, and the first commits
- **THEN** on every supported driver and dialect the second fails with an error matching the conflict error for that identifier, not a driver-specific duplicate-key error

#### Scenario: Losing writer is told the committed version

- **WHEN** a transaction reads a task at version 1, another transaction advances it to version 2 and commits, and the first then attempts its update, on a database whose default isolation reads from a snapshot
- **THEN** the first fails with a conflict error whose current version is 2

#### Scenario: Deadlock is a conflict

- **WHEN** two transactions each hold a lock the other needs and the database aborts one of them as a deadlock victim
- **THEN** the aborted transaction's operation fails with an error matching the conflict error, and the driver's error is still reachable from it

#### Scenario: Stale SQLite snapshot is a conflict

- **WHEN** on SQLite, with a connection string that does not request immediate transactions, two transactions read the same task, the first updates it and commits, and the second then updates it
- **THEN** the second fails with an error matching the conflict error, not a "database is locked" error

#### Scenario: Host-supplied error classification

- **WHEN** a host constructs a store with its own error classification that treats a particular driver error as a conflict
- **THEN** that driver error is reported as matching the conflict error, and driver errors the host's classification does not recognise are returned unchanged

### Requirement: The live schema is verified at startup

The system SHALL offer a verification that compares the live database schema against what its queries require, and SHALL report every discrepancy rather than failing on first use. On every supported dialect, verification SHALL include whether each identifier column carries the collation that makes comparison case-sensitive and ordering byte-wise.

#### Scenario: Missing table is reported before traffic

- **WHEN** verification runs against a database missing a required table
- **THEN** verification fails and names the missing table

#### Scenario: Verification passes on a correct schema

- **WHEN** verification runs against a database whose schema matches the published definition
- **THEN** verification succeeds

#### Scenario: Case-insensitive identifier column is reported

- **WHEN** verification runs against a schema created from the published definition, except that its identifier columns are declared case-insensitive on SQLite or without the byte-wise collation on PostgreSQL
- **THEN** verification fails with a schema mismatch that names each such column

### Requirement: Table names are host-configurable

The system SHALL allow the host to apply a prefix to every table it owns, so that the engine can be embedded in a database that already uses those names. By default no prefix is applied.

A prefix SHALL consist only of ASCII letters, digits and underscores. Every table, index and constraint name it produces SHALL fit the chosen dialect's identifier length limit. A prefix that breaks either rule SHALL be refused as a configuration error when the store is constructed, before any statement runs. A prefix SHALL NEVER be executed as SQL.

#### Scenario: Prefixed deployment

- **WHEN** a host configures a table prefix and applies the corresponding schema
- **THEN** all engine operations read and write the prefixed tables and none of the unprefixed ones

#### Scenario: Prefix carrying SQL is refused

- **WHEN** a host configures a prefix containing a quote character, a semicolon and a `DROP TABLE` statement
- **THEN** store construction fails with a configuration error, and no table in the database is created, altered or dropped

#### Scenario: Prefix too long for the dialect is refused

- **WHEN** a host configures, on PostgreSQL, a prefix that would make the longest engine-owned name exceed 63 bytes
- **THEN** store construction fails with a configuration error that names the limit, rather than the database silently truncating two names into one

## ADDED Requirements

### Requirement: A miswired store is refused at construction

A store constructor SHALL refuse a missing database handle or a missing dialect with a configuration error. It SHALL NOT return a store that fails or panics on first use.

#### Scenario: Nil handle

- **WHEN** a host constructs any SQL-backed store with a nil database handle, pool or dialect
- **THEN** construction returns an error matching the configuration error and no store

#### Scenario: Correctly wired store

- **WHEN** a host constructs a store with a valid handle, a supported dialect and no options
- **THEN** construction succeeds with no prefix applied

### Requirement: Candidate pools have set semantics

A candidate pool's users, groups and exclusions SHALL each behave as a set. A repeated entry SHALL carry no meaning. The engine and every store SHALL keep the first occurrence of each entry, in the order supplied, and drop later repeats. A pool with repeats SHALL NOT be rejected. This is deliberately not configurable: a repeated entry cannot change eligibility, so there is no behaviour to choose between.

#### Scenario: Duplicate candidate users on create

- **WHEN** a task is created with candidate users `alice, bob, alice`
- **THEN** creation succeeds on every store, and the stored task's candidate users read back as `alice, bob`

#### Scenario: Duplicates written straight through the repository

- **WHEN** a task whose candidate groups contain a repeated entry is created or updated directly through the repository port
- **THEN** every store, including the in-memory store, reads it back with the repeat removed and the first-seen order kept

### Requirement: Identifier lengths are bounded identically everywhere

The engine SHALL validate the length of each identifier it stores before writing it, against documented, named maximums. The same limits SHALL apply on every store:
- task identifier: 64 characters;
- task type name and owner type: 128 characters;
- owner reference, activity key, candidate user, candidate group, excluded actor, assignee and acting actor: 255 characters.

Length SHALL be counted in Unicode characters. A value over its limit SHALL be rejected with a validation error naming the field and its limit, and nothing SHALL be written. These maximums are the widths of the published schema's narrowest dialect. They are not configurable, because a longer value could not be stored portably. Every store SHALL apply the same limits when a task is created or updated directly through the repository port, so that the engine and the port reject the same values.

#### Scenario: Over-long task identifier

- **WHEN** a task is created with a 65-character identifier
- **THEN** creation fails with a validation error naming the task identifier and the 64-character limit, on every store

#### Scenario: Identifier at the limit

- **WHEN** a task is created with a 64-character identifier
- **THEN** creation succeeds and the task reads back with that identifier on every store

#### Scenario: Over-long value straight through the repository

- **WHEN** a task with a 65-character identifier is created directly through the repository port
- **THEN** every store, including the in-memory store, returns an error matching the validation error and writes nothing; on MySQL it is not a driver "data too long" error

### Requirement: Large candidate pools are stored whole

A store SHALL persist a candidate pool of any size the database can hold, and SHALL NOT fail because a single statement would exceed the database's limit on bind parameters. A pool SHALL be written atomically within the operation's transaction.

#### Scenario: Pool beyond the bind limit

- **WHEN** a task is created with 17,000 candidate users on PostgreSQL or MySQL, or 9,000 on SQLite
- **THEN** creation succeeds and the task reads back with every candidate user, in order

#### Scenario: Failure part-way leaves no partial pool

- **WHEN** writing a large pool fails after some of its rows were written, and the transaction rolls back
- **THEN** no candidate row of that task is durable

### Requirement: Overdue tasks are claimed most overdue first

When more tasks are overdue than a sweep's limit allows, every store SHALL claim them in ascending order of due date, with ties broken by ascending task identifier in byte-wise order. The claim order SHALL be part of the repository contract.

#### Scenario: Backlog larger than the limit

- **WHEN** task A is one hour overdue, task B is two hours overdue, and a sweep claims with a limit of 1
- **THEN** every store, including the in-memory store, claims task B

#### Scenario: Equal due dates

- **WHEN** two overdue tasks share a due date and a sweep claims with a limit of 1
- **THEN** every store claims the one whose identifier sorts first byte-wise

### Requirement: Identifiers page in byte-wise order on every dialect

The order used for identifier ties in listings and pagination SHALL be byte-wise on every supported dialect, so that upper-case identifiers sort before lower-case ones. It SHALL NOT be case-insensitive or accent-insensitive. Comparison of identifiers SHALL remain case-sensitive, and SHALL NOT ignore trailing spaces.

#### Scenario: Mixed-case identifiers page identically

- **WHEN** tasks `b-1`, `C-1`, `a-1`, `B-1`, `A-1`, `c-1` with equal creation time are paged two at a time
- **THEN** every store returns `A-1, B-1, C-1, a-1, b-1, c-1`

#### Scenario: Trailing space is significant

- **WHEN** a candidate `alice` is stored and the actor `alice ` (with a trailing space) is evaluated for eligibility
- **THEN** it does not match, on every supported dialect

### Requirement: A call's own context is honoured inside a transaction

Each repository call made inside a transaction SHALL run under the context passed to that call, not the context the transaction began with. A call whose context is already cancelled SHALL fail with the cancellation, and the transaction SHALL be left to its owner.

#### Scenario: Cancelled per-call context inside a transaction

- **WHEN** a transaction is open and a read is made with a context derived from the transaction's context and then cancelled
- **THEN** the read fails with an error matching `context.Canceled` on every driver, including GORM
