## MODIFIED Requirements

### Requirement: The host can bound the stream by length

The system SHALL allow the host to configure a maximum stream length. Each publish SHALL trim the stream towards that length. By default trimming SHALL be approximate: after a publish the stream SHALL hold at least the configured number of entries whenever at least that many have been published, and MAY hold more. When the host opts into exact trimming, after a publish the stream SHALL hold exactly the configured number of entries whenever at least that many have been published. The system SHALL document that exact trimming costs the broker more work on every publish and is not limited per publish.

#### Scenario: A length bound trims the oldest entries

- **WHEN** a length bound of N is configured and more than N events are published
- **THEN** the oldest entries are removed and the most recent N remain on the stream

#### Scenario: Trimming never goes below the bound

- **WHEN** a length bound of N is configured and events are published
- **THEN** the stream never holds fewer than N entries once N have been published

#### Scenario: A length bound without a trim mode works on an older broker

- **WHEN** a length bound is configured without a trim mode and the broker predates trim modes
- **THEN** the event is delivered and the stream is trimmed

#### Scenario: Exact trimming holds exactly the bound

- **WHEN** a length bound of 2 and exact trimming are configured, the broker keeps its default stream node size, and 10 events are published
- **THEN** the stream holds exactly the 2 most recent entries

#### Scenario: Exact trimming without a bound is refused

- **WHEN** a host opts into exact trimming without configuring a length or age bound
- **THEN** construction fails with a configuration error

### Requirement: The host can bound the stream by age

The system SHALL allow the host to configure a maximum entry age. Each publish SHALL trim entries whose stream identifier is older than the sink's current time minus that age. By default trimming SHALL be approximate: no entry newer than the cutoff SHALL be removed, and entries older than the cutoff MAY remain. When the host opts into exact trimming, a publish SHALL remove every entry older than the cutoff.

#### Scenario: An age bound trims entries older than the cutoff

- **WHEN** an age bound is configured and the stream holds entries older and newer than the cutoff
- **THEN** a publish removes the older entries and keeps the newer ones

#### Scenario: An age bound follows the sink's clock

- **WHEN** the sink is given a clock and an age bound
- **THEN** the cutoff is computed from that clock rather than from the host's wall time

#### Scenario: Exact trimming removes every entry older than the cutoff

- **WHEN** an age bound and exact trimming are configured, the broker keeps its default stream node size, and the stream holds a few entries older and newer than the cutoff
- **THEN** a publish leaves no entry older than the cutoff and keeps every newer one
