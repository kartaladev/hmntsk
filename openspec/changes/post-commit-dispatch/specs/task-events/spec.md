## MODIFIED Requirements

### Requirement: Events are durable before they are delivered

Events SHALL be recorded durably within the same transaction as the state change that produced them, and delivered to consumers only after that transaction commits. This holds whoever began the transaction. When the host began it, no library component SHALL deliver an event to an in-process consumer before the host commits, because the engine cannot observe that commit.

#### Scenario: Consumers never observe uncommitted state

- **WHEN** a consumer receives a completion event
- **THEN** the corresponding completed task is readable from the database

#### Scenario: Rollback delivers nothing

- **WHEN** a transaction containing a task change is rolled back
- **THEN** no event from that transaction is delivered to any consumer

#### Scenario: Host-led rollback delivers nothing to in-process consumers

- **WHEN** a lifecycle operation, a sweep or an HTTP request runs inside a transaction the host began, and the host rolls that transaction back
- **THEN** no in-process consumer has been told about any event from that transaction, neither before nor after the rollback

## ADDED Requirements

### Requirement: A host-led transaction hands in-process dispatch back to the host

When a lifecycle operation joins a transaction the host began, the system SHALL withhold in-process dispatch and hand the produced events back for the host to dispatch after it commits.

By default, the events are handed back on the operation's result. When the host has opened a deferred-dispatch scope on the context before beginning its transaction, the events of every operation in that scope SHALL be collected there instead, in the order the operations ran. The operation's result then reports nothing pending, so the same events are never dispatched twice. Dispatching the scope after the commit SHALL deliver each collected event once. Dropping the scope after a rollback SHALL deliver nothing.

An operation whose transaction the engine began SHALL commit and dispatch on its own, whether or not a deferred-dispatch scope is present.

#### Scenario: Default hand-back on the result

- **WHEN** a lifecycle operation runs inside a host transaction with no deferred-dispatch scope
- **THEN** no in-process consumer runs, the result reports its events as pending, and dispatching the result after the host commits delivers them

#### Scenario: Host override collects into a deferred-dispatch scope

- **WHEN** a host opens a deferred-dispatch scope, begins its transaction, runs two lifecycle operations, commits, and then dispatches the scope
- **THEN** no in-process consumer runs before the dispatch, both operations' events are delivered after it in the order the operations ran, and neither result reports pending events

#### Scenario: A deferred-dispatch scope dispatches each event once

- **WHEN** a host dispatches the same deferred-dispatch scope twice after committing
- **THEN** each collected event is delivered once, and the second dispatch delivers nothing

#### Scenario: A deferred-dispatch scope does not delay an engine-led operation

- **WHEN** a lifecycle operation runs under a deferred-dispatch scope but outside any host transaction
- **THEN** the engine commits and dispatches immediately, and nothing is collected in the scope

### Requirement: In-process handler errors reach the dispatch error hook on every path

An error returned by an in-process consumer SHALL be reported to the host's dispatch error hook whichever path dispatched it: the engine after its own commit, a result dispatched by the host, a deferred-dispatch scope, or a sweep's hand-back. It SHALL also be returned to the host that called the dispatch. No library component SHALL discard such an error. The default hook does nothing. A host replaces it with its own hook.

When a component withholds dispatch and cannot hand the events to anyone who will dispatch them after the commit, it SHALL report that to the dispatch error hook as a distinguishable "dispatch not deferred" error. It SHALL NOT run the consumers early and SHALL NOT drop the fact silently. The events stay durable for the relay.

#### Scenario: Default hook is silent

- **WHEN** an in-process consumer fails and the host configured no dispatch error hook
- **THEN** the operation still succeeds and nothing is logged by the library

#### Scenario: Host-supplied hook receives a host-led consumer error

- **WHEN** a host that configured a dispatch error hook dispatches a host-led result, a deferred-dispatch scope or a sweep hand-back, and a consumer returns an error
- **THEN** the hook receives that error, and the dispatch call returns it as well

#### Scenario: Declined dispatch is reported, not dropped

- **WHEN** a component declines to dispatch host-led events that no one will dispatch after the commit
- **THEN** the dispatch error hook receives an error identifiable as "dispatch not deferred" that names the task, no in-process consumer runs, and the events remain in the outbox for the relay
