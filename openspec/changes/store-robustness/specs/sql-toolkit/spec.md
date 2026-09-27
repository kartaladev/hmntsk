## MODIFIED Requirements

### Requirement: Supported databases are described by capability

The toolkit SHALL describe each supported database (PostgreSQL 9.5 or later, MySQL 8.0.17 or later, SQLite 3.35 or later) by the capabilities that change the SQL it runs:
- the bind marker for an argument position;
- how an identifier is quoted;
- whether a write can return rows, and whether skip-locked row selection is available;
- the collation that makes identifier comparison case-sensitive, ordering byte-wise, and trailing spaces significant;
- the column type for a UTC instant at microsecond precision, and the column type for an opaque JSON payload;
- the boolean literal, and the insert-or-update clause;
- the largest number of bind parameters one statement may carry;
- the longest identifier the database keeps without truncating it.

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

#### Scenario: Limits are described per database

- **WHEN** a caller asks each supported database for its bind-parameter and identifier-length limits
- **THEN** PostgreSQL reports 65535 parameters and 63 bytes, MySQL reports 65535 parameters and 64 characters, and SQLite reports 32766 parameters and no practical identifier limit

#### Scenario: MySQL identifier collation is byte-wise and pads nothing

- **WHEN** a caller asks MySQL for its identifier collation
- **THEN** the answer is `utf8mb4_0900_bin`, which orders by code point, compares case-sensitively and treats trailing spaces as significant

### Requirement: Schemas are published, never applied automatically

The toolkit SHALL render a published schema document for a chosen database into executable statements, with the caller's table prefix applied, in the order they must run. It SHALL NOT apply schema changes on its own in normal operation. A development runner SHALL apply rendered statements only when explicitly invoked, and a failure SHALL name the statement that failed.

Rendering SHALL refuse, as a configuration error, a prefix containing anything other than ASCII letters, digits and underscores. It SHALL also refuse a prefix that makes any name the document creates longer than the database's identifier limit. A refused prefix SHALL produce no statements.

#### Scenario: Schema rendered with a prefix

- **WHEN** a schema document is rendered for a database with the prefix `acme_`
- **THEN** every table the document creates carries the prefix, comments and blank lines are dropped, and statements are returned in document order

#### Scenario: Nothing is applied implicitly

- **WHEN** a store built on the toolkit is constructed against a database whose schema is missing
- **THEN** no schema statement is executed

#### Scenario: Failed statement is named

- **WHEN** the development runner applies a statement the database rejects
- **THEN** it stops and returns an error that includes the rejected statement's text

#### Scenario: Unsafe prefix is refused

- **WHEN** a schema document is rendered with a prefix containing a double quote, a semicolon or a space
- **THEN** rendering returns a configuration error and no statements

#### Scenario: Over-long prefix is refused

- **WHEN** a schema document is rendered for PostgreSQL with a prefix that makes one of its index names longer than 63 bytes
- **THEN** rendering returns a configuration error naming the name that would be truncated and the limit

### Requirement: Live schema verification reports every discrepancy

The toolkit SHALL compare a live database against a caller-supplied expectation of tables, columns, case-sensitive identifier columns and named secondary indexes. It SHALL report every discrepancy found in one error rather than the first, and SHALL classify that error as a configuration problem.

A table that is missing altogether SHALL be reported once, without also reporting each of its columns or indexes. On every supported database, an identifier column whose effective collation is not the database's identifier collation SHALL be reported. This includes a column that relies on a database default that differs from it. Where the database does not report collations through its column introspection, the toolkit SHALL read the collation from the column's declared definition.

#### Scenario: Several problems reported together

- **WHEN** verification runs against a database missing one table, one column of another table, and one required index
- **THEN** a single error lists all three discrepancies and is classified as a configuration problem

#### Scenario: Wrong collation on an identifier column

- **WHEN** an identifier column on MySQL carries a case-insensitive collation, or the collation this toolkit previously published
- **THEN** verification reports that column's collation as a discrepancy

#### Scenario: PostgreSQL identifier column without the byte-wise collation

- **WHEN** an identifier column on PostgreSQL is declared without `COLLATE "C"` and so takes the database's default collation
- **THEN** verification reports that column's collation as a discrepancy

#### Scenario: SQLite identifier column declared case-insensitive

- **WHEN** an identifier column on SQLite is declared `COLLATE NOCASE`
- **THEN** verification reports that column's collation as a discrepancy

#### Scenario: Correct schema passes

- **WHEN** verification runs against a database created from the published schema for the same expectation and prefix
- **THEN** verification succeeds

### Requirement: Statements execute through one executor contract

The toolkit SHALL offer one contract for executing statements that every supported driver implements:
- A write SHALL report how many rows it matched.
- An empty statement SHALL do nothing and report zero rows.
- A read SHALL present its rows to the caller and release them afterwards, including when reading fails part-way.
- An execution error SHALL include the text of the statement that failed, and SHALL keep the driver's error reachable so that it can be classified.
- An executor SHALL render statements with the bind marker its driver requires, so that a statement built for it never reaches the database with unbound markers.
- Inside a transaction, each call SHALL run under the context passed to that call.

#### Scenario: Rows matched are reported

- **WHEN** an update matching two rows runs through an executor
- **THEN** the executor reports two rows

#### Scenario: Rows released after a failed scan

- **WHEN** the caller's row handling returns an error on the first row
- **THEN** the executor returns that error and the rows are released

#### Scenario: Placeholders match the driver

- **WHEN** a statement is built for an executor whose driver rewrites `?` markers itself
- **THEN** the statement carries `?` markers and every argument is bound on each supported database

#### Scenario: Cancelled per-call context inside a transaction

- **WHEN** a transactional scope is open and a query is run with a context derived from the scope's context and then cancelled
- **THEN** the query fails with an error matching `context.Canceled` on every executor, including GORM

## ADDED Requirements

### Requirement: Driver errors are classified by code

The toolkit SHALL offer a classification of driver errors into these classes:
- unique violation;
- serialization failure;
- deadlock;
- busy or locked;
- value too long;
- unclassified.

It SHALL decide the class from the error code the driver reports (the SQLSTATE, the SQLite extended result code, or the MySQL error number), never from the message text. It SHALL recognise the errors of every driver the toolkit supports, without importing any driver. A wrapped driver error SHALL be classified the same as the bare one. An error the toolkit does not recognise, including nil, SHALL be unclassified.

The default classification SHALL be replaceable by the caller. A store or executor configured with the caller's classification SHALL use it in place of the default.

#### Scenario: Unique violation on each database

- **WHEN** an insert collides with an existing primary key through each of the seven driver and database combinations
- **THEN** the returned error is classified as a unique violation

#### Scenario: Serialization failure and deadlock

- **WHEN** PostgreSQL aborts a transaction with SQLSTATE `40001` or `40P01`, or MySQL aborts one with error 1213
- **THEN** the error is classified as a serialization failure or a deadlock respectively

#### Scenario: SQLite busy snapshot

- **WHEN** SQLite rejects a write with `SQLITE_BUSY` or `SQLITE_BUSY_SNAPSHOT`
- **THEN** the error is classified as busy or locked

#### Scenario: Value too long

- **WHEN** MySQL rejects a value with error 1406, or PostgreSQL with SQLSTATE `22001`
- **THEN** the error is classified as value too long

#### Scenario: Message text is not consulted

- **WHEN** a plain error whose message reads like a duplicate-key error, but that carries no driver code, is classified
- **THEN** it is unclassified

#### Scenario: Caller-supplied classification

- **WHEN** a caller supplies a classification that maps a driver-specific error code to unique violation
- **THEN** an error carrying that code is classified as a unique violation, and the default classification is not consulted
