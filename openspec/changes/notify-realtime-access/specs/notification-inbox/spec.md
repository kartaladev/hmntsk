## MODIFIED Requirements

### Requirement: A recipient can list, count and mark their notifications read

The system SHALL let a recipient:

- list their own notifications, newest first, filtered by state, kind or subject, in pages that repeat and skip nothing however many notifications arrive between pages;
- count their ACTIVE notifications;
- mark chosen notifications read;
- mark every notification read up to a given instant.

A listing SHALL accept at most a documented number of kind filter values and, separately, of state filter values, **100** each. Every kind filter value SHALL be non-empty and no longer than the longest kind a notification may have. A listing beyond these limits SHALL be refused as a validation error naming the offending filter, before any store is queried. It SHALL NOT fail as an internal error from the underlying database, and every store SHALL answer a listing within the limits. The limit is fixed by the library, not configurable, because it keeps every supported database within its bound-parameter budget.

Marking an ACTIVE notification read SHALL make it READ. Marking a READ notification read SHALL succeed and change nothing. Marking a CLOSED notification read SHALL record the read time and leave it CLOSED. A recipient SHALL NOT be able to read, list, count or mark another recipient's notifications through these operations. A notification of another recipient SHALL be reported as not found.

#### Scenario: Newest first with exact paging

- **WHEN** a recipient lists with a page size of 2 over five notifications, and a sixth arrives before the second page is requested
- **THEN** the pages together return the original five exactly once, newest first

#### Scenario: Counting counts only active notifications

- **WHEN** a recipient has three ACTIVE, two READ and one CLOSED notification
- **THEN** their count is 3

#### Scenario: Mark all read does not swallow what arrived later

- **WHEN** a recipient marks everything read up to the instant they loaded their list, and a notification created after that instant already exists
- **THEN** that later notification is still ACTIVE

#### Scenario: Another recipient's notification is not found

- **WHEN** bob marks alice's notification read
- **THEN** the operation reports not found, and alice's notification is unchanged

#### Scenario: Too many kind filters are a validation error on every store

- **WHEN** a recipient lists with 70,000 kind filter values, on any store, including SQLite and PostgreSQL
- **THEN** the listing is refused as a validation error naming the kinds, not a database error

#### Scenario: Too many state filter values are a validation error

- **WHEN** a recipient lists with 70,000 state filter values, each of them ACTIVE
- **THEN** the listing is refused as a validation error naming the states

#### Scenario: A listing at the limit is answered on every store

- **WHEN** a recipient lists with exactly the maximum number of kind filter values, on any store
- **THEN** the listing succeeds and returns only notifications of those kinds

#### Scenario: An empty or overlong kind filter is a validation error

- **WHEN** a recipient lists with a kind filter value that is empty, or longer than the longest kind a notification may have
- **THEN** the listing is refused as a validation error pointing at that value
