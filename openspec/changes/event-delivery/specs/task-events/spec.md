## MODIFIED Requirements

### Requirement: Delivery is at-least-once and survives restarts

The system SHALL retain recorded events until every configured destination has accepted them, so that a crash between commit and delivery does not lose them. Consumers SHALL therefore tolerate receiving the same event more than once.

An event that cannot be delivered — its attempt limit exhausted, or a destination rejecting it permanently — SHALL be marked dead-lettered and retained for inspection rather than retried indefinitely or discarded. Retention is therefore until acceptance **or** dead-lettering, not until acceptance alone.

#### Scenario: Crash after commit, before delivery

- **WHEN** the process terminates after a transaction commits but before its events are delivered
- **THEN** those events are still available for delivery after restart

#### Scenario: An undeliverable event is retained, not discarded

- **WHEN** an event exhausts its delivery attempts
- **THEN** it is marked dead-lettered, is no longer attempted, and remains readable with its attempt count and last error

#### Scenario: Acceptance is tracked per destination

- **WHEN** an event is accepted by one configured destination and not another
- **THEN** it is not yet considered delivered, and a later attempt targets only the destination that has not accepted it
