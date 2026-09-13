## Purpose

Defines how a connected client learns, without polling, that a recipient's notifications changed: who may subscribe to whose changes, what a signal carries, how signals reach every application instance, and how the system protects publishers from slow or disconnected clients.

## ADDED Requirements

### Requirement: A signal says that notifications changed, never what they contain

The system SHALL signal a recipient's subscribers whenever that recipient's notifications change. That covers creation, being marked read, being closed, and an active notification evicted by pruning. A signal SHALL carry only the kind of change and when it happened. It SHALL NOT carry a notification's title, links, data, kind or subject. A client SHALL learn the current state by reading the notification store, which remains the source of truth.

#### Scenario: Publishing signals the recipient

- **WHEN** a notification is published for alice while alice has an open stream
- **THEN** alice's stream receives an unread-changed signal naming the change as a creation

#### Scenario: A signal carries no content

- **WHEN** a notification with a title, links and data is published for alice
- **THEN** the signal on alice's stream contains none of the title, the links or the data

### Requirement: Subscriptions are authorized, by default to the acting user's own notifications

The system SHALL authorize every subscription against the acting user the host established, before any signal is sent. By default it SHALL permit a user to subscribe only to their own notifications, and SHALL refuse a subscription when no acting user is established.

The host SHALL be able to replace the default policy with its own, such as one that lets a supervisor follow a team member, or to permit every subscription through an explicitly named opt-out. Supplying no policy SHALL be a configuration error at construction. A refused subscription SHALL be answered as forbidden, with the policy's message.

#### Scenario: A user follows their own notifications by default

- **WHEN** alice opens a stream for her own notifications
- **THEN** the subscription is permitted

#### Scenario: Following someone else is refused by default

- **WHEN** alice opens a stream for bob's notifications under the default policy
- **THEN** the request is refused as forbidden and no stream is opened

#### Scenario: A host policy permits a supervisor

- **WHEN** a host replaces the default with a policy permitting supervisors to follow their reports, and alice, bob's supervisor, opens a stream for bob
- **THEN** the subscription is permitted and alice receives bob's signals

#### Scenario: A missing policy is refused at construction

- **WHEN** a host configures the realtime handler with no subscription policy
- **THEN** construction fails with a configuration error

### Requirement: Signals reach every instance through a replaceable broadcaster

The system SHALL pass signals between the place a change is written and the instances holding client connections through a broadcaster. With no configuration it SHALL use an in-process broadcaster, which reaches only connections held by the same process. The system SHALL document that a deployment with more than one instance must supply a broadcaster that crosses instances.

Receiving signals from the broadcaster SHALL run only while the host runs it. A stream requested while signals are not being received SHALL be refused as unavailable, rather than opened and left silent.

#### Scenario: One instance works with no configuration

- **WHEN** a single-instance host publishes a notification for alice, who has a stream open on that instance, with no broadcaster configured
- **THEN** alice's stream receives the signal

#### Scenario: A cross-instance broadcaster reaches another instance

- **WHEN** a host supplies a broadcaster shared by instances A and B, a notification for alice is published on A, and alice's stream is open on B
- **THEN** alice's stream on B receives the signal

#### Scenario: A stream is refused while signals are not received

- **WHEN** a client opens a stream on an instance whose host has not started receiving signals
- **THEN** the request is refused as unavailable

### Requirement: Streams stay alive, and slow clients never slow publishers

The system SHALL send a heartbeat on every open stream at a documented interval, **25 seconds** by default and configurable, so that proxies do not close idle streams. Signalling a recipient SHALL NOT wait for any client to receive the signal. When a client cannot keep up, the system SHALL coalesce its pending signals into one rather than queue them without bound. When a client stops accepting writes for longer than a documented write timeout, **10 seconds** by default and configurable, the system SHALL close that client's stream. The system SHALL cap the open streams per recipient on each instance, **8** by default and configurable, and SHALL refuse a stream beyond the cap as too many requests.

#### Scenario: An idle stream receives heartbeats

- **WHEN** a stream is open and nothing changes for 60 seconds with the default heartbeat
- **THEN** the client receives at least two heartbeats

#### Scenario: A stalled client does not block publishing

- **WHEN** alice's client has stopped reading its stream and 1,000 notifications are published for alice
- **THEN** every publish completes without waiting on the client, and alice's stream holds at most one pending signal

#### Scenario: A stalled client is disconnected

- **WHEN** alice's client accepts no writes for longer than the write timeout while a signal is pending
- **THEN** the system closes alice's stream

#### Scenario: Too many streams for one recipient are refused

- **WHEN** alice already has the maximum number of streams open on an instance and opens another
- **THEN** the request is refused as too many requests

### Requirement: A reconnecting client re-reads rather than replays

The system SHALL NOT replay signals missed while a client was disconnected. The system SHALL document that a client, on connecting or reconnecting, reads its unread count and any open list from the notification store before relying on signals.

#### Scenario: Signals missed while disconnected are not replayed

- **WHEN** alice's stream is closed, three notifications are published for her, and she reconnects
- **THEN** her new stream receives no signal for those three, and reading her unread count returns them
