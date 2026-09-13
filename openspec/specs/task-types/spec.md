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

A registered task type SHALL carry an input JSON Schema, an output JSON Schema, a default priority, a default deadline interval, a default escalation policy, default assignment configuration, and metadata. Metadata SHALL be a set of string keys and string values that the engine stores and returns unchanged and never interprets.

#### Scenario: Defaults are applied at creation

- **WHEN** a task is created without specifying priority or deadline
- **THEN** the stored task carries the priority and the deadline derived from its type's defaults

#### Scenario: Schemas are retrievable by type name

- **WHEN** a client requests the schemas for a registered type
- **THEN** the input and output JSON Schemas are returned as supplied at registration

#### Scenario: Metadata is returned unchanged

- **WHEN** a type is registered with metadata and later looked up, including after being persisted and read back from the database
- **THEN** the metadata is returned with exactly the keys and values supplied

#### Scenario: Metadata takes part in conflict detection

- **WHEN** a host registers a type name already registered with identical schemas and defaults but different metadata
- **THEN** registration fails with an error identifying the conflicting type name

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

### Requirement: Well-known metadata keys are documented conventions

The system SHALL define, as documented constants, well-known metadata keys for linking a task to where its work is done:

- a **form key**, naming a form the client knows how to render;
- a **route template**, a link whose placeholders are replaced by the task's identifier, its type and its correlation values.

Keys under the reserved `hmntsk.` prefix SHALL be the only keys the library defines. Every other key belongs to the host. A host that does not use the well-known keys SHALL lose nothing else.

The system SHALL offer, as an optional helper, expansion of a route template for a given task. It SHALL leave any placeholder it does not recognise untouched.

#### Scenario: A route template expands for a task

- **WHEN** a type carries the route template `/invoices/{correlation.ownerRef}/approve?task={task.id}` and it is expanded for a task whose owner reference is "INV-42"
- **THEN** the result is `/invoices/INV-42/approve?task=` followed by that task's identifier

#### Scenario: Unknown placeholders are left alone

- **WHEN** a route template containing `{tenant}` is expanded for a task
- **THEN** `{tenant}` remains in the result unchanged

#### Scenario: Host-defined keys coexist with well-known keys

- **WHEN** a type is registered with the well-known form key and a host key "acme.icon"
- **THEN** both are stored and returned, and the engine treats neither differently
