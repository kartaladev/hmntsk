## ADDED Requirements

### Requirement: One delivery attempt publishes at most once

The relay owns the retry budget, so one delivery attempt of the Redis sink SHALL append the event to the stream at most once. Where the client's retry settings can be inspected, the sink SHALL refuse at construction a client that would re-send a failed write inside one attempt, and report a configuration error that names the setting to change. A host SHALL be able to opt into client retries explicitly, accepting that one attempt may then append the event more than once. Where the client cannot be inspected, the requirement SHALL be documented on the constructor.

#### Scenario: A client built with default retry settings is refused

- **WHEN** a host constructs the sink with a single-node client built with the client library's default retry setting
- **THEN** construction fails with a configuration error that names the retry setting and the value that disables it

#### Scenario: A client with retries disabled publishes once when the reply is lost

- **WHEN** the sink is constructed with a client whose retries are disabled, and the connection is severed after the broker has appended the entry but before the reply reaches the client
- **THEN** the attempt is reported retryable and the stream holds exactly one entry for that delivery identifier

#### Scenario: A host opts into client retries

- **WHEN** a host constructs the sink with a client that retries and explicitly opts into client retries
- **THEN** construction succeeds, and the sink documents that one attempt may append the event more than once under one delivery identifier

#### Scenario: A client the sink cannot inspect is accepted

- **WHEN** a host constructs the sink with a client implementation whose retry settings the sink cannot read
- **THEN** construction succeeds, and ensuring that the client does not retry is the host's documented responsibility
