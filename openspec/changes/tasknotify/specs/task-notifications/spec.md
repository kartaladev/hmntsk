## Purpose

Defines how human task events become per-user notifications: who is offered a task, who is told it was taken or assigned, when a task's notifications close, what links they carry, and how the projection stays correct when events are redelivered, retried or reordered.

## ADDED Requirements

### Requirement: Task events are projected durably and at most once per event and recipient

The system SHALL turn recorded task events into notifications through the durable event delivery path, so that a crash, a redeploy or a failed attempt never loses a notification an event should produce. Redelivering an event SHALL NOT create a second notification for any recipient. A failure that another attempt might fix SHALL be retried. A failure no attempt can fix SHALL be reported to the host and SHALL NOT cause the event to be abandoned for other destinations.

#### Scenario: A redelivered event creates no duplicates

- **WHEN** a pooled task's creation event is delivered twice
- **THEN** each eligible candidate has exactly one offer for that task

#### Scenario: A crash after recording the event still notifies

- **WHEN** a task is created and the process stops before any notification is written, and delivery runs after a restart
- **THEN** the eligible candidates receive their offers

#### Scenario: A transient failure is retried

- **WHEN** the notification store is unavailable while a claim event is delivered, and available on the next attempt
- **THEN** after the next attempt the claim's notifications exist exactly as if the first attempt had succeeded

#### Scenario: An unfixable failure does not dead-letter the event for other destinations

- **WHEN** projecting an event fails in a way no retry can change, and a webhook destination is configured beside the projection
- **THEN** the failure is reported to the host, and the event is still delivered to the webhook

### Requirement: The acting user is not notified of their own action

The system SHALL NOT address a notification produced by an event to the actor who performed that event.

#### Scenario: A creator who is also a candidate

- **WHEN** alice creates a task whose pool includes alice and bob
- **THEN** bob is offered the task and alice is not

### Requirement: Creating a task offers or assigns it

When a task is created into its pool, the system SHALL offer it to every actor eligible for it at that moment, with group membership expanded and exclusions removed. When a task is created reserved for one actor, the system SHALL notify that actor that the task is assigned to them.

#### Scenario: A pooled task is offered to its candidates

- **WHEN** a task is created with candidate users alice and group approvers (bob, carol), excluding carol
- **THEN** alice and bob each have an active offer for the task, and carol has none

#### Scenario: A reserved task is assigned

- **WHEN** a task is created reserved for dave
- **THEN** dave has an active assigned notification for the task

### Requirement: Claiming a task closes its offers and tells the other candidates

When a task is claimed, the system SHALL close every offer for that task, the claimant's included, with the reason "taken". It SHALL notify every other recipient whose offer this closed that the task was taken, naming who claimed it. The claimant SHALL NOT receive a taken notification. Recipients whose offer was already closed SHALL NOT be notified again.

#### Scenario: Others learn the task was taken

- **WHEN** alice, bob and carol hold offers for a task and carol claims it
- **THEN** all three offers are closed with reason "taken", and alice and bob each have an active taken notification naming carol, and carol has none

#### Scenario: A read offer still yields a taken notification

- **WHEN** alice has read her offer, and bob claims the task
- **THEN** alice's offer is closed, and she has an active taken notification

#### Scenario: Taken notifications survive a retried claim

- **WHEN** projecting a claim fails after its offers were closed, and the claim is delivered again
- **THEN** the other candidates each have exactly one taken notification

### Requirement: Releasing a task offers it again and retires stale notifications

When a task is released back to its pool, the system SHALL close the task's taken and assigned notifications and SHALL offer the task to every actor eligible for it, except the actor who released it.

#### Scenario: Released work is offered again

- **WHEN** carol claims a task that alice and bob were offered, and later releases it
- **THEN** alice's and bob's taken notifications are closed, alice and bob each have a new active offer, and carol has no offer

#### Scenario: A released assignment is retired

- **WHEN** dave releases a task he was assigned at creation
- **THEN** his assigned notification is closed

### Requirement: Delegating a task moves the assignment

When a task is delegated, the system SHALL notify the new holder that the task is assigned to them, and SHALL close every other assigned notification for the task without notifying their recipients.

#### Scenario: The new holder is told

- **WHEN** dave, who was assigned a task, delegates it to erin
- **THEN** erin has an active assigned notification, dave's assigned notification is closed, and dave receives no new notification

### Requirement: Widening a claimable task offers it only to newly eligible actors

When escalation widens a task's pool while the task is in its pool, the system SHALL offer the task to every eligible actor who has no offer for it that is still active or read. When the task is held by someone, widening SHALL produce no offers.

#### Scenario: Only new candidates are offered

- **WHEN** alice holds an active offer for a pooled task and escalation adds group managers (frank)
- **THEN** frank has an active offer, and alice still has exactly one offer

#### Scenario: A held task is not offered on widening

- **WHEN** escalation widens the pool of a task that bob holds
- **THEN** no offer is created

### Requirement: A task reaching a closing status closes all its notifications

When a task reaches a closing status, the system SHALL close every notification for the task, of every kind, with a reason naming that status. By default the closing statuses SHALL be all final statuses: completed, failed, errored, cancelled and obsoleted. The host SHALL be able to choose a different set, which SHALL include completed and cancelled and SHALL contain only final statuses. A set that breaks either rule SHALL be refused when the projection is built.

#### Scenario: Completion closes everything

- **WHEN** a task with active offers, taken notifications and an assigned notification is completed
- **THEN** every one of them is closed with reason "completed"

#### Scenario: A narrowed set leaves failed tasks' notifications open

- **WHEN** the host sets the closing statuses to completed and cancelled, and a task fails
- **THEN** the task's notifications are unchanged

#### Scenario: A set missing cancelled is refused

- **WHEN** the host sets the closing statuses to completed and failed
- **THEN** building the projection fails with a configuration error

#### Scenario: A non-final status is refused

- **WHEN** the host sets the closing statuses to completed, cancelled and ready
- **THEN** building the projection fails with a configuration error

### Requirement: Late and reordered events never reopen a task's notifications

The system SHALL order a task's notifications by the task version its events carry. An event older than one already projected SHALL NOT create or reopen a notification that the newer event closed.

#### Scenario: A retried creation after the claim

- **WHEN** a task's creation event fails and is retried after its claim event was projected
- **THEN** no active offer exists for the task

#### Scenario: A retried claim after the task closed

- **WHEN** a task's claim event is retried after its completion was projected
- **THEN** no taken notification is active for the task

#### Scenario: A late claim after a release

- **WHEN** a release is projected before the claim that preceded it
- **THEN** the offers from the release stay active, and no taken notification is active

### Requirement: Notifications link to the task and to where its work is done

Every notification the system produces SHALL carry a link to the task's details. When the task's type declares a route to where its work is done, the notification SHALL also carry a contextual link expanded for the task. Values inserted into links SHALL be inserted as they are, without escaping. The host SHALL be able to replace how both links are built.

#### Scenario: Both links are present

- **WHEN** a task of a type whose route is "/invoices/{correlation.ownerRef}/approve?task={task.id}" and whose owner reference is INV-42 is offered
- **THEN** the offer carries a task link to the task's details and a contextual link to "/invoices/INV-42/approve?task=<task id>"

#### Scenario: A type without a route

- **WHEN** a task of a type that declares no route is offered
- **THEN** the offer carries a task link and no contextual link

#### Scenario: A host-built task link

- **WHEN** the host configures the task link to point at its own web application
- **THEN** notifications carry the host's task link

### Requirement: Other transitions change nothing by default, and the rules are replaceable

By default, starting, suspending, resuming, and escalating without widening SHALL NOT create or close notifications. The host SHALL be able to replace the rules that decide what each event opens and closes, and to derive its rules from the defaults.

#### Scenario: Suspension leaves notifications alone

- **WHEN** a pooled task with active offers is suspended
- **THEN** the offers are unchanged

#### Scenario: A host notifies the creator on completion

- **WHEN** the host supplies rules that add a notification for the creator on completion to the default rules, and a task is completed
- **THEN** the creator has that notification, and every other notification for the task is closed as by default

### Requirement: Group membership is resolved when a notification is written

The system SHALL expand candidate groups into members when it writes the notifications for an event. An actor who joins a candidate group afterwards SHALL NOT receive a notification for that earlier event. This limit SHALL be documented.

#### Scenario: A later group member is not offered retroactively

- **WHEN** a task is offered to group approvers, and gina joins approvers afterwards
- **THEN** gina has no offer for the task, and the task still appears among the tasks she may claim
