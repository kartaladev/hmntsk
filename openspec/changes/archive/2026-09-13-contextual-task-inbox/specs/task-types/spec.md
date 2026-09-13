## MODIFIED Requirements

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

## ADDED Requirements

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
