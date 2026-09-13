## MODIFIED Requirements

### Requirement: Inbox queries are filterable and paginated

The system SHALL support querying tasks by assignee, by eligibility, by group, by status, by type and by correlation. It SHALL order results by any ordering the engine supports, and SHALL return results in stable pages.

#### Scenario: Paging is stable under concurrent writes

- **WHEN** a client pages through an inbox while other tasks are being created
- **THEN** no task already returned on an earlier page is returned again on a later one

#### Scenario: Mixed types in one response

- **WHEN** an actor's inbox contains tasks of several types
- **THEN** all are returned in one response with their payloads intact

#### Scenario: An ordered inbox

- **WHEN** a client queries its inbox requesting urgency ordering
- **THEN** tasks are returned most urgent first, in stable pages

#### Scenario: An unsupported ordering is a bad request

- **WHEN** a client requests an ordering the engine does not support
- **THEN** the response status is `400`

### Requirement: Task type schemas are served to clients

The system SHALL expose the input and output schemas and the metadata of a registered task type, so that a client can render a form or link to the business form for a task without knowing the type in advance.

#### Scenario: Client renders an unfamiliar task

- **WHEN** a client encounters a task of a type it does not recognise and requests that type's schemas
- **THEN** the input and output schemas are returned as registered

#### Scenario: Client links a task to its business form

- **WHEN** a client requests a task type registered with metadata
- **THEN** the metadata is returned exactly as registered

## ADDED Requirements

### Requirement: Query counts are served to clients

The system SHALL expose a count of the tasks an inbox query matches, accepting the same filters as the inbox query endpoint and subject to the same query authorization.

#### Scenario: A bucket badge

- **WHEN** a client requests the count of its available tasks
- **THEN** the response carries the number of tasks the equivalent inbox query matches

#### Scenario: A count is authorized like a query

- **WHEN** a client requests a count that query authorization would refuse as a query
- **THEN** the response status is `403`

### Requirement: The acting user can be named as `me`

The system SHALL resolve the value `me` for the candidate and assignee filters, on both the query and count endpoints, to the acting user the host established, before query authorization runs.

#### Scenario: My available tasks

- **WHEN** a client whose host established the actor "alice" queries with candidate `me`
- **THEN** the query runs as candidate "alice"

#### Scenario: No acting user

- **WHEN** a client queries with assignee `me` and the host established no actor
- **THEN** the response status is `403`

### Requirement: Inbox queries are authorized, self-only by default

The system SHALL authorize every inbox query and count against the acting user before running it.

By default it SHALL permit a query only when every candidate and assignee filter names the acting user, and SHALL refuse a query that filters by group, or that names neither a candidate nor an assignee. A refusal SHALL be `403`.

The host SHALL be able to replace this default with its own authorization policy, which then decides every query and count alone.

#### Scenario: Reading someone else's inbox is refused by default

- **WHEN** a client acting as "alice" queries with candidate "bob" and the host supplied no policy
- **THEN** the response status is `403` and no tasks are returned

#### Scenario: A team queue is refused by default

- **WHEN** a client queries by group and the host supplied no policy
- **THEN** the response status is `403`

#### Scenario: A host policy lets a supervisor see a team queue

- **WHEN** the host supplies a policy permitting "carol" to query the group "finance-approvers", and a client acting as "carol" does so
- **THEN** the group's queue is returned

#### Scenario: A host policy refuses what the default would allow

- **WHEN** the host supplies a policy refusing all queries on weekends, and a client queries its own inbox on a weekend
- **THEN** the response status is `403`
