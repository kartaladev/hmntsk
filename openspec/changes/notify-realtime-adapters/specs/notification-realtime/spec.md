## ADDED Requirements

### Requirement: Clients can receive change signals over a WebSocket connection

The system SHALL offer a WebSocket endpoint that delivers a recipient's change signals, as an alternative to the server-sent event stream. It SHALL apply the same rules as the stream:
- the subscription is authorized against the acting user by the same replaceable policy, and a missing policy is a configuration error at construction;
- a signal carries only the kind of change and when it happened;
- connections are refused while signals are not being received;
- a stalled client never slows a publisher;
- idle connections are kept alive at the same default interval;
- the per-recipient connection cap counts WebSocket connections and streams together.

A refusal SHALL be answered as an HTTP status before the connection is upgraded:
- forbidden for a missing acting user or a refused subscription;
- too many requests beyond the cap;
- unavailable while signals are not being received.

#### Scenario: A user receives their own signals over WebSocket

- **WHEN** alice opens a WebSocket connection for her own notifications and a notification is published for her
- **THEN** alice's connection receives an unread-changed message naming the change as a creation, with no title, links, data, kind or subject

#### Scenario: Following someone else is refused before upgrading

- **WHEN** alice requests a WebSocket connection for bob's notifications under the default policy
- **THEN** the request is answered as forbidden and the connection is not upgraded

#### Scenario: The cap counts streams and WebSocket connections together

- **WHEN** alice already holds the maximum number of connections on an instance, some as streams and some as WebSocket connections, and requests another WebSocket connection
- **THEN** the request is answered as too many requests and the connection is not upgraded

#### Scenario: A connection is refused while signals are not received

- **WHEN** a client requests a WebSocket connection on an instance whose host has not started receiving signals
- **THEN** the request is answered as unavailable and the connection is not upgraded

#### Scenario: An idle connection is kept alive

- **WHEN** a WebSocket connection is open and nothing changes for 60 seconds with the default heartbeat
- **THEN** the server has sent at least two keep-alive pings on the connection

#### Scenario: A stalled WebSocket client is disconnected without slowing publishers

- **WHEN** alice's client stops reading its WebSocket connection, 1,000 notifications are published for her, and the client accepts no writes for longer than the write timeout
- **THEN** every publish completes without waiting on the client, at most one signal is pending for the connection, and the system closes the connection

### Requirement: A WebSocket client can mark notifications read over its connection

The system SHALL accept, on an open WebSocket connection, requests to mark named notifications read and to mark all notifications read. It SHALL apply each request to the connection's recipient only, with the same outcome as the equivalent HTTP operation, and SHALL answer each request with a reply that echoes the client's request reference. A request naming a notification that does not exist or belongs to another recipient SHALL be answered as not found, indistinguishably. A malformed request SHALL be answered as a validation error without closing the connection. A message larger than the documented read limit SHALL close the connection.

#### Scenario: Marking a notification read over the connection

- **WHEN** alice sends a mark-read request with reference `r1` naming her active notification
- **THEN** the notification is READ, alice receives a reply referencing `r1` with one notification marked, and an unread-changed signal naming the change as a read

#### Scenario: Another recipient's notification is not found

- **WHEN** alice sends a mark-read request naming bob's notification
- **THEN** alice receives a not-found reply identical to the one for an identifier that does not exist, and bob's notification is unchanged

#### Scenario: A malformed request keeps the connection open

- **WHEN** alice sends a message that is not a recognised request
- **THEN** alice receives a validation error reply and the connection stays open

#### Scenario: An oversized message closes the connection

- **WHEN** alice sends a message larger than the read limit
- **THEN** the system closes the connection as a message too big

### Requirement: WebSocket connections from foreign browser origins are refused by default

The system SHALL, by default, refuse a WebSocket connection whose browser origin differs from the host the request was made to. The host SHALL be able to permit a documented list of origin patterns, or to permit every origin through an explicitly named opt-out.

#### Scenario: A same-origin connection is accepted

- **WHEN** a browser page served from the application's own host opens a WebSocket connection
- **THEN** the connection is accepted, subject to authorization

#### Scenario: A foreign origin is refused by default

- **WHEN** a page served from another site opens a WebSocket connection with alice's credentials
- **THEN** the request is refused as forbidden and the connection is not upgraded

#### Scenario: A host permits a listed origin

- **WHEN** a host permits the origin pattern `app.example.com` and a page from `app.example.com` opens a connection to `api.example.com`
- **THEN** the connection is accepted, subject to authorization

### Requirement: Signals cross instances over Redis or NATS

The system SHALL offer two broadcasters that pass change signals between application instances: one over Redis publish/subscribe and one over NATS core subjects. Each SHALL deliver a signal broadcast on any instance to every instance receiving signals through the same channel or subject. Each SHALL use a documented default channel or subject that the host can replace, distinct from the ones used for durable event delivery.

#### Scenario: A signal written on one instance reaches a stream on another over Redis

- **WHEN** instances A and B share a Redis broadcaster, a notification for alice is published on A, and alice's stream is open on B
- **THEN** alice's stream on B receives the signal

#### Scenario: A signal written on one instance reaches a WebSocket on another over NATS

- **WHEN** instances A and B share a NATS broadcaster, a notification for alice is published on A, and alice's WebSocket connection is open on B
- **THEN** alice's connection on B receives the signal

#### Scenario: Instances on different channels do not see each other

- **WHEN** instance A broadcasts on channel `app-one.signals` and instance B receives on `app-two.signals`
- **THEN** B receives no signal from A

### Requirement: Cross-instance broadcasting is best effort and carries no content

A broadcaster SHALL carry only the recipient, the kind of change and when it happened. It SHALL NOT carry a notification's title, links, data, kind or subject.

Broadcasting SHALL be best effort:
- a broker that cannot be reached SHALL NOT fail or roll back the notification write that produced the signal;
- signals broadcast while an instance is disconnected SHALL NOT be replayed to it;
- an instance SHALL resume receiving signals once its connection to the broker recovers, without the host restarting it.

The notification store SHALL remain the source of truth.

#### Scenario: A broker outage does not fail publishing

- **WHEN** the broker is unreachable and a notification is published for alice
- **THEN** the notification is stored and the publish succeeds, and the broadcast failure is reported to the host's handler

#### Scenario: Receiving resumes after the broker recovers

- **WHEN** an instance's connection to the broker drops and recovers, and a notification for alice is then published on another instance
- **THEN** alice's stream on the recovered instance receives the signal

#### Scenario: A broadcast message carries no content

- **WHEN** a notification with a title, links and data is published and broadcast
- **THEN** the message on the broker contains the recipient, the change and its time, and none of the title, links, data, kind or subject

### Requirement: Unrecognised broadcast messages are dropped, not delivered

The system SHALL tag every broadcast message with a format version. An instance receiving a message with an unknown version, or one it cannot decode, SHALL deliver no signal for it, SHALL report it to the host's handler, and SHALL keep receiving.

#### Scenario: A message from a newer format is ignored

- **WHEN** an instance receives a broadcast message tagged with a format version it does not know
- **THEN** no stream receives a signal for it, the host's handler is told, and later valid messages are still delivered

### Requirement: Broadcaster and WebSocket wiring mistakes fail at construction

The system SHALL refuse, at construction and before any traffic:
- a broadcaster built with no client or connection;
- an empty channel or subject;
- a NATS subject containing wildcards or empty tokens;
- a WebSocket endpoint built with no way to establish the acting user, or with no subscription policy;
- a non-positive read limit, heartbeat or write timeout.

#### Scenario: A NATS broadcaster with a wildcard subject is refused

- **WHEN** a host builds a NATS broadcaster on subject `notify.*`
- **THEN** construction fails with a configuration error

#### Scenario: A Redis broadcaster with no client is refused

- **WHEN** a host builds a Redis broadcaster with no client
- **THEN** construction fails with a configuration error
