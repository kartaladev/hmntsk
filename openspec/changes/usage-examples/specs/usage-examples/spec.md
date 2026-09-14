## Purpose

Gives consumers runnable, tested scenarios that show how to wire each hmntsk capability into an application, each demonstrating the library's default first and then a consumer override, plus a browser demo of a contextual inbox.

## ADDED Requirements

### Requirement: Every scenario runs and is proven by a test

Each scenario SHALL be runnable on its own as a program, printing what it demonstrates. Each scenario SHALL have a test that runs the same code the program runs and checks what it prints, so that a scenario which no longer runs, or no longer shows what it claims, fails the build.

#### Scenario: A reader runs a scenario

- **WHEN** a reader runs a scenario from the examples module
- **THEN** it completes without error and prints the steps and outcomes it demonstrates

#### Scenario: A library change breaks a scenario

- **WHEN** a change to any library module alters behaviour a scenario depends on
- **THEN** that scenario's test fails in the repository's standard test run

### Requirement: Each scenario shows the default, then an override

Every scenario other than the quickstart SHALL present its output in two labelled sections: first the library's default behaviour with no configuration beyond what is required, then the same capability with a consumer override applied. The override section SHALL show an observable difference from the default. The quickstart SHALL use defaults only.

#### Scenario: Default and override are both visible

- **WHEN** a reader runs a scenario that demonstrates a replaceable default
- **THEN** the output contains a default section and an override section, and the override section shows an outcome the default section does not

#### Scenario: The quickstart needs no configuration

- **WHEN** a reader runs the quickstart
- **THEN** it takes one task from creation to completion using only required wiring and default options

### Requirement: Scenarios need no external services unless they demonstrate one

Every scenario that does not exist to demonstrate a database server or message broker SHALL run, with its test, with no database server, message broker, mail server, container runtime or network access beyond the loopback interface. Its persistent storage SHALL use an in-memory store or an embedded database.

#### Scenario: Running offline

- **WHEN** the tests of the scenarios that demonstrate no external service run on a machine with no network and no container runtime
- **THEN** every one of those tests passes

### Requirement: Service-backed scenarios run against real services

A scenario that demonstrates a database server or message broker SHALL run against a real instance of it, never a fake. Its test SHALL provision that instance with a container, through the helper the owning module already provides. When run as a program, it SHALL take the service's address from the environment, and SHALL fail with a message naming the missing setting and how to start the service when the address is absent.

#### Scenario: A service-backed test

- **WHEN** the test of a service-backed scenario runs on a machine with a container runtime
- **THEN** it starts the service in a container, runs the scenario against it, and passes

#### Scenario: Running without the service

- **WHEN** a reader runs a service-backed scenario without setting the service's address
- **THEN** the program exits unsuccessfully with a message naming the environment setting and how to start the service

### Requirement: Scenario output is deterministic

A scenario's printed output SHALL be identical across runs, so that its test can compare it exactly. Values that vary between runs, such as generated identifiers and wall-clock timestamps, SHALL either be controlled by the scenario or left out of the printed output.

#### Scenario: Repeated runs

- **WHEN** a scenario is run twice
- **THEN** both runs print identical output

### Requirement: Every example explains itself

Every example directory SHALL contain a README that states what the example demonstrates, the context and domain it assumes, what it deliberately leaves out, and how to run it, including any service it needs and how to start that service.

#### Scenario: A reader opens an example

- **WHEN** a reader opens any example directory
- **THEN** its README tells them what the example shows and the exact commands to run it and its test

### Requirement: The scenarios cover the library's capabilities

The examples SHALL together demonstrate, each in at least one scenario:

- a task's lifecycle from creation to completion, and every other lifecycle operation: release, delegation, suspension and resumption, failure and cancellation, with a stale-version conflict and an illegal transition refused;
- assignment rules: reservation of a task with a single eligible candidate, an error state for a task with none, and excluded users;
- correlated task creation inside the host's own database transaction, nested transaction scopes, the published migration statements, a table prefix, and schema verification reporting a mismatch;
- inbox buckets as queries, every supported ordering, exact paging and bucket counts;
- self-only inbox query authorization over HTTP, and a host policy permitting a team queue;
- listing every task for one business record, participants-only single-task reads, and a host read policy;
- type metadata with route expansion, and host-defined metadata keys;
- serving task-type schemas, saving progress (which starts a reserved task), completing against the full output schema, and the typed facade including its typed completion handler;
- escalation by the sweeper under a type default and a per-task override, direct escalation, exemption of worked tasks, an escalation cap, and supersession into obsolescence;
- in-process event handling; durable delivery through the relay to a signed webhook that the receiver verifies, including the default destination policy refusing a loopback address, retry with backoff, and independent acceptance by several sinks; and the audience snapshot every event carries;
- publishing events to a Redis stream with a retention bound, to NATS subjects and to JetStream;
- the task store on PostgreSQL and MySQL through each supported driver;
- the task HTTP contract and the notification handlers served by the Gin and Fiber bindings;
- projecting task events into notifications, including on release, delegation and widening escalation, listing, counting, reading, reading all and streaming them, custom notification links, titles, rules and closing statuses, a subscription policy override, age and count retention under both strategies, and email delivery through a host-supplied mailer with kind filtering and recipients without an address;
- notifications without tasks: idempotent publishing, coalescing, closing with a successor, and version watermarks;
- notification change signals shared across application instances through Redis and NATS broadcasters, and the WebSocket endpoint with its origin check and mark-read requests.

#### Scenario: A consumer looks for a capability

- **WHEN** a consumer looks up any capability in the list above in the examples index
- **THEN** the index names the scenario that demonstrates it

### Requirement: The examples are built with every module and never released

The examples module SHALL be part of the development workspace and of every repository-wide build, lint, test, race and vulnerability run. It SHALL NOT appear in the release order and SHALL never be tagged.

#### Scenario: Repository-wide test run

- **WHEN** a contributor runs the repository-wide unit tests
- **THEN** the examples module's tests run with them

#### Scenario: Release order

- **WHEN** the release order is printed
- **THEN** it does not include the examples module, and the release documentation states that the examples are never tagged

### Requirement: A browser demo shows a contextual inbox

The examples SHALL include one browser demo, served by a Go program, that shows:

- inbox buckets with their counts, ordered by urgency;
- each task linked to its business record through the task type's route;
- a form rendered from the task type's schemas, through which a task is progressed and completed;
- a notification count that updates without a page reload when a notification is published for the viewer.

The demo SHALL let the viewer switch between demo users so that per-user buckets and authorization are visible, and SHALL state on the page that this identity mechanism is for demonstration only. The demo SHALL be labelled as an illustration and not as a reusable UI component.

#### Scenario: Switching users changes the buckets

- **WHEN** a viewer switches from one demo user to another
- **THEN** the buckets and counts shown are those of the selected user

#### Scenario: Completing a task from the form

- **WHEN** a viewer claims a task, fills in the rendered form with valid output and submits it
- **THEN** the task is completed and leaves the viewer's active buckets, and the counts update

#### Scenario: A live notification

- **WHEN** a task that the viewing user may act on is created while the page is open
- **THEN** the notification count increases without the viewer reloading the page

### Requirement: The browser demo runs without a frontend toolchain

The browser demo SHALL run with only the Go toolchain installed. The built frontend it serves SHALL be committed, and the repository's checks SHALL fail when the committed build does not match its source.

#### Scenario: Running the demo with Go only

- **WHEN** a reader with Go and no Node toolchain runs the browser demo
- **THEN** the demo starts and serves the complete page

#### Scenario: A stale frontend build

- **WHEN** a contributor changes the demo's frontend source without rebuilding the committed output
- **THEN** the repository's generated-artefact check fails
