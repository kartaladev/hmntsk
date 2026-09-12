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

The system SHALL support querying tasks by assignee, by eligibility, by status, by type and by correlation, and SHALL return results in stable pages.

#### Scenario: Paging is stable under concurrent writes

- **WHEN** a client pages through an inbox while other tasks are being created
- **THEN** no task already returned on an earlier page is returned again on a later one

#### Scenario: Mixed types in one response

- **WHEN** an actor's inbox contains tasks of several types
- **THEN** all are returned in one response with their payloads intact

### Requirement: Task type schemas are served to clients

The system SHALL expose the input and output schemas of a registered task type, so that a client can render a form for a task without knowing the type in advance.

#### Scenario: Client renders an unfamiliar task

- **WHEN** a client encounters a task of a type it does not recognise and requests that type's schemas
- **THEN** the input and output schemas are returned as registered

### Requirement: The host owns transport concerns

The system SHALL leave authentication, authorisation of the caller's identity, rate limiting, CORS and TLS to the host, and SHALL accept the acting actor as an input determined by the host.

#### Scenario: Actor is supplied by the host

- **WHEN** a request reaches a task route
- **THEN** the acting actor is taken from what the host's middleware established, and the engine performs no authentication of its own
