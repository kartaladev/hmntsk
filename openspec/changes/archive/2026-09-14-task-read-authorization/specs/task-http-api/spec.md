## ADDED Requirements

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
