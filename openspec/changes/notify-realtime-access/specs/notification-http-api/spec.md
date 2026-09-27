## MODIFIED Requirements

### Requirement: Errors map to status codes consistently

The system SHALL answer:

- a malformed request with `400`. That covers a bad cursor, an out-of-range page size, an unknown state filter, an unparseable instant, a query string that cannot be parsed, and more kind or state filter values than the documented limit;
- a missing acting user or a refused subscription with `403`;
- an unknown notification, or one belonging to someone else, with `404`, identically, so that identifiers cannot be probed;
- too many streams for one recipient with `429`;
- a stream requested while signals are not being received with `503`;
- anything unanticipated with `500`, without internal detail.

A query string that cannot be parsed SHALL be refused as a whole. The system SHALL NOT answer it with the parameters it could parse and silently leave out the rest, because a dropped filter widens a listing and a dropped recipient changes whose stream is opened. That applies whatever the reason the query cannot be parsed, including a malformed escape or more parameters than the parser accepts.

Every error SHALL carry a body with a stable, machine-readable code and a human-readable message, in the same shape as the task HTTP contract's errors.

#### Scenario: Someone else's notification is not found

- **WHEN** bob requests `POST /v1/notifications/{id}/read` for alice's notification
- **THEN** the response is `404`, identical to the response for an identifier that does not exist

#### Scenario: A malformed cursor is a bad request

- **WHEN** alice requests `GET /v1/notifications?cursor=not-a-cursor`
- **THEN** the response is `400` with a validation error code

#### Scenario: An unanticipated failure hides its detail

- **WHEN** the notification store fails with a driver error while alice lists
- **THEN** the response is `500` with a generic message and no driver detail

#### Scenario: A query string past the parameter limit is a bad request, not an unfiltered page

- **WHEN** alice has a `wanted` and an `excluded` notification, and requests `GET /v1/notifications` with `kind=wanted` repeated more times than the query-parameter limit allows
- **THEN** the response is `400` with a validation error code, and no notification is returned

#### Scenario: A malformed escape in the query string is a bad request

- **WHEN** alice requests `GET /v1/notifications?kind=wanted&subject=%zz`
- **THEN** the response is `400` with a validation error code

#### Scenario: An unparseable stream query does not fall back to the caller's own stream

- **WHEN** alice requests `GET /v1/notifications/stream` with a query string that cannot be parsed
- **THEN** the response is `400` and no stream is opened

#### Scenario: Too many kind filters is a bad request

- **WHEN** alice requests `GET /v1/notifications` with more distinct `kind` values than the documented filter limit, within the query-parameter limit
- **THEN** the response is `400` with a validation error code pointing at the kinds, and the store is not queried
