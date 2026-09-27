## ADDED Requirements

### Requirement: Create overrides the engine cannot honour are bad requests

Every framework binding SHALL answer `400` to a create request whose overrides cannot be honoured exactly, and SHALL store no task. This covers:
- a priority outside `0`..`10`;
- a negative `deadlineSeconds`;
- a `deadlineSeconds` too large to represent as an interval without overflow;
- an invalid escalation policy.

The contract SHALL NOT wrap, truncate or replace such a value with the type's default.

#### Scenario: An overflowing deadline is refused

- **WHEN** a client creates a task with `deadlineSeconds` of `18446744074`
- **THEN** the response is `400` and no task is stored

#### Scenario: A negative deadline is refused

- **WHEN** a client creates a task with `deadlineSeconds` of `-60` for a type that has a default deadline
- **THEN** the response is `400` and no task is stored

#### Scenario: An out-of-range priority is refused

- **WHEN** a client creates a task with `priority` of `-5`, or of `11`
- **THEN** the response is `400` and no task is stored

#### Scenario: An in-range override is accepted

- **WHEN** a client creates a task with `priority` of `3` and `deadlineSeconds` of `3600`
- **THEN** the response is `201` and the task carries priority `3` and a due date one hour after its creation
