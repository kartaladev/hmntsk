## ADDED Requirements

### Requirement: A recorded failure names each refusing sink once

The system SHALL record one part per refusing sink in an event's last error, and each part SHALL name the sink once. When a sink's own message already begins with the sink's name followed by a colon, the relay SHALL NOT add the name again. The same rule SHALL apply to the text of the error passed to the host's error handler.

#### Scenario: A sink whose message carries its own name

- **WHEN** a sink named `webhook` refuses an event with a message beginning `webhook: `
- **THEN** the recorded last error begins `webhook: ` exactly once, not `webhook: webhook: `

#### Scenario: A renamed sink is still identified

- **WHEN** a host renames the sink to `partner-hook`, and it refuses an event with a message beginning `webhook: `
- **THEN** the recorded part is `partner-hook: webhook: …`, so the configured name is still what identifies the sink
