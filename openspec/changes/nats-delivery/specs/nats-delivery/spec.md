## Purpose

Defines publication of task events to NATS, for hosts whose internal consumers use NATS: on plain subjects, where delivery means the server received the message, and on JetStream, where delivery means a stream stored it.

## ADDED Requirements

### Requirement: Two delivery modes, each under its own sink name

The system SHALL offer two NATS sinks, one publishing to plain subjects and one publishing to JetStream. Each SHALL have a default sink name distinct from the other's, so that the per-sink acceptance the relay records for one mode is never taken as acceptance by the other.

#### Scenario: Default names differ

- **WHEN** a plain-subject sink and a JetStream sink are constructed with default names
- **THEN** their names differ

#### Scenario: Switching mode does not skip events

- **WHEN** a host that delivered events through the plain-subject sink switches to the JetStream sink, both using their default names
- **THEN** events the plain-subject sink already accepted are still offered to the JetStream sink

### Requirement: Events are published to a subject derived from the event type

The system SHALL publish each event to a subject made of a subject prefix followed by the event type, so that a consumer can select event types with subject wildcards on the server. The prefix SHALL default to `hmntsk.events` and SHALL be configurable. The task type and correlation data SHALL NOT be part of the subject.

#### Scenario: Default subject

- **WHEN** a sink with the default prefix publishes a `task.completed` event
- **THEN** the message is published to `hmntsk.events.task.completed`

#### Scenario: Configured prefix

- **WHEN** a sink constructed with the prefix `acme.tasks` publishes a `task.claimed` event
- **THEN** the message is published to `acme.tasks.task.claimed`

#### Scenario: A task type that is not a valid subject token

- **WHEN** an event is published for a task whose type name contains a character that is not valid in a subject, such as a space or a wildcard
- **THEN** the event is published to the subject for its event type, unaffected by the task type

### Requirement: Messages carry routing headers and the whole event

Every published message SHALL carry headers for the event identifier, the attempt's delivery identifier, the attempt number, the event type, the task identifier, the task type and the message contract version, and a header for each correlation value the task carries. Correlation headers SHALL be omitted when the value is empty. The message body SHALL be the whole event as JSON.

The body SHALL be the authoritative copy of every value. Where a header value contains a character that NATS header framing cannot carry, the header MAY hold a sanitised form of the value while the body holds the original.

#### Scenario: A consumer routes on headers alone

- **WHEN** a consumer receives a published message
- **THEN** the event identifier, event type, task type and correlation values are readable from the headers without parsing the body

#### Scenario: The body carries the whole event

- **WHEN** a consumer parses the body of a published message
- **THEN** it obtains the event exactly as recorded, including its output and callback target

#### Scenario: Empty correlation values are omitted

- **WHEN** an event is published for a task with no correlation data
- **THEN** the message carries no correlation headers

#### Scenario: A header value NATS cannot frame

- **WHEN** an event is published for a task whose type name contains a line break
- **THEN** the event is delivered, the task type header holds a sanitised value, and the body holds the original type name

### Requirement: Plain-subject delivery means the server received the message

The plain-subject sink SHALL report an event delivered only once the NATS server has confirmed receiving everything published before it. It SHALL NOT report delivered while a message is only buffered in the client, for example during a reconnect. Delivered SHALL NOT imply that any subscriber received the message: an event published while no subscriber matches its subject is delivered and not offered again.

#### Scenario: A subscriber receives the event

- **WHEN** a subscriber is listening on the event's subject and the plain-subject sink publishes the event
- **THEN** the event is delivered and the subscriber receives one message for it

#### Scenario: No subscriber is listening

- **WHEN** no subscriber matches the event's subject and the plain-subject sink publishes the event
- **THEN** the event is reported delivered, and no subscriber ever receives it

#### Scenario: The server cannot confirm receipt in time

- **WHEN** the connection to the server is lost or reconnecting and the attempt's timeout expires before the server confirms receipt
- **THEN** the failure is retryable and the event is not reported delivered

### Requirement: JetStream delivery means a stream stored the message

The JetStream sink SHALL report an event delivered only when a stream has acknowledged storing it. Every publication SHALL carry the event identifier as its message identifier, so the stream discards a redelivery of the same event within its duplicate window. A publication the stream acknowledges as a duplicate SHALL be reported delivered.

#### Scenario: A stream stores the event

- **WHEN** a stream captures the event's subject and the JetStream sink publishes the event
- **THEN** the event is delivered and the stream holds one message for it, identified by the event identifier

#### Scenario: A redelivery within the duplicate window

- **WHEN** the same event is published twice within the stream's duplicate window
- **THEN** both attempts are reported delivered and the stream holds one message for the event

#### Scenario: No stream captures the subject

- **WHEN** no stream captures the event's subject and the JetStream sink publishes the event
- **THEN** the failure is retryable, the event remains undelivered, and nothing is stored

### Requirement: The JetStream sink neither creates streams nor retries on its own

The JetStream sink SHALL NOT create, update or delete streams; stream configuration, including retention, replicas and the duplicate window, is the host's. Each delivery attempt SHALL make a single publication, leaving every retry to the relay.

The host MAY name the stream a publication is expected to land in; a publication that would land in a different stream SHALL fail as retryable and SHALL NOT be stored.

#### Scenario: No stream is created

- **WHEN** the JetStream sink publishes events and no stream captures their subjects
- **THEN** the set of streams on the server is unchanged

#### Scenario: A missing stream is not retried within the attempt

- **WHEN** no stream captures the event's subject and the JetStream sink publishes the event
- **THEN** the attempt returns its retryable failure after a single publication, without a retry of its own

#### Scenario: The publication would land in an unexpected stream

- **WHEN** the host names an expected stream and the event's subject is captured by a different stream
- **THEN** the failure is retryable and the message is not stored

### Requirement: Failures no further attempt can change are permanent

Both NATS sinks SHALL classify as permanent a failure that is a property of the event or of the sink's configuration rather than of the broker at that moment: an event that cannot be rendered as a message, and a message larger than the server accepts. Every other failure SHALL be retryable.

#### Scenario: An event without an identifier

- **WHEN** a sink is asked to deliver an event that has no identifier
- **THEN** the failure is permanent and nothing is published

#### Scenario: A message larger than the server accepts

- **WHEN** an event's message exceeds the server's maximum payload
- **THEN** the failure is permanent and nothing is published

### Requirement: NATS sink wiring mistakes are rejected at construction

The system SHALL reject, when a NATS sink is constructed and before any event is published: a missing connection or JetStream context, an empty sink name, a timeout that is not positive, and a subject prefix that is not a valid subject for publishing, such as an empty prefix, one with an empty token, or one containing a wildcard or whitespace.

#### Scenario: A missing connection

- **WHEN** a NATS sink is constructed without a connection or JetStream context
- **THEN** construction fails with a configuration error and no sink is returned

#### Scenario: An invalid subject prefix

- **WHEN** a NATS sink is constructed with a prefix that is empty, has an empty token, or contains a wildcard or whitespace
- **THEN** construction fails with a configuration error and no sink is returned

#### Scenario: A non-positive timeout or empty name

- **WHEN** a NATS sink is constructed with a timeout of zero or less, or an empty name
- **THEN** construction fails with a configuration error and no sink is returned
