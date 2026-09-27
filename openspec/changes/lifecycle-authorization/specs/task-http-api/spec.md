## MODIFIED Requirements

### Requirement: The API covers the full task lifecycle

The system SHALL expose creating, cancelling, reading, querying, claiming, releasing, starting, saving progress, completing, failing, delegating, suspending, resuming and escalating a task.

Every operation SHALL have a route. The authorization this capability describes decides who may use each route. By default the escalation route refuses every client, and a host enables it through its operation policy.

#### Scenario: Every operation is reachable

- **WHEN** a client exercises each lifecycle operation over HTTP, as an actor the engine's rules permit, against a contract whose host permits escalation
- **THEN** each has a route and produces the same result as the equivalent direct invocation

#### Scenario: Escalation is withheld by default

- **WHEN** a client escalates a task over HTTP and the host supplied no operation policy
- **THEN** the response status is `403` and the task is unchanged

## ADDED Requirements

### Requirement: Lifecycle operations and creates require an acting user

The system SHALL refuse with `403` every lifecycle operation route and every create for which no acting user is established.

The refusal SHALL come before the request body is used and before the task is looked up. An anonymous caller SHALL therefore receive the same response whether the task exists or not and whatever version it sent, and the response SHALL NOT carry the task's current version. No host policy SHALL be able to permit an anonymous lifecycle operation or create.

#### Scenario: An anonymous cancel changes nothing

- **WHEN** a client with no established acting user sends `DELETE /tasks/{id}` for a `READY` task
- **THEN** the response status is `403` and the task remains `READY`

#### Scenario: An anonymous stale-version call does not reveal the task

- **WHEN** a client with no established acting user claims an existing task with a stale version, and then claims an unknown task identifier with the same version
- **THEN** both responses have the same status, `403`, and neither body contains a current version

#### Scenario: An anonymous create is refused

- **WHEN** a client with no established acting user creates a task
- **THEN** the response status is `403` and no task is stored

### Requirement: Lifecycle operations are authorized, escalation withheld by default

The system SHALL put every lifecycle operation an HTTP client invokes to an operation policy, before the engine is asked. The policy SHALL receive the acting user, the operation and the task identifier, but not the task. The engine then applies its own actor rules to whatever the policy permits, and its refusals SHALL answer `403`.

By default the policy SHALL refuse escalation and permit every other operation to reach the engine.

The host SHALL be able to replace the default wholesale, and it SHALL be able to permit every operation explicitly. Supplying an empty policy SHALL be refused when the contract is constructed, before any request is served. A policy's refusal SHALL answer `403` with the policy's message, whatever the policy's error.

#### Scenario: A stranger cannot cancel over HTTP

- **WHEN** a client acting as an actor who neither created, holds nor is a candidate for a `READY` task sends `DELETE /tasks/{id}`
- **THEN** the response status is `403` and the task remains `READY`

#### Scenario: A non-candidate cannot suspend a pooled task over HTTP

- **WHEN** a client acting as an actor who is neither the creator nor eligible for a `READY` task suspends it
- **THEN** the response status is `403` and the task remains `READY`

#### Scenario: The creator cancels over HTTP

- **WHEN** a client acting as the task's creator sends `DELETE /tasks/{id}` for a live task
- **THEN** the response status is `200` and the task's status is `EXITED`

#### Scenario: A host lets operators escalate

- **WHEN** the host supplies an operation policy permitting the actor "ops" to escalate, and a client acting as "ops" escalates a `READY` task whose policy widens its pool
- **THEN** the response status is `200` and the pool is widened

#### Scenario: A host policy refuses what the engine would allow

- **WHEN** the host supplies an operation policy refusing every cancellation, and the task's creator cancels it
- **THEN** the response status is `403`, the refusal carries the policy's reason, and the task is unchanged

#### Scenario: The host opts out of operation authorization

- **WHEN** the host explicitly permits every operation, and a client acting as an actor the engine permits escalates a task
- **THEN** the response status is `200`

#### Scenario: An empty operation policy is a wiring mistake

- **WHEN** a host constructs the contract replacing the operation policy with no policy at all
- **THEN** construction fails with a configuration error and no route is served

### Requirement: Creates are authorized, plain creates only by default

The system SHALL put every create to a create policy, before the engine is asked. The policy SHALL receive the acting user and the full create request.

By default the policy SHALL permit a create that carries only a type, input, correlation, priority and a deadline or due date. It SHALL refuse with `403` a create that carries a client-chosen identifier, a candidate pool, an escalation policy or a callback, and the refusal SHALL name each such field.

The host SHALL be able to replace the default wholesale, and it SHALL be able to permit every create explicitly. Supplying an empty policy SHALL be refused when the contract is constructed. The acting user SHALL be recorded as the task's creator.

#### Scenario: A plain create is accepted

- **WHEN** a client acting as "owner" creates a task with a type, input, correlation, priority and deadline, and the host supplied no create policy
- **THEN** the response status is `201` and the task's creator is "owner"

#### Scenario: A create carrying a callback is refused by default

- **WHEN** a client acting as "owner" creates a task carrying a callback address, and the host supplied no create policy
- **THEN** the response status is `403`, the message names the callback, and no task is stored

#### Scenario: A create choosing its own candidates is refused by default

- **WHEN** a client acting as "owner" creates a task carrying a candidate pool and an escalation policy, and the host supplied no create policy
- **THEN** the response status is `403`, the message names both fields, and no task is stored

#### Scenario: A create choosing its own identifier is refused by default

- **WHEN** a client acting as "owner" creates a task carrying an identifier, and the host supplied no create policy
- **THEN** the response status is `403` and no task is stored

#### Scenario: A host lets a trusted service configure its tasks

- **WHEN** the host supplies a create policy permitting overrides for the actor "billing-service", and a client acting as "billing-service" creates a task carrying an identifier, a candidate pool and a callback
- **THEN** the response status is `201` and the task carries them

#### Scenario: An empty create policy is a wiring mistake

- **WHEN** a host constructs the contract replacing the create policy with no policy at all
- **THEN** construction fails with a configuration error and no route is served

### Requirement: Lifecycle and create responses honour the read policy

After a lifecycle operation or create succeeds, the system SHALL put the resulting task to the single-task read policy for the acting user before rendering it.

When the read policy permits, the response SHALL carry the full task. When it refuses, or when eligibility cannot be determined, the response SHALL keep its success status and SHALL carry only a receipt: the task's identifier, status and version. The operation has taken effect either way.

#### Scenario: A participant receives the task

- **WHEN** a candidate claims a pooled task
- **THEN** the response status is `200` and the body is the full task, including its input

#### Scenario: A permitted non-participant receives only a receipt

- **WHEN** the host's operation policy permits "ops" to escalate, "ops" is not a participant of the task, and "ops" escalates it under the default read policy
- **THEN** the response status is `200` and the body carries the task's identifier, status and version, but not its input, correlation or callback

#### Scenario: A host read policy restores the full body

- **WHEN** the host's read policy permits "ops" to read every task, and "ops" escalates a task
- **THEN** the response body is the full task
