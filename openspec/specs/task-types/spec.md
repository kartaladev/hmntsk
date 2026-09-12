# task-types Specification

## Purpose

Defines the task type registry, which holds everything that is true of a kind of work rather than of one piece of work — its payload schemas, its defaults and its validation rules — so that callers describe a task instance without restating its type's configuration on every call.

## Requirements

### Requirement: Task types must be registered before use

The system SHALL require every task type to be registered before a task of that type can be created. Creating a task whose type is not registered SHALL fail with a validation error.

#### Scenario: Unknown type is refused

- **WHEN** a caller creates a task with a type name that has not been registered
- **THEN** creation fails with a validation error naming the unknown type, and no task is stored

#### Scenario: Registration is available without Go type parameters

- **WHEN** a host registers a task type by supplying its configuration as data rather than as Go types
- **THEN** tasks of that type can be created, validated and served identically to a type registered with Go types

### Requirement: A task type carries its schemas and defaults

A registered task type SHALL carry an input JSON Schema, an output JSON Schema, a default priority, a default deadline interval, a default escalation policy, and default assignment configuration.

#### Scenario: Defaults are applied at creation

- **WHEN** a task is created without specifying priority or deadline
- **THEN** the stored task carries the priority and the deadline derived from its type's defaults

#### Scenario: Schemas are retrievable by type name

- **WHEN** a client requests the schemas for a registered type
- **THEN** the input and output JSON Schemas are returned as supplied at registration

### Requirement: Per-task values override type defaults

A caller SHALL be able to override a type's default priority, deadline, escalation policy and assignment configuration for an individual task at creation time.

#### Scenario: Explicit deadline wins

- **WHEN** a task is created with an explicit due date while its type defines a default deadline interval
- **THEN** the stored task carries the explicit due date

### Requirement: Registration conflicts fail at wiring time

Registering two different configurations under the same type name SHALL fail at registration, not at first use.

#### Scenario: Conflicting registration is rejected immediately

- **WHEN** a host registers the type name `approval` twice with different schemas
- **THEN** the second registration fails with an error identifying the conflicting type name

#### Scenario: Identical re-registration is accepted

- **WHEN** a host registers the same type name twice with identical configuration
- **THEN** registration succeeds and the type remains registered once

### Requirement: Validation scope differs between progress and completion

Saving progress SHALL validate only the shape of the supplied data — that its fields and types are consistent with the input schema — and SHALL NOT require completeness. Completing a task SHALL validate the supplied output fully against the output schema, including required fields.

#### Scenario: Half-filled form is saved

- **WHEN** the assignee saves progress containing only some of the input schema's required fields
- **THEN** the save succeeds and the partial data is persisted

#### Scenario: Wrongly typed field is refused on save

- **WHEN** the assignee saves progress where a field's type contradicts the input schema
- **THEN** the save fails with a validation error identifying the field, and nothing is persisted

#### Scenario: Incomplete output is refused on completion

- **WHEN** an actor completes a task with output missing a field the output schema requires
- **THEN** completion fails with a validation error and the task's status is unchanged

### Requirement: Payloads are stored without reinterpretation

The system SHALL store input and output payloads as the caller supplied them, preserving field order, number formatting and fields not described by the schema.

#### Scenario: Large integers survive a round trip

- **WHEN** a payload containing an integer too large for exact floating-point representation is stored and read back
- **THEN** the value read back is identical to the value supplied

#### Scenario: Unknown fields survive a round trip

- **WHEN** a payload containing fields absent from the schema is stored and read back
- **THEN** those fields are present and unchanged

### Requirement: Typed access is behaviourally identical to untyped access

The system SHALL offer a typed, compile-time-checked path for creating, completing, reading and handling events for a single task type. Operations performed through it SHALL produce results indistinguishable from the same operations performed through the untyped path.

#### Scenario: Typed and untyped creation agree

- **WHEN** a task is created through the typed path and an equivalent task through the untyped path
- **THEN** both stored tasks carry the same type, the same payload bytes and the same defaults

#### Scenario: Mixed-type queries remain available

- **WHEN** a client queries an actor's inbox containing tasks of several types
- **THEN** all matching tasks are returned regardless of type, with their payloads intact
