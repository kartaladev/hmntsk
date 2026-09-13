# notification-retention Specification

## Purpose
Defines how stored notifications are kept from growing without bound: a per-recipient count bound and an age bound, the strategy that decides whether unread notifications may ever be removed, and the host-driven pruning that enforces both.

## Requirements

### Requirement: Pruning runs only when the host drives it

The system SHALL NOT prune on its own. A pruning pass SHALL run only when the host invokes it, whether once, from a long-running loop the host starts, or from the host's own scheduler. Stopping a loop the host started SHALL end it without leaking any background work.

#### Scenario: Constructing a pruner starts nothing

- **WHEN** a host constructs a pruner and never invokes it
- **THEN** no notification is ever deleted, and no background work is running

#### Scenario: A loop stops when the host cancels it

- **WHEN** a host runs the pruning loop and cancels its context
- **THEN** the loop returns, and no background work remains

### Requirement: Retention bounds count per recipient and age, with defaults

The system SHALL bound each recipient's notifications by count and bound inactive notifications by age. The two bounds SHALL be combinable. With no configuration, a pruner SHALL apply the documented default bounds:

- at most **500** notifications per recipient;
- inactive (READ or CLOSED) notifications kept for at most **90 days** after they became inactive.

The host SHALL be able to set either bound, and SHALL be able to remove either bound explicitly. Every recipient SHALL be subject to the same bounds.

#### Scenario: Default bounds apply with no configuration

- **WHEN** a host runs a pruner constructed with no options
- **THEN** the pass enforces 500 notifications per recipient and deletes inactive notifications that became inactive more than 90 days ago

#### Scenario: Both bounds apply together

- **WHEN** a pruner bounds recipients to 100 notifications and inactive notifications to 30 days, and alice has 50 notifications of which 10 became inactive 40 days ago
- **THEN** the pass deletes alice's 10 old inactive notifications, and nothing else of hers

#### Scenario: A host removes the age bound

- **WHEN** a pruner is configured without an age bound
- **THEN** no notification is ever deleted for its age, and the count bound still applies

### Requirement: Age never removes an active notification

The system SHALL measure age from the moment a notification became inactive, and SHALL apply the age bound only to READ and CLOSED notifications. The age bound SHALL NOT delete an ACTIVE notification, whatever the retention strategy.

#### Scenario: A long-lived active notification survives the age bound

- **WHEN** an ACTIVE notification was created 200 days ago and the age bound is 90 days
- **THEN** a pruning pass does not delete it

#### Scenario: Age counts from becoming inactive

- **WHEN** a notification created 120 days ago was read 10 days ago, and the age bound is 90 days
- **THEN** a pruning pass does not delete it

### Requirement: The retention strategy decides whether the count bound may evict active notifications

The system SHALL offer two retention strategies:

- **Evict oldest active (default).** To bring a recipient within the count bound, the system SHALL first delete that recipient's oldest inactive notifications. Only when none are left SHALL it delete the recipient's oldest ACTIVE notifications until the bound holds.
- **Retain active.** The system SHALL delete only inactive notifications. A recipient whose ACTIVE notifications alone exceed the bound SHALL keep all of them. This limit SHALL be documented.

Under either strategy, inactive notifications SHALL always be deleted before any active one, and the oldest before the newer.

#### Scenario: Inactive notifications go first

- **WHEN** the count bound is 5, the strategy is the default, and alice has 4 ACTIVE and 3 READ notifications
- **THEN** a pruning pass deletes alice's 2 oldest READ notifications and no ACTIVE one

#### Scenario: The default strategy evicts the oldest active notifications when it must

- **WHEN** the count bound is 5, the strategy is the default, and alice has 8 ACTIVE notifications and nothing else
- **THEN** a pruning pass deletes alice's 3 oldest ACTIVE notifications

#### Scenario: Retain active never deletes an unread notification

- **WHEN** the count bound is 5, the strategy is retain active, and alice has 8 ACTIVE notifications and nothing else
- **THEN** a pruning pass deletes nothing of alice's

### Requirement: A pass reports what it removed, and bounds are approximate between passes

A pruning pass SHALL report how many inactive notifications it deleted for age, how many it deleted for the count bound, and, separately, how many ACTIVE notifications it evicted. Between passes, a recipient MAY exceed the count bound and inactive notifications MAY outlive the age bound. The system SHALL document that bounds hold only as of the end of a pass.

The system SHALL notify a recipient's realtime subscribers when a pass evicts any of their ACTIVE notifications.

#### Scenario: Evictions are reported separately

- **WHEN** a pass deletes 7 inactive notifications for age, 3 inactive and 2 ACTIVE for the count bound
- **THEN** its result reports 7 deleted for age, 3 deleted for count and 2 active evicted

#### Scenario: The bound can be exceeded until the next pass

- **WHEN** the count bound is 5, a pass has just left alice with 5 notifications, and 3 more are published
- **THEN** alice has 8 notifications until the next pass, which brings her back to 5

### Requirement: Contradictory retention configuration fails at construction

The system SHALL refuse, at construction and before any pass runs:

- a count bound of zero or less, or an age bound of zero or less;
- a retention strategy that is not one of the defined strategies;
- a bound both set and removed;
- a pruner with both bounds removed, because it could never prune anything.

#### Scenario: A zero count bound is refused

- **WHEN** a host constructs a pruner with a count bound of 0
- **THEN** construction fails with a configuration error and no pruner is returned

#### Scenario: A pruner with nothing to enforce is refused

- **WHEN** a host constructs a pruner with both the count bound and the age bound removed
- **THEN** construction fails with a configuration error

### Requirement: Close records outlive late redelivery, then expire

The system SHALL keep the record of the version at which a subject was closed at least as long as a documented watermark retention, **7 days** by default, and the host SHALL be able to change it. Once a subject has no notifications left and its close record is older than that retention, a pruning pass SHALL delete the record. The system SHALL document that a source redelivered after its subject's close record expired can create notifications again.

#### Scenario: A recent close record is kept

- **WHEN** a subject's notifications were all deleted and its close record is 2 days old
- **THEN** a pruning pass keeps the close record

#### Scenario: An expired close record of an empty subject is deleted

- **WHEN** a subject has no notifications and its close record is 8 days old, with the default watermark retention
- **THEN** a pruning pass deletes the close record
