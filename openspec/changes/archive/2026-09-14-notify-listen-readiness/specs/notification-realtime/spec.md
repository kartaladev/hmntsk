## MODIFIED Requirements

### Requirement: Signals reach every instance through a replaceable broadcaster

The system SHALL pass signals between the place a change is written and the instances holding client connections through a broadcaster. With no configuration it SHALL use an in-process broadcaster, which reaches only connections held by the same process. The system SHALL document that a deployment with more than one instance must supply a broadcaster that crosses instances.

Receiving signals from the broadcaster SHALL run only while the host runs it. A stream requested while signals are not being received SHALL be refused as unavailable, rather than opened and left silent.

Signals SHALL count as being received only once the broadcaster has confirmed that its subscription is in place, not merely once the host has started receiving. A stream requested before that confirmation SHALL be refused as unavailable. Once an instance reports that it is receiving, a signal broadcast afterwards SHALL reach the streams open on that instance, within the best-effort delivery the broadcaster offers. The system SHALL let the host wait for that confirmation without polling. A broadcaster whose subscription cannot be confirmed SHALL end receiving with an error rather than report itself ready.

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
