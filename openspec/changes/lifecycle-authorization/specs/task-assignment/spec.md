## MODIFIED Requirements

### Requirement: Eligibility is enforced on every actor-driven operation

The system SHALL verify the acting actor's eligibility on claim and delegate, and SHALL verify that the acting actor is the current assignee on release, start, save progress, complete and fail.

On suspend and resume the system SHALL, by default:
- when somebody holds the task, permit only the current assignee;
- when nobody holds the task, permit only the task's creator or an actor eligible for it (a candidate user, or a member of a candidate group, and not excluded).

On cancel the system SHALL, by default, permit only the task's creator. On escalate it SHALL, by default, permit any named actor.

Eligibility for suspend and resume is decided by the same rule a claim applies.

The defaults for suspend, resume, cancel and escalate SHALL be replaceable by the host's lifecycle policy. The rules for claim, release, start, save progress, complete, fail and delegate are not replaceable.

#### Scenario: Non-assignee cannot complete

- **WHEN** an actor who is not the assignee attempts to complete a reserved task
- **THEN** the operation is refused with an authorisation error and the task is unchanged

#### Scenario: Ineligible actor cannot claim

- **WHEN** an actor outside the candidate pool attempts to claim a `READY` task
- **THEN** the operation is refused with an authorisation error and the task remains `READY`

#### Scenario: Non-candidate cannot suspend a pooled task

- **WHEN** an actor who is neither the creator nor eligible for a `READY` task suspends it
- **THEN** the operation is refused with an authorisation error and the task remains `READY`

#### Scenario: Non-candidate cannot resume a pooled task

- **WHEN** a candidate suspends a `READY` task and an actor who is neither the creator nor eligible then resumes it
- **THEN** the resume is refused with an authorisation error and the task remains `SUSPENDED`

#### Scenario: A candidate suspends a pooled task

- **WHEN** a member of a `READY` task's candidate group suspends it
- **THEN** the task becomes `SUSPENDED`

#### Scenario: The creator suspends a pooled task

- **WHEN** the creator of a `READY` task, who is not a candidate, suspends it
- **THEN** the task becomes `SUSPENDED`

#### Scenario: Only the holder suspends a held task

- **WHEN** a candidate who does not hold a reserved task suspends it
- **THEN** the operation is refused with an authorisation error and the task remains `RESERVED`
