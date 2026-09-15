# usage-examples Specification

## Purpose
Gives consumers runnable, tested scenarios that show how to wire each hmntsk capability into an application, each demonstrating the library's default first and then a consumer override, plus a browser demo of tasks inside an application's own pages.

## Requirements

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

### Requirement: A browser demo shows an order's purchasing workflow as contextual tasks

The examples SHALL include one browser demo, `contextual-ui`, served by a Go program, that embeds tasks in the pages of a small purchasing application rather than in a stand-alone inbox. The demo SHALL declare its own purchasing domain rather than reuse the shared invoicing domain. It SHALL show:

- a sign-in page that lists the demo users, and a signed-in user's avatar in the top-right corner of every page with a menu to sign out;
- an inbox page with buckets and their counts, ordered by urgency, with no user switcher on it;
- an orders page where a purchasing user places an order, which creates the order and its approval task;
- an order page, reached through the task types' route, that shows the order, its purchase order documents, its invoice once it has arrived, every step of its workflow and every task on it, and the form named by the viewer's task type through which the task is claimed, progressed and completed;
- a notification count that updates without a page reload when a notification is published for the viewer;
- every table as a data grid. A grid over a server-ordered, cursor-paged task list SHALL page on the server and SHALL NOT offer sorting or filtering of a single page in the browser.

An order SHALL move through approval, purchase order, invoice, invoice review and invoice approval:

- approving an order SHALL create its purchase order task, after the approval's completion is committed, and declining it SHALL end the workflow;
- the purchase order task SHALL be issued by sending the purchase order when the order's supplier is on the demo's supplier registry, and by uploading a purchase order document otherwise;
- issuing the purchase order SHALL store the document and complete the task together, or do neither;
- the supplier's invoice SHALL arrive, with its review task, a delay after the purchase order was issued;
- completing a review that finds the invoice matches its order SHALL create the invoice's approval task, after the review's completion is committed, and the invoice's approval SHALL decide whether the order is approved or rejected.

Every workflow step SHALL be safe to repeat. The demo SHALL state on the sign-in page that its identity mechanism is for demonstration only, and SHALL be labelled as an illustration and not as a reusable UI component.

#### Scenario: Signing in shows the user's own inbox

- **WHEN** a viewer signs in as one demo user, signs out, and signs in as another
- **THEN** the buckets and counts shown are those of the signed-in user, and the avatar names that user

#### Scenario: An unsigned viewer is sent to sign in

- **WHEN** a viewer with no demo session opens any page
- **THEN** the sign-in page is shown, and after signing in the viewer returns to the page they opened

#### Scenario: Placing an order asks for its approval

- **WHEN** a purchasing user places an order with a supplier, description and amount
- **THEN** the order and its approval task exist, no invoice exists, and the budget holder's notification count increases without a reload

#### Scenario: Only purchasing places orders

- **WHEN** a signed-in user outside purchasing places an order
- **THEN** the order is refused and nothing is created

#### Scenario: A declined order goes no further

- **WHEN** the budget holder completes an order's approval declining it
- **THEN** no purchase order task is created and the order is shown as declined

#### Scenario: A registered supplier's purchase order is sent

- **WHEN** an order with a supplier on the registry is approved, and purchasing sends its purchase order from the order page
- **THEN** a purchase order document addressed to the supplier's registry contact is stored, the purchase order task is completed, and the order waits for its invoice

#### Scenario: An unregistered supplier's purchase order is uploaded

- **WHEN** an order with a supplier not on the registry is approved, and purchasing uploads a PDF purchase order from the order page
- **THEN** the uploaded document is stored and can be downloaded, the purchase order task is completed, and the order waits for its invoice

#### Scenario: A purchase order issued the wrong way is refused

- **WHEN** purchasing sends the purchase order of an order whose supplier is not on the registry, uploads one for a registered supplier, or uploads a file that is not a PDF, PNG or JPEG or is larger than 5 MB
- **THEN** the request is refused, no document is stored and the task is not completed

#### Scenario: The invoice arrives after a delay

- **WHEN** an order's purchase order has been issued and the invoice delay has passed
- **THEN** the order's invoice and its review task exist, and receiving invoices again creates neither a second time

#### Scenario: A matching review leads to approval

- **WHEN** an approver completes an invoice's review from the order page stating that it matches its order
- **THEN** the invoice's approval task is created, and the order page shows the review completed and the approval waiting

#### Scenario: A disputed review ends the workflow

- **WHEN** an approver completes an invoice's review stating that it does not match its order
- **THEN** no approval task is created and the order is shown as disputed

#### Scenario: A repeated workflow step changes nothing

- **WHEN** the completion of any workflow task is delivered to the workflow more than once
- **THEN** the order's next task exists once and the order is not moved back

#### Scenario: Completing a task from the form

- **WHEN** a viewer claims a task on the order page, fills in the rendered form with valid output and submits it
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
