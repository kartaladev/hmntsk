## MODIFIED Requirements

### Requirement: A task type carries its schemas and defaults

A registered task type SHALL carry an input JSON Schema, an output JSON Schema, a default priority, a default deadline interval, a default escalation policy, default assignment configuration, and metadata. Metadata SHALL be a set of string keys and string values that the engine stores and returns unchanged and never interprets.

A type registered without a default priority SHALL carry the documented default priority (`5`, the middle of the range `0`..`10`). It SHALL NOT be treated as the most urgent priority. A host SHALL be able to register any priority in the range, including the most urgent (`0`), as the type's default, and a type looked up after registration SHALL report the default priority it applies.

#### Scenario: Defaults are applied at creation

- **WHEN** a task is created without specifying priority or deadline
- **THEN** the stored task carries the priority and the deadline derived from its type's defaults

#### Scenario: A type that names no priority gets the documented default

- **WHEN** a host registers a type without a default priority and a task of that type is created without a priority
- **THEN** the stored task carries priority `5`, not `0`, and looking the type up reports a default priority of `5`

#### Scenario: A type may default to the most urgent priority

- **WHEN** a host registers a type whose default priority is explicitly `0` and a task of that type is created without a priority
- **THEN** the stored task carries priority `0`

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

The system SHALL refuse an override it cannot honour with a validation error, and SHALL store no task. It SHALL NOT replace such an override with the type's default. In particular:
- a priority outside the range `0`..`10` SHALL be refused;
- a negative deadline interval SHALL be refused;
- an escalation policy that is not valid (see "Escalation policies are validated where they are supplied") SHALL be refused.

An explicit deadline interval of zero SHALL mean the task has no deadline, even when its type defines a default deadline.

#### Scenario: Explicit deadline wins

- **WHEN** a task is created with an explicit due date while its type defines a default deadline interval
- **THEN** the stored task carries the explicit due date

#### Scenario: An in-range priority override is applied

- **WHEN** a task is created with priority `3` for a type whose default priority is `5`
- **THEN** the stored task carries priority `3`

#### Scenario: An out-of-range priority is refused

- **WHEN** a task is created with priority `99`, or with priority `-1`
- **THEN** creation fails with a validation error and no task is stored

#### Scenario: A negative deadline is refused, not replaced

- **WHEN** a task is created with a deadline interval of minus one hour for a type whose default deadline is 24 hours
- **THEN** creation fails with a validation error and no task is stored

#### Scenario: A zero deadline means no deadline

- **WHEN** a task is created with an explicit deadline interval of zero for a type whose default deadline is 24 hours
- **THEN** the stored task has no due date

#### Scenario: An invalid escalation override is refused

- **WHEN** a task is created with an escalation policy whose action is `widen` in lower case
- **THEN** creation fails with a validation error and no task is stored

### Requirement: Validation scope differs between progress and completion

Saving progress SHALL validate only the shape of the supplied data — that its fields and types are consistent with the input schema — and SHALL NOT require completeness. Completing a task SHALL validate the supplied output fully against the output schema, including required fields.

The shape check SHALL never be stricter than full validation: a draft that satisfies the full input schema SHALL be accepted by a progress save. This SHALL hold for schemas that use negation (`not`), exclusive alternatives (`oneOf`) and conditionals (`if`/`then`/`else`).

#### Scenario: Half-filled form is saved

- **WHEN** the assignee saves progress containing only some of the input schema's required fields
- **THEN** the save succeeds and the partial data is persisted

#### Scenario: Wrongly typed field is refused on save

- **WHEN** the assignee saves progress where a field's type contradicts the input schema
- **THEN** the save fails with a validation error identifying the field, and nothing is persisted

#### Scenario: Incomplete output is refused on completion

- **WHEN** an actor completes a task with output missing a field the output schema requires
- **THEN** completion fails with a validation error and the task's status is unchanged

#### Scenario: A draft valid under a negated requirement is saved

- **WHEN** the input schema is `{"type":"object","not":{"required":["forbidden"]}}` and the assignee saves a draft `{"a":1}`
- **THEN** the save succeeds

#### Scenario: A draft matching exactly one alternative is saved

- **WHEN** the input schema is `{"type":"object","oneOf":[{"required":["a"]},{"required":["b"]}]}` and the assignee saves a draft `{"a":1}`
- **THEN** the save succeeds

#### Scenario: A conditional that does not apply is not enforced

- **WHEN** the input schema requires `n` to be at least `10` only if `kind` is present, and the assignee saves a draft `{"n":3}` with no `kind`
- **THEN** the save succeeds

## ADDED Requirements

### Requirement: Escalation policies are validated where they are supplied

The system SHALL validate an escalation policy wherever one is supplied: as a type's default at registration, and as a per-task override at creation. A policy SHALL be valid only when all of the following hold:
- its action is one of the defined actions: notify (no action), widen, or supersede, matched exactly and case-sensitively;
- its escalation cap is zero (no cap) or positive;
- a widening policy names at least one user or group to add;
- a policy that does not widen names no users or groups to add.

An invalid default policy SHALL fail registration with a configuration error. An invalid override SHALL fail creation with a validation error.

#### Scenario: A valid widening policy registers

- **WHEN** a host registers a type whose default policy widens to the group `mgr`
- **THEN** registration succeeds

#### Scenario: An action in the wrong case is refused at registration

- **WHEN** a host registers a type whose default policy's action is `widen` in lower case
- **THEN** registration fails with a configuration error and the type is not registered

#### Scenario: An unknown action is refused at registration

- **WHEN** a host registers a type whose default policy's action is `PAGE_ONCALL`
- **THEN** registration fails with a configuration error

#### Scenario: A negative cap is refused at registration

- **WHEN** a host registers a type whose default policy's escalation cap is `-1`
- **THEN** registration fails with a configuration error

#### Scenario: A widening policy with nothing to add is refused

- **WHEN** a host registers a type whose default policy widens but names no users and no groups
- **THEN** registration fails with a configuration error

#### Scenario: Additions on a non-widening policy are refused

- **WHEN** a host registers a type whose default policy only notifies but names groups to add
- **THEN** registration fails with a configuration error
