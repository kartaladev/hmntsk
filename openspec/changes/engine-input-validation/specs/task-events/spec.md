## ADDED Requirements

### Requirement: Recorded events are isolated from caller memory

An event recorded durably SHALL be unaffected by any later change a caller makes to memory it holds. That covers the events returned from a lifecycle operation and an event read back from the durable record. It SHALL hold for every store the engine supports, including the in-memory store, and for every field that holds a collection: candidate users, groups and exclusions, correlation data, the callback target, and the output payload.

#### Scenario: Mutating returned events does not change the record

- **WHEN** a caller creates a task with correlation data `{"k":"v"}` and candidate user `a`, then changes the returned event's correlation value to `tampered` and its first candidate user to `mallory`
- **THEN** reading that event back from the durable record still shows correlation `{"k":"v"}` and candidate user `a`

#### Scenario: Mutating a read event does not change the record

- **WHEN** a caller reads an event from the durable record and changes its correlation value and first candidate user
- **THEN** reading the same event again returns the original values
