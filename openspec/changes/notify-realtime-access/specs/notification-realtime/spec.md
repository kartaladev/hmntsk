## MODIFIED Requirements

### Requirement: Subscriptions are authorized, by default to the acting user's own notifications

The system SHALL authorize every subscription against the acting user the host established, before any signal is sent. By default it SHALL permit a user to subscribe only to their own notifications, and SHALL refuse a subscription when no acting user is established.

The host SHALL be able to replace the default policy with its own, such as one that lets a supervisor follow a team member, or to permit every subscription through an explicitly named opt-out. Supplying no policy SHALL be a configuration error at construction. A refused subscription SHALL be answered as forbidden, with the policy's message.

The library's named policies, the self-only default and the permit-all opt-out, SHALL be fixed. No code in the host process SHALL be able to change what they permit, or what a handler built without a policy applies.

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

#### Scenario: The default policy is the same for every handler in the process

- **WHEN** one handler in a process is built with the permit-all opt-out, and a second handler is built with no policy
- **THEN** the second handler still refuses alice following bob as forbidden

### Requirement: Signals reach every instance through a replaceable broadcaster

The system SHALL pass signals between the place a change is written and the instances holding client connections through a broadcaster. With no configuration it SHALL use an in-process broadcaster, which reaches only connections held by the same process. The system SHALL document that a deployment with more than one instance must supply a broadcaster that crosses instances.

Receiving signals from the broadcaster SHALL run only while the host runs it. A stream requested while signals are not being received SHALL be refused as unavailable, rather than opened and left silent.

Signals SHALL count as being received only once the broadcaster has confirmed that its subscription is in place, not merely once the host has started receiving. A stream requested before that confirmation SHALL be refused as unavailable. Once an instance reports that it is receiving, a signal broadcast afterwards SHALL reach the streams open on that instance, within the best-effort delivery the broadcaster offers. The system SHALL let the host wait for that confirmation without polling. A broadcaster whose subscription cannot be confirmed SHALL end receiving with an error rather than report itself ready.

A broadcaster whose connection to its broker has closed for good, whether the host closed it or its reconnect attempts ran out, SHALL end receiving with an error. The instance SHALL then stop reporting that it is receiving, and SHALL refuse new streams as unavailable. A connection that drops and is still reconnecting is not closed for good. Receiving continues across it, as the best-effort requirement describes.

When receiving ends on an instance, for any reason, the system SHALL close every stream and WebSocket connection open on that instance, so that clients reconnect and re-read rather than wait on a stream that can no longer receive signals.

#### Scenario: One instance works with no configuration

- **WHEN** a single-instance host publishes a notification for alice, who has a stream open on that instance, with no broadcaster configured
- **THEN** alice's stream receives the signal

#### Scenario: A cross-instance broadcaster reaches another instance

- **WHEN** a host supplies a broadcaster shared by instances A and B, a notification for alice is published on A, and alice's stream is open on B
- **THEN** alice's stream on B receives the signal

#### Scenario: A stream is refused while signals are not received

- **WHEN** a client opens a stream on an instance whose host has not started receiving signals
- **THEN** the request is refused as unavailable

#### Scenario: A stream is refused until the subscription is confirmed

- **WHEN** the host has started receiving signals, the broadcaster has not yet confirmed its subscription, and a client opens a stream
- **THEN** the request is refused as unavailable

#### Scenario: A signal broadcast right after readiness is delivered

- **WHEN** instance B reports that it is receiving, alice opens a stream on B, and a notification for alice is published on instance A immediately afterwards with no delay
- **THEN** alice's stream on B receives the signal

#### Scenario: A host waits for readiness without polling

- **WHEN** a host starts receiving signals and waits for the instance to report readiness
- **THEN** the wait ends once the broadcaster has confirmed its subscription, and not before

#### Scenario: A subscription that cannot be confirmed ends receiving with an error

- **WHEN** the cross-instance broker does not confirm the subscription within the broadcaster's subscribe timeout
- **THEN** receiving ends with an error, and the instance never reports that it is receiving

#### Scenario: A host-supplied broadcaster decides when it is ready

- **WHEN** a host supplies its own broadcaster, and that broadcaster reports readiness only after its own subscription is confirmed
- **THEN** streams on that instance are refused as unavailable until it does, and accepted afterwards

#### Scenario: A NATS connection the host closes ends receiving

- **WHEN** an instance is receiving over NATS and the host closes the NATS connection
- **THEN** receiving ends with an error, the instance no longer reports that it is receiving, and a new stream is refused as unavailable

#### Scenario: A NATS connection whose reconnect attempts run out ends receiving

- **WHEN** an instance is receiving over NATS, the broker goes away, and the connection exhausts its reconnect attempts and closes
- **THEN** receiving ends with an error and the instance no longer reports that it is receiving

#### Scenario: Stopping receiving closes an open server-sent event stream

- **WHEN** alice has a server-sent event stream open on an instance and the host stops receiving signals on that instance
- **THEN** alice's stream ends, and no further heartbeat is written to it

#### Scenario: Stopping receiving closes an open WebSocket connection

- **WHEN** alice has a WebSocket connection open on an instance and the host stops receiving signals on that instance
- **THEN** the system closes the connection with a close status telling the client to try again later, and answers no further request on it

### Requirement: Streams stay alive, and slow clients never slow publishers

The system SHALL send a heartbeat on every open stream at a documented interval, **25 seconds** by default and configurable, so that proxies do not close idle streams. Signalling a recipient SHALL NOT wait for any client to receive the signal. When a client cannot keep up, the system SHALL coalesce its pending signals into one rather than queue them without bound. When a client stops accepting writes for longer than a documented write timeout, **10 seconds** by default and configurable, the system SHALL close that client's stream.

The system SHALL cap the open streams for each recipient on each instance with two separate budgets:
- the recipient's own streams, opened by the recipient as acting user, **8** by default and configurable;
- streams following the recipient, opened by any other acting user the subscription policy permits, **8** by default and configurable independently.

A stream beyond its budget SHALL be refused as too many requests. Streams following a recipient SHALL NOT count against that recipient's own budget, so no number of followers can refuse the recipient their own stream.

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

- **WHEN** alice already has the maximum number of her own streams open on an instance and opens another for herself
- **THEN** the request is refused as too many requests

#### Scenario: Followers cannot exhaust the recipient's own streams

- **WHEN** a host policy lets other users follow bob, eight of them each hold a stream following bob on one instance, and bob opens a stream for himself
- **THEN** bob's stream is opened

#### Scenario: Too many followers for one recipient are refused

- **WHEN** eight followers already hold streams following bob on one instance, with the default follower budget, and a ninth follower opens a stream for bob
- **THEN** the ninth request is refused as too many requests, and bob can still open his own stream

#### Scenario: A host changes the follower budget

- **WHEN** a host sets the follower budget to 2, and two followers already hold streams following bob
- **THEN** a third follower's stream for bob is refused as too many requests, while bob's own budget stays at its default of 8

### Requirement: Clients can receive change signals over a WebSocket connection

The system SHALL offer a WebSocket endpoint that delivers a recipient's change signals, as an alternative to the server-sent event stream. It SHALL apply the same rules as the stream:
- the subscription is authorized against the acting user by the same replaceable policy, and a missing policy is a configuration error at construction;
- a signal carries only the kind of change and when it happened;
- connections are refused while signals are not being received, and closed when receiving stops;
- a stalled client never slows a publisher;
- idle connections are kept alive at the same default interval;
- the recipient's own budget and the follower budget each count WebSocket connections and streams together.

A refusal SHALL be answered as an HTTP status before the connection is upgraded:
- bad request for a query string that cannot be parsed;
- forbidden for a missing acting user or a refused subscription;
- too many requests beyond a budget;
- unavailable while signals are not being received.

#### Scenario: A user receives their own signals over WebSocket

- **WHEN** alice opens a WebSocket connection for her own notifications and a notification is published for her
- **THEN** alice's connection receives an unread-changed message naming the change as a creation, with no title, links, data, kind or subject

#### Scenario: Following someone else is refused before upgrading

- **WHEN** alice requests a WebSocket connection for bob's notifications under the default policy
- **THEN** the request is answered as forbidden and the connection is not upgraded

#### Scenario: The cap counts streams and WebSocket connections together

- **WHEN** alice already holds the maximum number of her own connections on an instance, some as streams and some as WebSocket connections, and requests another WebSocket connection for herself
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

#### Scenario: An unparseable query string is a bad request before upgrading

- **WHEN** alice requests a WebSocket connection whose query string cannot be parsed, such as one naming a recipient beyond the query-parameter limit
- **THEN** the request is answered as a bad request and the connection is not upgraded

### Requirement: A WebSocket client can mark notifications read over its connection

The system SHALL accept, on an open WebSocket connection, requests to mark named notifications read and to mark all notifications read. Each request SHALL be authorized against the acting user and the connection's recipient by a mark policy, before anything is changed. The mark policy SHALL be separate from the subscription policy: permission to follow a recipient's signals SHALL NOT grant permission to change that recipient's notifications.

By default the mark policy SHALL permit a user to mark only their own notifications. The host SHALL be able to replace it with its own policy, or to permit every mark through the explicitly named opt-out. Supplying no mark policy SHALL be a configuration error at construction.

A permitted request SHALL be applied to the connection's recipient, with the same outcome as the equivalent HTTP operation. A refused request SHALL be answered with a forbidden reply, SHALL change nothing, and SHALL keep the connection open. Every reply SHALL echo the client's request reference.

A request naming a notification that does not exist or belongs to another recipient SHALL be answered as not found, indistinguishably. A malformed request SHALL be answered as a validation error without closing the connection. A message larger than the documented read limit SHALL close the connection.

#### Scenario: Marking a notification read over the connection

- **WHEN** alice, on a connection for her own notifications, sends a mark-read request with reference `r1` naming her active notification
- **THEN** the notification is READ, alice receives a reply referencing `r1` with one notification marked, and an unread-changed signal naming the change as a read

#### Scenario: Another recipient's notification is not found

- **WHEN** alice, on a connection for her own notifications, sends a mark-read request naming bob's notification
- **THEN** alice receives a not-found reply identical to the one for an identifier that does not exist, and bob's notification is unchanged

#### Scenario: A follower cannot mark the followed recipient's notification read by default

- **WHEN** a host policy lets alice follow bob, alice opens a connection for bob under the default mark policy, and sends a mark-read request with reference `r1` naming bob's active notification
- **THEN** alice receives a forbidden reply referencing `r1`, bob's notification is still ACTIVE, and the connection stays open

#### Scenario: A follower cannot mark all of the followed recipient's notifications read by default

- **WHEN** a host policy lets alice follow bob, alice opens a connection for bob under the default mark policy, and sends a mark-all-read request
- **THEN** alice receives a forbidden reply, and every one of bob's notifications keeps its state

#### Scenario: A host mark policy lets a delegate mark on a recipient's behalf

- **WHEN** a host lets alice follow bob and installs a mark policy permitting alice to mark bob's notifications, and alice sends a mark-read request naming bob's active notification on a connection for bob
- **THEN** bob's notification is READ and alice receives a reply with one notification marked

#### Scenario: A malformed request keeps the connection open

- **WHEN** alice sends a message that is not a recognised request
- **THEN** alice receives a validation error reply and the connection stays open

#### Scenario: An oversized message closes the connection

- **WHEN** alice sends a message larger than the read limit
- **THEN** the system closes the connection as a message too big

### Requirement: Broadcaster and WebSocket wiring mistakes fail at construction

The system SHALL refuse, at construction and before any traffic:
- a broadcaster built with no client or connection;
- an empty channel or subject;
- a NATS subject containing wildcards or empty tokens;
- a WebSocket endpoint built with no way to establish the acting user, with no subscription policy, or with no mark policy;
- a non-positive read limit, heartbeat or write timeout;
- a hub whose own-stream budget or follower budget is below one.

#### Scenario: A NATS broadcaster with a wildcard subject is refused

- **WHEN** a host builds a NATS broadcaster on subject `notify.*`
- **THEN** construction fails with a configuration error

#### Scenario: A Redis broadcaster with no client is refused

- **WHEN** a host builds a Redis broadcaster with no client
- **THEN** construction fails with a configuration error

#### Scenario: A WebSocket endpoint with no mark policy is refused

- **WHEN** a host builds the WebSocket endpoint and supplies an empty mark policy
- **THEN** construction fails with a configuration error

#### Scenario: A hub with no follower budget is refused

- **WHEN** a host builds a hub with a follower budget of zero
- **THEN** construction fails with a configuration error
