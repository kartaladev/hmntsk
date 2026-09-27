## ADDED Requirements

### Requirement: Generated identifiers fit every store

The system SHALL bound every identifier it generates, including notification identifiers and email message keys, to a documented maximum of 64 bytes. It SHALL enforce that bound identically on every store, so that one generator never works on one database and fails on another. By default the system SHALL generate identifiers within the bound. The host SHALL be able to supply its own generator. When the service is constructed, the system SHALL obtain one identifier from the host's generator and SHALL refuse construction with a configuration error if that identifier is empty or longer than the bound. An identifier generated later that is empty or longer than the bound SHALL fail the operation that needed it, with an error naming the bound, before anything is written.

#### Scenario: The default generator publishes on every store

- **WHEN** a service with no identifier generator configured publishes a notification on each supported store
- **THEN** the publish succeeds everywhere, and the identifier is at most 64 bytes

#### Scenario: A host generator within the bound works on every store

- **WHEN** a host supplies a generator that mints 64-byte identifiers, and publishes a notification on each supported store
- **THEN** the publish succeeds everywhere, and reading the notification back returns the 64-byte identifier

#### Scenario: A host generator over the bound fails at construction

- **WHEN** a host supplies a generator that mints 100-byte identifiers and constructs the service over any supported store
- **THEN** construction fails with a configuration error that names the 64-byte bound

#### Scenario: A generator that later exceeds the bound writes nothing

- **WHEN** a host generator mints a valid identifier at construction and a 100-byte identifier on the next publish
- **THEN** the publish fails with an error naming the bound, nothing is stored, and the same outcome holds on every supported store
