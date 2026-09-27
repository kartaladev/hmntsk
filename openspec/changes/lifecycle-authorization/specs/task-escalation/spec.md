## MODIFIED Requirements

### Requirement: Escalation is an ordinary lifecycle operation

Escalation SHALL be invocable directly as well as by the sweep, and SHALL pass through the same transition validation, history recording and event production as every other operation.

A direct escalation SHALL name its acting actor. The system SHALL apply the host's lifecycle policy to a direct escalation. The default policy permits any named actor.

A swept escalation SHALL be recorded under the sweeper's owner identifier, in both history and the escalation event. The system SHALL NOT put a swept escalation to the host's lifecycle policy: the escalation policy itself decides it, so a lifecycle policy cannot stop deadlines being enforced.

#### Scenario: Manual escalation

- **WHEN** an operator escalates a task directly, naming themselves as the actor
- **THEN** the escalation is applied, recorded in history under the operator and produces an escalation event, exactly as a swept escalation would

#### Scenario: Manual escalation without an actor

- **WHEN** a task is escalated directly with no acting actor
- **THEN** the operation fails with an authorisation error and the task is unchanged

#### Scenario: A swept escalation is attributed to the sweeper

- **WHEN** a sweep escalates an overdue task
- **THEN** the history record and the escalation event carry the sweeper's owner identifier as the actor

#### Scenario: A host policy does not stop the sweep

- **WHEN** the host supplies a lifecycle policy refusing every escalation, and a sweep runs over an overdue task
- **THEN** the task is escalated, while a direct escalation of another task is refused by the policy
