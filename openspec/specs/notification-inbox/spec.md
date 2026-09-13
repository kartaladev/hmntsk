# notification-inbox Specification

## Purpose
Defines durable, per-recipient notifications that know nothing about the domain that publishes them: how they are published without duplication, how they move between active, read and closed, how a subject's notifications are closed without a late source reopening them, and how a recipient reads them. It behaves identically on every supported store.

## Requirements

### Requirement: A notification belongs to exactly one recipient and is in one of three states

The system SHALL record each notification for exactly one recipient. It SHALL keep each notification in one of three states:

- **ACTIVE**: the recipient has not read it and nothing has closed it;
- **READ**: the recipient has read it;
- **CLOSED**: its subject moved on, with a closed reason recorded.

A notification SHALL never return to ACTIVE once it has left that state. The system SHALL treat a notification's kind, title, links and data as opaque, storing and returning them unchanged.

#### Scenario: A new notification is active

- **WHEN** a notification is published for a recipient
- **THEN** it is ACTIVE, with no read or closed time

#### Scenario: Opaque content is returned unchanged

- **WHEN** a notification is published with a kind, title, links by relation name and a JSON data payload
- **THEN** reading it back returns the kind, title, links and data exactly as published, the payload's field order and number literals included

#### Scenario: A read notification cannot become active again

- **WHEN** a READ notification is published again, or targeted by any other operation
- **THEN** it is not ACTIVE afterwards

### Requirement: Publishing is idempotent per source and recipient

The system SHALL identify a published notification by its source identifier together with its recipient. Publishing the same source for the same recipient again SHALL create nothing, SHALL leave the existing notification unchanged whatever its state, and SHALL NOT be an error. A publish result SHALL report which notifications were created, and how many were duplicates.

#### Scenario: A redelivered source creates no duplicate

- **WHEN** a source is published for recipients alice and bob, and the same source is published for alice and bob again
- **THEN** alice and bob each have exactly one notification from that source, and the second publish reports two duplicates and nothing created

#### Scenario: A redelivery does not undo a read

- **WHEN** alice reads a notification and its source is published for her again
- **THEN** the notification is still READ

#### Scenario: One source addresses many recipients

- **WHEN** one source is published for alice, bob and carol
- **THEN** each has their own notification, which each can read without affecting the others

### Requirement: A subject's notifications can be closed by kind and version

The system SHALL close the notifications of a subject in one operation, and SHALL let that operation:

- restrict closing to some kinds, or apply to every kind;
- close only notifications whose subject version is at or below a given version;
- spare one named recipient.

Closing SHALL move ACTIVE and READ notifications to CLOSED with the given reason, SHALL leave CLOSED notifications unchanged, and SHALL report how many notifications it closed and which recipients they belonged to.

#### Scenario: Closing one kind on a subject

- **WHEN** a subject has ACTIVE notifications of kinds "offer" and "assigned", and its "offer" notifications are closed at the subject's current version with reason "taken"
- **THEN** every "offer" notification on the subject is CLOSED with reason "taken", and every "assigned" notification is unchanged

#### Scenario: Sparing one recipient

- **WHEN** a subject's "offer" notifications for alice, bob and carol are closed sparing carol
- **THEN** alice's and bob's are CLOSED, carol's is unchanged, and the result names alice and bob

#### Scenario: A newer notification survives an older close

- **WHEN** a subject has an "assigned" notification at version 7, and the subject's "assigned" notifications are closed at version 6
- **THEN** the version-7 notification is unchanged

#### Scenario: A read notification can still be closed

- **WHEN** a READ notification's subject is closed
- **THEN** the notification is CLOSED, and it keeps the time it was read

### Requirement: A late source never reopens a closed subject

The system SHALL remember, for each subject and kind, the highest version at which notifications were closed, and for each subject the highest version at which every kind was closed. Publishing a notification whose subject version is below the remembered version for its kind SHALL create nothing, and SHALL report the notification as suppressed rather than failing.

Closing and publishing for the same subject SHALL NOT interleave in a way that leaves an ACTIVE notification below a remembered close version, even when they run concurrently on different application instances.

#### Scenario: A retried older source is suppressed

- **WHEN** a subject's "offer" notifications are closed at version 5, and afterwards a source at version 1 publishes an "offer" on that subject
- **THEN** nothing is created, and the publish reports one suppressed notification

#### Scenario: A newer source still publishes after a close

- **WHEN** a subject's "offer" notifications are closed at version 5, and afterwards a source at version 6 publishes an "offer" on that subject
- **THEN** the offer is created ACTIVE

#### Scenario: Closing every kind suppresses every kind

- **WHEN** a subject's notifications of every kind are closed at version 9, and afterwards a source at version 8 publishes a "taken" notification on that subject
- **THEN** nothing is created

#### Scenario: Concurrent close and late publish

- **WHEN** a close at version 5 and a publish at version 3 for the same subject and kind run concurrently on two connections
- **THEN** once both have finished, no ACTIVE notification of that kind exists on the subject below version 5

### Requirement: A close can tell each recipient it closed what happened, atomically

The system SHALL let a close name a successor notification. In the same transaction as the close, the system SHALL publish the successor to each recipient whose notification that close moved to CLOSED, except recipients the close names to skip. Successors SHALL be subject to the same idempotency and watermark suppression as any publish. A close result SHALL report the successors it created.

A close that is retried after an earlier attempt committed SHALL close nothing further and SHALL create no successors, and the successors the earlier attempt created SHALL remain.

#### Scenario: Closing offers tells the other recipients

- **WHEN** a subject has "offer" notifications for alice, bob and carol, and its "offer" notifications are closed at version 5 with a "taken" successor, skipping carol
- **THEN** all three offers are CLOSED, and alice and bob each have one ACTIVE "taken" notification at version 5, and carol has none

#### Scenario: A retried close creates no further successors

- **WHEN** the same close with a successor runs twice
- **THEN** each recipient has exactly one successor notification from that close

#### Scenario: A successor below a newer watermark is suppressed

- **WHEN** a subject's "taken" notifications are closed at version 8, and afterwards an older close at version 5 closes offers with a "taken" successor at version 5
- **THEN** no "taken" notification is created, and the close result reports the successors as suppressed

### Requirement: A coalescing publish does not repeat an open notification

The system SHALL let a publisher mark a notification as coalescing. A coalescing notification SHALL create nothing when its recipient already has an ACTIVE or READ notification of the same kind on the same subject, and SHALL be reported as coalesced rather than failing. The decision SHALL be made inside the subject's serialised write, so that concurrent publishes cannot both create one.

#### Scenario: An open offer absorbs a coalescing offer

- **WHEN** alice has an ACTIVE "offer" on a subject, and a coalescing "offer" from a new source is published for alice and bob on that subject
- **THEN** alice still has one offer, bob has a new ACTIVE offer, and the publish reports one coalesced and one created

#### Scenario: A read notification also absorbs it

- **WHEN** alice has a READ "offer" on a subject, and a coalescing "offer" is published for her on that subject
- **THEN** nothing is created for alice

#### Scenario: A closed notification does not absorb it

- **WHEN** alice's "offer" on a subject is CLOSED, and a coalescing "offer" at a version above the close is published for her
- **THEN** a new ACTIVE offer is created for alice

### Requirement: A recipient can list, count and mark their notifications read

The system SHALL let a recipient:

- list their own notifications, newest first, filtered by state, kind or subject, in pages that repeat and skip nothing however many notifications arrive between pages;
- count their ACTIVE notifications;
- mark chosen notifications read;
- mark every notification read up to a given instant.

Marking an ACTIVE notification read SHALL make it READ. Marking a READ notification read SHALL succeed and change nothing. Marking a CLOSED notification read SHALL record the read time and leave it CLOSED. A recipient SHALL NOT be able to read, list, count or mark another recipient's notifications through these operations. A notification of another recipient SHALL be reported as not found.

#### Scenario: Newest first with exact paging

- **WHEN** a recipient lists with a page size of 2 over five notifications, and a sixth arrives before the second page is requested
- **THEN** the pages together return the original five exactly once, newest first

#### Scenario: Counting counts only active notifications

- **WHEN** a recipient has three ACTIVE, two READ and one CLOSED notification
- **THEN** their count is 3

#### Scenario: Mark all read does not swallow what arrived later

- **WHEN** a recipient marks everything read up to the instant they loaded their list, and a notification created after that instant already exists
- **THEN** that later notification is still ACTIVE

#### Scenario: Another recipient's notification is not found

- **WHEN** bob marks alice's notification read
- **THEN** the operation reports not found, and alice's notification is unchanged

### Requirement: Every store behaves identically

The system SHALL behave identically on every store it supports: in memory, and on PostgreSQL, MySQL and SQLite through each supported database driver. It SHALL assert this with one shared suite of behavioural cases executed against every combination, not with per-store tests.

#### Scenario: The shared suite passes everywhere

- **WHEN** the shared notification store suite runs against the in-memory store and every supported driver and dialect combination
- **THEN** every case passes on every combination

### Requirement: Notification writes do not join the caller's transaction

The system SHALL perform each notification write in a transaction of its own, and SHALL NOT join a transaction the caller has open for other data. This limit SHALL be documented.

#### Scenario: A rolled-back caller transaction does not roll back notifications

- **WHEN** a caller publishes a notification while holding its own open transaction, and then rolls its transaction back
- **THEN** the notification still exists
