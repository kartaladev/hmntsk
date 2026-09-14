## ADDED Requirements

### Requirement: In-process consumers can be built from the engine they consume

The system SHALL let a host register, at construction, in-process consumers that are built from the engine being constructed, so that a consumer needing the engine does not have to be wired through a variable assigned after construction. The engine SHALL build them only once the rest of construction has succeeded. A failure to build them SHALL fail construction. In-process consumers SHALL be run in the order the host registered them, whether registered directly or built from the engine.

#### Scenario: A typed completion consumer is built from the engine

- **WHEN** a host registers a consumer builder that defines a typed task type on the engine and returns its completion consumer, and a task of that type is then completed
- **THEN** construction succeeds and the consumer receives the completion with its decoded output

#### Scenario: A failing builder fails construction

- **WHEN** a consumer builder reports an error
- **THEN** construction fails with that error and no engine is returned

#### Scenario: Registration order is dispatch order

- **WHEN** a host registers a consumer directly, then a consumer builder, then another consumer directly
- **THEN** each event is offered to the three consumers in that order

#### Scenario: A builder is not run for a refused configuration

- **WHEN** construction is refused because the durable event sink cannot join the host's transaction
- **THEN** no consumer builder is called
