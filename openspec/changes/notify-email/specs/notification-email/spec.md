## Purpose

Defines how stored notifications are emailed to people who are not looking at the application: which notifications qualify, how they are batched, how duplicate and lost emails are bounded across crashes and concurrent dispatchers, and what the host supplies for addressing, templating and sending.

## ADDED Requirements

### Requirement: Email dispatch runs only when the host drives it

The system SHALL NOT email on its own. A dispatch pass SHALL run only when the host invokes it, whether once, from a long-running loop the host starts, or from the host's own scheduler. Stopping a loop the host started SHALL end it without leaking any background work.

#### Scenario: Constructing a dispatcher sends nothing

- **WHEN** a host constructs an email dispatcher and never invokes it
- **THEN** no email is sent, no delivery record is written, and no background work is running

#### Scenario: A loop stops when the host cancels it

- **WHEN** a host runs the dispatch loop and cancels its context
- **THEN** the loop returns, and no background work remains

### Requirement: Only active notifications past the grace delay and within the lag bound are emailed

The system SHALL email a notification only while it is ACTIVE. The system SHALL NOT email a notification until a grace delay has passed since it was created, so that a notification read or closed in the application within that window is never emailed. The system SHALL NOT email a notification created longer ago than a maximum lag, so that enabling email, or resuming after an outage, does not send a backlog of old notifications. With no configuration, the grace delay SHALL be **5 minutes** and the maximum lag **24 hours**. The host SHALL be able to change both, and SHALL be able to remove the grace delay explicitly.

#### Scenario: A notification read within the grace delay is never emailed

- **WHEN** a notification is created for alice and she reads it 2 minutes later, and a dispatch pass runs 6 minutes after creation
- **THEN** no email is sent for that notification

#### Scenario: A notification still active after the grace delay is emailed

- **WHEN** a notification is created for alice, stays ACTIVE, and a dispatch pass runs 6 minutes after creation
- **THEN** alice is emailed about it

#### Scenario: A closed notification is never emailed

- **WHEN** a notification is closed before any dispatch pass considers it
- **THEN** no email is ever sent for it

#### Scenario: An old backlog is not emailed when email is first enabled

- **WHEN** a host enables email for the first time while alice has an ACTIVE notification created 3 days earlier
- **THEN** no email is sent for that notification

#### Scenario: A host removes the grace delay

- **WHEN** a host configures a dispatcher with no grace delay, and a notification is created
- **THEN** the next dispatch pass may email it immediately

### Requirement: A notification that stops being active before sending is not emailed

The system SHALL check that each claimed notification is still ACTIVE immediately before sending, and SHALL NOT include one that has been read, closed or deleted since it was claimed. The remaining window between that check and the send SHALL be documented as a stated limit.

#### Scenario: A notification read after claiming is excluded

- **WHEN** a dispatch pass claims alice's notification and she reads it before the pass renders her message
- **THEN** the message does not include that notification, and the notification is not emailed later

#### Scenario: A notification pruned before emailing is never emailed

- **WHEN** a notification is deleted by retention before any dispatch pass emails it
- **THEN** no email is ever sent for it, and no delivery record for it remains after the next pass

### Requirement: One message per recipient per pass

The system SHALL combine a recipient's qualifying notifications from one pass into one message, and SHALL cap how many notifications one message carries. With no configuration the cap SHALL be **20**. Notifications beyond the cap SHALL be emailed by a later pass. The host SHALL be able to change the cap.

#### Scenario: Several notifications arrive as one message

- **WHEN** a dispatch pass finds three qualifying notifications for alice and one for bob
- **THEN** alice receives one message covering her three notifications, and bob receives one message

#### Scenario: A burst beyond the cap is spread over passes

- **WHEN** alice has 25 qualifying notifications and the cap is 20
- **THEN** one pass sends her a message covering the 20 oldest, and a later pass covers the remaining 5

### Requirement: A notification is emailed at most once by default

By default the system SHALL email each notification at most once. A send that may have happened, because the process stopped or the sender could not tell, SHALL NOT be repeated and SHALL be recorded as abandoned. The host SHALL be able to choose at-least-once delivery instead, under which an in-doubt send is repeated with the same idempotency key as the original attempt, so a sender that honours the key delivers it once. Every message SHALL carry an idempotency key that stays the same for every attempt of the same message.

#### Scenario: A crash after sending does not resend by default

- **WHEN** a dispatcher sends alice's message and stops before recording it, and a later pass runs after its lease expired
- **THEN** the message is not sent again, and the notifications are recorded as abandoned

#### Scenario: A host chooses at-least-once delivery

- **WHEN** a dispatcher configured for at-least-once delivery sends alice's message and stops before recording it, and a later pass runs after its lease expired
- **THEN** the message is sent again for the same notifications with the same idempotency key

#### Scenario: A resend does not absorb newer notifications

- **WHEN** an in-doubt message is resent under at-least-once delivery while alice has received a newer notification
- **THEN** the resent message covers exactly the original notifications, and the newer one goes in a separate message

### Requirement: Concurrent dispatchers never send the same notification twice

The system SHALL let dispatchers run in several application instances at once, and SHALL ensure that only one of them sends each notification. A dispatcher that stops mid-pass SHALL NOT strand its notifications: once its claim lapses, another dispatcher SHALL take them over, subject to the delivery guarantee for any send already in doubt.

#### Scenario: Two instances dispatch at the same time

- **WHEN** two dispatchers run passes concurrently over the same qualifying notifications
- **THEN** each notification is sent by exactly one of them

#### Scenario: A stopped dispatcher's unsent claim is taken over

- **WHEN** a dispatcher claims notifications and stops before attempting to send them
- **THEN** after its claim lapses, another dispatcher emails them

### Requirement: Recipients without an address are skipped without error

The system SHALL obtain each recipient's address from the host. A recipient with no address SHALL NOT be an error: that recipient's notifications SHALL be recorded as skipped and SHALL NOT be reconsidered. A failure to look an address up SHALL be retried.

#### Scenario: A recipient has no email address

- **WHEN** a dispatch pass finds qualifying notifications for a recipient the host has no address for
- **THEN** nothing is sent, no error is reported, and later passes do not reconsider those notifications

#### Scenario: The address lookup fails

- **WHEN** the host's address lookup returns an error for alice
- **THEN** alice's notifications are attempted again by a later pass

### Requirement: Which notifications are emailed is the host's choice

With no configuration, the system SHALL consider notifications of every kind for email. The host SHALL be able to restrict email to named kinds, and SHALL be able to replace the selection with its own rule, where preferences, quiet hours and unsubscribes plug in. A notification the selection rejects SHALL be recorded as skipped and SHALL NOT be reconsidered.

#### Scenario: Every kind is emailed by default

- **WHEN** a dispatcher with no selection configured finds qualifying notifications of two different kinds
- **THEN** both are emailed

#### Scenario: A host restricts email to certain kinds

- **WHEN** a dispatcher restricted to kind "assigned" finds qualifying notifications of kinds "assigned" and "taken"
- **THEN** only the "assigned" notification is emailed, and the "taken" notification is never reconsidered

#### Scenario: A host applies its own rule

- **WHEN** a host supplies a rule that rejects notifications for recipients in quiet hours
- **THEN** those notifications are skipped, and the others are emailed

### Requirement: Send failures are classified, retried within a budget, and reported

The system SHALL treat a send the host's sender reports as rejected as a permanent failure, recorded as failed and never retried. The system SHALL retry any other failure that means the message was not sent, after a delay that grows with the attempts already made, up to an attempt limit, after which it SHALL record the notifications as failed. A failure to render a message SHALL be a permanent failure. Every failure SHALL be reported to the host rather than silently swallowed, and SHALL NOT abandon the rest of the pass.

#### Scenario: The sender rejects a message

- **WHEN** the host's sender reports alice's message as rejected
- **THEN** her notifications are recorded as failed, are not retried, and the error reaches the host

#### Scenario: A transient failure is retried until the limit

- **WHEN** the host's sender fails transiently for alice on every attempt
- **THEN** her notifications are attempted again with growing delays, and are recorded as failed once the attempt limit is reached

#### Scenario: One recipient's failure does not stop the pass

- **WHEN** rendering fails for alice and succeeds for bob in the same pass
- **THEN** bob is emailed, and the failure for alice reaches the host

### Requirement: Wiring mistakes fail at construction

The system SHALL refuse to construct a dispatcher that has no sender, no address lookup or no template, whose notification store does not record email deliveries, or whose durations, caps or limits are zero or negative where a positive value is required.

#### Scenario: A dispatcher without a sender

- **WHEN** a host constructs a dispatcher without a sender
- **THEN** construction fails with a configuration error

#### Scenario: A store that cannot record email deliveries

- **WHEN** a host constructs a dispatcher over a notification store that does not support email delivery records
- **THEN** construction fails with a configuration error

#### Scenario: A negative grace delay

- **WHEN** a host configures a negative grace delay
- **THEN** construction fails with a configuration error

### Requirement: Email delivery state is durable and identical on every store

The system SHALL record email delivery state durably, so that a restart or redeploy neither resends a sent notification nor loses track of an unsent one. The claiming, recording and clean-up of email delivery state SHALL behave identically on every supported store, asserted by one shared suite rather than per-store tests. A host that does not use email SHALL NOT need the storage email delivery requires.

#### Scenario: A redeploy does not resend

- **WHEN** a dispatcher sends alice's message and records it, and the application is redeployed
- **THEN** no later pass sends that message again

#### Scenario: Email storage is optional

- **WHEN** a host uses notifications without email and has not applied the email delivery schema
- **THEN** notifications, retention and realtime work, and schema verification for notifications succeeds

### Requirement: A pass reports what it did

The system SHALL report, for each pass, how many notifications were claimed, sent, skipped (with the reason: no address, not selected, no longer active), retried, failed and abandoned, how many messages were sent, and how many delivery records for deleted notifications were removed.

#### Scenario: Counts after a mixed pass

- **WHEN** a pass sends one message covering two notifications, skips one for a missing address, and retries one after a transient failure
- **THEN** the pass reports two sent, one message, one skipped for no address, and one retried
