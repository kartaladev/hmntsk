## Purpose

Provides dialect-portable SQL support that knows nothing about any domain. It describes each supported database by its capabilities, publishes and verifies schemas, and runs statements and transactions identically on every supported driver, so that any store built on it behaves the same everywhere.

## ADDED Requirements

### Requirement: Supported databases are described by capability

The toolkit SHALL describe each supported database (PostgreSQL 9.5 or later, MySQL 8.0 or later, SQLite 3.35 or later) by the capabilities that change the SQL it runs:
- the bind marker for an argument position;
- how an identifier is quoted;
- whether a write can return rows, and whether skip-locked row selection is available;
- the collation that makes identifier comparison case-sensitive;
- the column type for a UTC instant at microsecond precision, and the column type for an opaque JSON payload;
- the boolean literal, and the insert-or-update clause.

Statement construction SHALL depend on these capabilities and never on a database's name. Looking up a database by a name the toolkit does not support SHALL report that it is unsupported.

#### Scenario: Bind markers follow the database

- **WHEN** a statement binds three arguments on PostgreSQL and on MySQL
- **THEN** PostgreSQL renders them as `$1`, `$2`, `$3` and MySQL renders each as `?`

#### Scenario: A reserved word is usable as an identifier

- **WHEN** a statement names a column called `type` on each supported database
- **THEN** the identifier is quoted so that the statement is not a syntax error

#### Scenario: Unknown database name

- **WHEN** a caller looks up a database by a name that is not supported
- **THEN** the lookup reports that no such database is supported and returns no description

### Requirement: Payloads and instants round-trip unchanged

The toolkit SHALL store an opaque JSON payload as text and return it byte for byte as supplied. It SHALL store an instant as UTC at microsecond precision and return a value equal to the one written. On a database without a native timestamp type, it SHALL use a fixed-width textual encoding whose lexical order equals chronological order.

#### Scenario: Payload returned exactly as written

- **WHEN** a payload with unusual key order, whitespace and number literals is written and read back on each supported database
- **THEN** the bytes read back are identical to the bytes written

#### Scenario: Instant round-trips to the microsecond

- **WHEN** an instant with nanosecond detail in a non-UTC zone is written and read back on each supported database
- **THEN** the value read back is the same instant in UTC, truncated to microsecond precision

#### Scenario: Textual timestamps sort chronologically

- **WHEN** instants are stored on a database without a native timestamp type and ordered by that column
- **THEN** the rows come back in chronological order

### Requirement: Schemas are published, never applied automatically

The toolkit SHALL render a published schema document for a chosen database into executable statements, with the caller's table prefix applied, in the order they must run. It SHALL NOT apply schema changes on its own in normal operation. A development runner SHALL apply rendered statements only when explicitly invoked, and a failure SHALL name the statement that failed.

#### Scenario: Schema rendered with a prefix

- **WHEN** a schema document is rendered for a database with the prefix `acme_`
- **THEN** every table the document creates carries the prefix, comments and blank lines are dropped, and statements are returned in document order

#### Scenario: Nothing is applied implicitly

- **WHEN** a store built on the toolkit is constructed against a database whose schema is missing
- **THEN** no schema statement is executed

#### Scenario: Failed statement is named

- **WHEN** the development runner applies a statement the database rejects
- **THEN** it stops and returns an error that includes the rejected statement's text

### Requirement: Live schema verification reports every discrepancy

The toolkit SHALL compare a live database against a caller-supplied expectation of tables, columns, case-sensitive identifier columns and named secondary indexes. It SHALL report every discrepancy found in one error rather than the first, and SHALL classify that error as a configuration problem.

A table that is missing altogether SHALL be reported once, without also reporting each of its columns or indexes. On a database that does not report collations through introspection, an unreported collation SHALL be treated as the default.

#### Scenario: Several problems reported together

- **WHEN** verification runs against a database missing one table, one column of another table, and one required index
- **THEN** a single error lists all three discrepancies and is classified as a configuration problem

#### Scenario: Wrong collation on an identifier column

- **WHEN** an identifier column on MySQL carries a case-insensitive collation
- **THEN** verification reports that column's collation as a discrepancy

#### Scenario: Correct schema passes

- **WHEN** verification runs against a database created from the published schema for the same expectation and prefix
- **THEN** verification succeeds

### Requirement: Statements execute through one executor contract

The toolkit SHALL offer one contract for executing statements that every supported driver implements:
- A write SHALL report how many rows it matched.
- An empty statement SHALL do nothing and report zero rows.
- A read SHALL present its rows to the caller and release them afterwards, including when reading fails part-way.
- An execution error SHALL include the text of the statement that failed.
- An executor SHALL render statements with the bind marker its driver requires, so that a statement built for it never reaches the database with unbound markers.

#### Scenario: Rows matched are reported

- **WHEN** an update matching two rows runs through an executor
- **THEN** the executor reports two rows

#### Scenario: Rows released after a failed scan

- **WHEN** the caller's row handling returns an error on the first row
- **THEN** the executor returns that error and the rows are released

#### Scenario: Placeholders match the driver

- **WHEN** a statement is built for an executor whose driver rewrites `?` markers itself
- **THEN** the statement carries `?` markers and every argument is bound on each supported database

### Requirement: Whoever begins a transaction commits it

An executor SHALL NOT commit or roll back a transaction it did not begin. When the caller supplies an already-open transaction through the context, the executor SHALL run inside it and leave its disposition to the caller. The executor SHALL report whether a transaction is active on a given context.

#### Scenario: Executor-led transaction

- **WHEN** work is run in a transactional scope with no transaction active
- **THEN** the executor begins a transaction, runs the work, and commits before returning

#### Scenario: Caller-led transaction is left open

- **WHEN** work is run in a transactional scope on a context carrying a transaction the caller began
- **THEN** the work runs in that transaction, the executor neither commits nor rolls it back, and the caller's later rollback leaves nothing durable

### Requirement: Nested scopes join and flatten

When a transactional scope is entered while one is active, an executor SHALL join the active transaction and SHALL NOT begin a second transaction or create a savepoint, including on drivers that nest with savepoints by default. A failure in an inner scope SHALL abort the whole scope, even if the caller handles the inner error and continues.

#### Scenario: Inner failure aborts everything

- **WHEN** an inner scope writes a row and fails, and the outer scope handles the error and returns successfully
- **THEN** no write from either scope is durable

#### Scenario: Nested writes commit together

- **WHEN** an outer and an inner scope each write a row and both succeed
- **THEN** both rows become durable in one commit

### Requirement: Errors, panics and cancellation roll back

A transaction an executor began SHALL be rolled back when:
- the work returns an error;
- the work panics, in which case the panic SHALL be re-raised unchanged rather than converted to an error;
- the context is cancelled while the work runs, rather than committing work whose caller has gone.

#### Scenario: Panic rolls back and propagates

- **WHEN** work inside an executor-led transaction writes a row and then panics
- **THEN** the panic reaches the caller unchanged and the row is not durable

#### Scenario: Cancelled context does not commit

- **WHEN** the context is cancelled after the work has written a row but before the scope ends
- **THEN** the scope returns the cancellation and the row is not durable

### Requirement: Behaviour is identical on every driver and database

The toolkit SHALL behave identically on every supported combination of driver and database: `database/sql` with PostgreSQL, MySQL and SQLite; pgx with PostgreSQL; GORM with PostgreSQL, MySQL and SQLite. It SHALL assert this with one shared suite of behavioural cases executed against every combination, rather than per-driver tests.

#### Scenario: One suite, seven combinations

- **WHEN** the shared executor suite runs against each of the seven combinations
- **THEN** every case passes with identical observable results

#### Scenario: Identifier comparison is case-sensitive

- **WHEN** a value differing only in letter case from a stored identifier is compared on a column carrying the published identifier collation
- **THEN** it does not match, on every combination

### Requirement: The toolkit depends on no domain

The toolkit, its executors and its conformance suite SHALL NOT depend on any domain library that uses them, including in their tests. Adding such a dependency SHALL fail the repository's checks, so that the toolkit can move to its own repository without change.

#### Scenario: Forbidden import fails the build checks

- **WHEN** a toolkit package, or one of its tests, imports a domain module
- **THEN** the repository's dependency check fails and names the offending import
