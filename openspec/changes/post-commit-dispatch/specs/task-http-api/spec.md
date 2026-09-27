## ADDED Requirements

### Requirement: A request served inside a host transaction never dispatches before the commit

A host MAY wrap the contract's handlers in a transaction of its own, so that a lifecycle operation joins the host's writes. A request served that way SHALL NOT run in-process consumers while the handler is running, on any binding, because the host has not committed yet. A consumer's error SHALL NOT be discarded.

By default, when the host has not opened a deferred-dispatch scope, the transport cannot observe the commit. It SHALL therefore run no in-process consumer for that request, and SHALL report a "dispatch not deferred" error naming the task to the engine's dispatch error hook. The response is unaffected, and the events remain durable in the outbox for the relay.

A host that wants in-process consumers to run for such requests opens a deferred-dispatch scope in the same middleware that begins its transaction, and dispatches the scope after committing. Consumer errors then reach the engine's dispatch error hook.

A request served with no host transaction SHALL keep its current behaviour: the engine commits and dispatches before the response is written.

#### Scenario: Default: no dispatch before the host commits

- **WHEN** a host serves a claim inside its own transaction on the net/http, gin or fiber binding, with no deferred-dispatch scope
- **THEN** the response is `200` with the claimed task, no in-process consumer runs, and the dispatch error hook receives a "dispatch not deferred" error naming the task

#### Scenario: Host override: consumers run after the host commits

- **WHEN** a host's middleware opens a deferred-dispatch scope, begins its transaction, serves a claim, commits, and dispatches the scope, on any binding
- **THEN** an in-process consumer that reads the task back sees it `RESERVED`, and never sees the pre-claim status

#### Scenario: A failing consumer's error reaches the hook

- **WHEN** under the host override an in-process consumer returns an error
- **THEN** the engine's dispatch error hook receives that error, and the response already sent is unaffected

#### Scenario: Requests with no host transaction are unchanged

- **WHEN** a claim is served with no host transaction
- **THEN** the engine commits and runs in-process consumers before the response is written, and no "dispatch not deferred" error is reported
