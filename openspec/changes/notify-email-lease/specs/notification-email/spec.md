## MODIFIED Requirements

### Requirement: A notification is emailed at most once by default

By default the system SHALL email each notification at most once. A send that may have happened, because the process stopped or the sender could not tell, SHALL NOT be repeated and SHALL be recorded as abandoned. The host SHALL be able to choose at-least-once delivery instead, under which an in-doubt send is repeated with the same idempotency key as the original attempt, so a sender that honours the key delivers it once. Every message SHALL carry an idempotency key that stays the same for every attempt of the same message.

A message repeated under the same idempotency key SHALL cover exactly the notifications of the original attempt, and no others. This SHALL hold even when some of them were read or closed since the original attempt, and the repeat SHALL be rendered from those same notifications. When any notification of the original attempt no longer exists, the original message cannot be reproduced. The deleted notifications SHALL then be recorded as skipped, and the survivors that are still active SHALL be sent as a new message under a new idempotency key. The survivors that are no longer active SHALL be recorded as skipped. A message SHALL NOT go out under an idempotency key already used for a different set of notifications.

#### Scenario: A crash after sending does not resend by default

- **WHEN** a dispatcher sends alice's message and stops before recording it, and a later pass runs after its lease expired
- **THEN** the message is not sent again, and the notifications are recorded as abandoned

#### Scenario: A host chooses at-least-once delivery

- **WHEN** a dispatcher configured for at-least-once delivery sends alice's message and stops before recording it, and a later pass runs after its lease expired
- **THEN** the message is sent again for the same notifications with the same idempotency key

#### Scenario: A resend does not absorb newer notifications

- **WHEN** an in-doubt message is resent under at-least-once delivery while alice has received a newer notification
- **THEN** the resent message covers exactly the original notifications, and the newer one goes in a separate message

#### Scenario: A resend keeps a notification read while the send was in doubt

- **WHEN** a message covering alice's notifications n1 and n2 is in doubt under at-least-once delivery, alice reads n1, and a later pass resends the message
- **THEN** the resent message covers n1 and n2, carries the original idempotency key, and has the same subject as the original

#### Scenario: A resend whose notification was deleted goes out under a new key

- **WHEN** a message covering alice's notifications n1 and n2 is in doubt under at-least-once delivery, n1 is deleted by retention, and a later pass handles the message
- **THEN** n1 is recorded as skipped, and n2 is sent in a message whose idempotency key differs from the original

### Requirement: Concurrent dispatchers never send the same notification twice

The system SHALL let dispatchers run in several application instances at once, and SHALL ensure that only one of them sends each notification. A dispatcher that stops mid-pass SHALL NOT strand its notifications: once its claim lapses, another dispatcher SHALL take them over, subject to the delivery guarantee for any send already in doubt.

A dispatcher SHALL start a send only while its claim on every notification of the message is still live, judged at the moment the send starts rather than when the pass began. Starting a send SHALL renew the claim on those notifications for a full lease period. Starting a send SHALL be recorded for all of the message's notifications or for none of them. When a dispatcher cannot start a send because its claim lapsed or was taken over, it SHALL NOT send, and SHALL release any of the message's notifications it still holds, without counting an attempt. The outcome of a send SHALL be recorded whenever no other dispatcher has taken the notifications over since the send started, even if the claim has lapsed since. The sender's answer SHALL therefore decide what is recorded. In particular, a message the sender accepted SHALL be recorded as sent, and a transient failure SHALL be scheduled for retry, not abandoned. By default a claim lasts five minutes. The host SHALL be able to set a different lease, and the lease SHALL outlast one send. A send that outlasts a full lease may be taken over, and is then treated as in doubt under the configured delivery guarantee. This limit SHALL be documented.

#### Scenario: Two instances dispatch at the same time

- **WHEN** two dispatchers run passes concurrently over the same qualifying notifications
- **THEN** each notification is sent by exactly one of them

#### Scenario: A stopped dispatcher's unsent claim is taken over

- **WHEN** a dispatcher claims notifications and stops before attempting to send them
- **THEN** after its claim lapses, another dispatcher emails them

#### Scenario: A slow pass does not start a send after its claim lapsed

- **WHEN** a dispatcher claims alice's and bob's notifications, and alice's send takes longer than the lease
- **THEN** the dispatcher does not send bob's message, bob's notifications are released without an attempt counted, and a later pass by another dispatcher emails bob exactly once

#### Scenario: An accepted send is recorded as sent even if the pass ran long

- **WHEN** a dispatcher starts bob's send under a live claim, the sender accepts it, and the pass's original claim time has passed by then
- **THEN** bob's notifications are recorded as sent, and no other dispatcher records them as abandoned

#### Scenario: A transient failure after a long pass is retried, not abandoned

- **WHEN** a dispatcher starts bob's send under a live claim, and the sender fails transiently
- **THEN** bob's notifications are scheduled for retry and are emailed by a later pass

#### Scenario: A host lengthens the lease for a slow sender

- **WHEN** a host configures a lease of thirty minutes and its sender takes ten minutes per message
- **THEN** no send is taken over by another dispatcher, and every accepted message is recorded as sent

### Requirement: Email delivery state is durable and identical on every store

The system SHALL record email delivery state durably, so that a restart or redeploy neither resends a sent notification nor loses track of an unsent one. The claiming, recording and clean-up of email delivery state SHALL behave identically on every supported store, asserted by one shared suite rather than per-store tests. That includes the lease fencing of a send's start and its all-or-nothing recording. A host that does not use email SHALL NOT need the storage email delivery requires. A host store that records email deliveries SHALL pass the same shared suite.

#### Scenario: A redeploy does not resend

- **WHEN** a dispatcher sends alice's message and records it, and the application is redeployed
- **THEN** no later pass sends that message again

#### Scenario: Email storage is optional

- **WHEN** a host uses notifications without email and has not applied the email delivery schema
- **THEN** notifications, retention and realtime work, and schema verification for notifications succeeds

#### Scenario: Starting a send under a lapsed lease is refused on every store

- **WHEN** the shared suite records the start of a send for a notification whose lease its owner held but which lapsed before the recorded instant
- **THEN** every store changes nothing and reports zero notifications changed

#### Scenario: Starting a send renews the lease on every store

- **WHEN** the shared suite records the start of a send with a lease of five minutes, and another owner claims four minutes later
- **THEN** every store keeps the notification with its first owner

#### Scenario: A partially held message is not started on any store

- **WHEN** the shared suite records the start of a send for two notifications of which the owner still holds only one
- **THEN** every store changes neither notification

## ADDED Requirements

### Requirement: In-doubt resends are bounded by the attempt limit and the maximum lag

Under at-least-once delivery, each repeat of an in-doubt message SHALL count as an attempt against the same attempt limit that bounds retries. A message in doubt whose notifications have used up their attempts SHALL be recorded as failed and not repeated. A message in doubt that contains a notification older than the maximum lag SHALL NOT be repeated. Its notifications SHALL instead be recorded as abandoned, because whether it was delivered is unknown. With no configuration, the limits SHALL be the dispatcher's defaults of five attempts and twenty-four hours. A host that changes the attempt limit or the maximum lag SHALL change the bound on repeats too.

#### Scenario: A sender that never answers is not retried forever

- **WHEN** a dispatcher configured for at-least-once delivery and an attempt limit of three runs twelve passes, each after the lease lapsed, and the sender reports every send as in doubt
- **THEN** alice's message is handed to the sender at most three times, and her notifications are then recorded as failed

#### Scenario: The default attempt limit bounds repeats

- **WHEN** a dispatcher configured for at-least-once delivery with no attempt limit set keeps finding alice's message in doubt
- **THEN** the message is handed to the sender at most five times

#### Scenario: A repeat past the maximum lag is not sent

- **WHEN** a dispatcher configured for at-least-once delivery and a maximum lag of five minutes finds alice's message in doubt eleven minutes after her notification was created
- **THEN** the message is not sent again, and her notification is recorded as abandoned

### Requirement: An in-doubt message is claimed whole

A claim that takes any notification of a message whose send is in doubt SHALL take every notification of that message whose claim has lapsed, even if this exceeds the claim's limit by less than one message's worth. Two concurrent claims SHALL NOT split one message between them. This SHALL hold identically on every store.

#### Scenario: A claim limit does not split an in-doubt message

- **WHEN** a message covering three of alice's notifications is in doubt and a dispatcher claims with a limit of two
- **THEN** the claim returns all three notifications of that message

#### Scenario: Concurrent claims do not split an in-doubt message

- **WHEN** two dispatchers claim at once after an in-doubt message's lease lapsed
- **THEN** exactly one of them receives the message's notifications, and it receives all of them
