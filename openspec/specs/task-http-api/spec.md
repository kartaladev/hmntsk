# task-http-api Specification

## Purpose

Defines the HTTP contract through which task inboxes, end users and external callers interact with the engine, and requires that contract to be identical no matter which web framework the host has chosen.

## Requirements

### Requirement: One contract, identical across every framework binding

The system SHALL expose the same routes, request shapes, response shapes and status codes regardless of which supported web framework serves them, and SHALL assert this with one shared suite of behavioural cases executed against every binding.

#### Scenario: One suite, every binding

- **WHEN** the shared API suite is executed against each supported framework binding
- **THEN** every case passes with identical responses

#### Scenario: Switching frameworks does not change the contract

- **WHEN** a host replaces one supported framework binding with another
- **THEN** existing clients require no change

### Requirement: The contract does not assume a net/http-based framework

The system SHALL define its request handling independently of any one framework's request and response types, so that frameworks not built on the Go standard library's HTTP types are supported without translation through it.

#### Scenario: Non-standard-library framework is a first-class binding

- **WHEN** the API is served by a framework that does not use standard library request and response types
- **THEN** it passes the same shared suite as the others, with no conversion layer between the two models

### Requirement: The API covers the full task lifecycle

The system SHALL expose creating, cancelling, reading, querying, claiming, releasing, starting, saving progress, completing, failing, delegating, suspending, resuming and escalating a task.

#### Scenario: Every operation is reachable

- **WHEN** a client exercises each lifecycle operation over HTTP
- **THEN** each has a route and produces the same result as the equivalent direct invocation

### Requirement: Errors map to predictable status codes

The system SHALL map its error conditions to HTTP status codes consistently: a concurrent-modification conflict and an illegal transition to `409`, a failed eligibility or assignee check to `403`, an unknown task or route to `404`, a schema or request validation failure to `400`, and an unregistered task type to `400`.

#### Scenario: Stale write returns a conflict

- **WHEN** a client submits a change based on a version that is no longer current
- **THEN** the response is `409` and its body identifies the current version

#### Scenario: Non-assignee is forbidden

- **WHEN** an actor who is not the assignee attempts to complete a task
- **THEN** the response is `403` and the task is unchanged

#### Scenario: Unregistered type is a bad request

- **WHEN** a client creates a task with an unregistered type
- **THEN** the response is `400` and its body names the unknown type

### Requirement: Payloads pass through unmodified

The system SHALL deliver input and output payloads between client and storage without reformatting, reordering or reinterpreting them.

#### Scenario: Payload returned as supplied

- **WHEN** a client creates a task with a payload and then reads the task back
- **THEN** the payload returned is semantically identical to the one supplied, including fields not described by the schema and values outside exact floating-point range

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

### Requirement: The host owns transport concerns

The system SHALL leave authentication, authorisation of the caller's identity, rate limiting, CORS and TLS to the host, and SHALL accept the acting actor as an input determined by the host.

#### Scenario: Actor is supplied by the host

- **WHEN** a request reaches a task route
- **THEN** the acting actor is taken from what the host's middleware established, and the engine performs no authentication of its own

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

### Requirement: Reading a task is authorized, participants only by default

The system SHALL authorize every read of a single task, and every read of a task's history, against the acting user before returning anything about that task.

By default the system SHALL permit the read only when the acting user:
- holds the task;
- is eligible for it (named as a candidate user, or a member of a candidate group, and not excluded); or
- created it.

The system SHALL refuse every other read with `403`, returning nothing of the task.

The order of checks SHALL be:
1. A read with no acting user established SHALL be refused with `403` before the task is looked up, so an anonymous caller cannot learn whether a task exists.
2. With an acting user established, a read of a task that does not exist SHALL be `404`.
3. A read whose eligibility cannot be determined because group membership could not be resolved SHALL be `500`, not `403`.

The host SHALL be able to replace the default with its own policy, which then decides every single-task and history read alone. The host SHALL be able to permit every read explicitly. Supplying an empty policy SHALL be refused when the contract is constructed, before any request is served.

#### Scenario: The holder reads their task

- **WHEN** a client acting as "alice" reads a task that "alice" holds, and the host supplied no policy
- **THEN** the response status is `200` and the task is returned

#### Scenario: An eligible candidate reads a pooled task

- **WHEN** a client acting as "bob", a member of the task's candidate group, reads the pooled task, and the host supplied no policy
- **THEN** the response status is `200`

#### Scenario: The creator reads the task they asked for

- **WHEN** a client acting as the actor who created a task reads it, although that actor is not a candidate, and the host supplied no policy
- **THEN** the response status is `200`

#### Scenario: An outsider is refused

- **WHEN** a client acting as "carol", who neither holds, is eligible for, nor created the task, reads it, and the host supplied no policy
- **THEN** the response status is `403` and the body contains no task

#### Scenario: An outsider is refused the history too

- **WHEN** a client acting as "carol" reads the history of the same task
- **THEN** the response status is `403` and no transition records are returned

#### Scenario: An excluded candidate is refused

- **WHEN** a client acting as an actor who belongs to a candidate group but is excluded from the task reads it
- **THEN** the response status is `403`

#### Scenario: An anonymous read reveals nothing

- **WHEN** a client reads a task, existing or not, and the host established no acting user
- **THEN** the response status is `403` in both cases

#### Scenario: An unknown task is not found

- **WHEN** a client with an established acting user reads a task identifier that does not exist
- **THEN** the response status is `404`

#### Scenario: A directory failure is not a refusal

- **WHEN** a client reads a pooled task and group membership cannot be resolved
- **THEN** the response status is `500`, not `403`

#### Scenario: A host policy lets an auditor read any task

- **WHEN** the host supplies a policy permitting the actor "auditor" to read every task, and a client acting as "auditor" reads a task it has no part in
- **THEN** the response status is `200`

#### Scenario: A host policy refuses what the default would allow

- **WHEN** the host supplies a policy refusing reads of closed tasks, and the holder of a completed task reads it
- **THEN** the response status is `403` and the refusal carries the policy's reason

#### Scenario: A refusal is 403 whatever the policy's error

- **WHEN** a host policy refuses a read with an error that would otherwise classify as a validation failure
- **THEN** the response status is still `403`

#### Scenario: The host opts out of read authorization

- **WHEN** the host explicitly permits every read, and a client acting as "carol" reads a task it has no part in
- **THEN** the response status is `200`

#### Scenario: An empty policy is a wiring mistake

- **WHEN** a host constructs the contract replacing the read policy with no policy at all
- **THEN** construction fails with a configuration error and no route is served
