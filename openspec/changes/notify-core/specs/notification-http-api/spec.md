## Purpose

Defines the HTTP contract through which a user's client lists their notifications, reads their unread count, marks notifications read, and opens a stream of change signals, served by handlers the host mounts on its own router.

## ADDED Requirements

### Requirement: Every endpoint acts on the acting user's own notifications

The system SHALL take the acting user from the host, and SHALL NOT authenticate anyone itself. Listing, counting and marking read SHALL always act on the acting user's own notifications, and SHALL NOT accept a parameter naming another recipient. A request with no acting user established SHALL be refused as forbidden. Supplying no way to establish the acting user SHALL be a configuration error at construction.

#### Scenario: Listing returns only the caller's notifications

- **WHEN** alice lists notifications and both alice and bob have notifications
- **THEN** the response contains only alice's

#### Scenario: No acting user is refused

- **WHEN** a request arrives for which the host established no acting user
- **THEN** it is answered `403` and nothing is read or changed

#### Scenario: A handler with no actor source is refused at construction

- **WHEN** a host constructs the notification handlers without supplying how the acting user is established
- **THEN** construction fails with a configuration error

### Requirement: The contract exposes list, count, mark read, mark all read and stream

Under a base path, `/v1` by default and configurable, the system SHALL expose:

- `GET /notifications`: the acting user's notifications, newest first. Optional filters are state, kind and subject. The page size defaults to 50 and is capped at 500. Paging uses a continuation cursor.
- `GET /notifications/count`: the number of the acting user's ACTIVE notifications.
- `POST /notifications/{id}/read`: mark one notification read.
- `POST /notifications/read-all`: mark everything read up to an instant the client may supply, defaulting to the time of the request.
- `GET /notifications/stream`: a server-sent event stream of the acting user's change signals and heartbeats.

A notification SHALL be returned with its identifier, kind, state, closed reason, title, links, data and timestamps. Its data SHALL be returned exactly as published.

#### Scenario: Listing a page with a cursor

- **WHEN** alice requests `GET /v1/notifications?limit=2` over five notifications, then requests the returned cursor
- **THEN** the first response holds her two newest notifications and a cursor, and the second holds the next two

#### Scenario: Counting unread notifications

- **WHEN** alice, with three ACTIVE notifications, requests `GET /v1/notifications/count`
- **THEN** the response is `200` with a count of 3

#### Scenario: Marking one notification read

- **WHEN** alice requests `POST /v1/notifications/{id}/read` for her ACTIVE notification
- **THEN** the response is `200` with the notification now READ

#### Scenario: Opening the stream

- **WHEN** alice requests `GET /v1/notifications/stream`
- **THEN** the response is a server-sent event stream that delivers her change signals and heartbeats until she disconnects

### Requirement: Errors map to status codes consistently

The system SHALL answer:

- a malformed request (a bad cursor, an out-of-range page size, an unknown state filter, an unparseable instant) with `400`;
- a missing acting user or a refused subscription with `403`;
- an unknown notification, or one belonging to someone else, with `404`, identically, so that identifiers cannot be probed;
- too many streams for one recipient with `429`;
- a stream requested while signals are not being received with `503`;
- anything unanticipated with `500`, without internal detail.

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

### Requirement: The handlers mount on any standard-library-compatible router

The system SHALL provide its handlers as standard library HTTP handlers the host mounts on its own router, behind its own middleware. The system SHALL document how to mount them on the standard library, Gin and Fiber, and SHALL document that on a framework not built on the standard library's HTTP types they run through that framework's adaptor.

#### Scenario: Mounting on the standard library

- **WHEN** a host mounts the handlers on a standard library mux under `/v1`
- **THEN** every endpoint of the contract answers under `/v1`
