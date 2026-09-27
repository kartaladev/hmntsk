## Why

An audit of `notify` confirmed, each with a failing reproduction test, that realtime access and listing leak or degrade silently:

- a user permitted only to *follow* another user's change signals can mark that user's notifications read over WebSocket;
- a NATS broadcaster whose connection has closed for good keeps the hub "running" forever;
- streams stay open and silent after the hub stops receiving;
- followers can use up a recipient's own stream cap;
- list filters are either unbounded (a driver error, `500`) or silently dropped when the query string cannot be parsed (`200` with unfiltered results).

Each of these breaks a guarantee the notify specs make, or should make. They need fixing before the first tag, while breaking an exported signature is still free.

## What Changes

- **Follow is not write (N1).** WebSocket `mark-read` and `mark-all-read` act on the connection's recipient only when a new, replaceable **mark policy** permits the acting user. The default policy is self-only, so on a follow connection a mark request gets a `forbidden` error reply and changes nothing. New port `notify.MarkAuthorizer` (plus `MarkAuthorizerFunc`) and option `websocket.WithMarkAuthorizer`. A nil policy is a configuration error. The spec line "apply each request to the connection's recipient" is corrected.
- **A closed NATS connection ends receiving (N2).** `notify/nats` `Listen` watches the connection's status and returns an error wrapping `nats.ErrConnectionClosed` once the connection reaches CLOSED. `Hub.Run` then returns that error and `Hub.Running()` becomes false, so new streams are refused with `503`.
- **Stopping the hub closes open streams (N3).** When `Hub.Run` returns, every open subscription ends. SSE responses finish, and WebSocket connections close with status 1013 (try again later), so clients reconnect and re-read. New `Subscription.Done() <-chan struct{}`.
- **Followers get their own budget (N6).** **BREAKING** `Hub.Subscribe(recipient)` becomes `Hub.Subscribe(actor, recipient)`. A recipient's own streams count against `DefaultMaxStreamsPerRecipient` (8, unchanged). Streams opened by other users count against a separate per-recipient follower cap, `DefaultMaxFollowerStreamsPerRecipient` (8), which `WithMaxFollowerStreamsPerRecipient` replaces.
- **Filter lists are bounded (N7).** `ListQuery.Validate` refuses more than `MaxListFilterValues` (100) kinds or states. It also refuses an empty kind, or one longer than `MaxKindBytes`, with a `ValidationError`, so the HTTP answer is `400`. Every store is held to this by the shared `notifytest` suite.
- **An unparseable query string is a bad request (N7′).** The list, stream and WebSocket endpoints parse the query with `url.ParseQuery`. A parse error, including exceeding Go's query-parameter limit, is answered `400` instead of dropping the filters or the recipient.
- **Default policies cannot be reassigned (N9, fixed).** **BREAKING** `notify.SelfOnly` and `notify.AllowAll` change from mutable package variables to functions returning immutable policies. Each policy implements both `SubscriptionAuthorizer` and `MarkAuthorizer`. Constructors no longer read package state for their defaults. `tasknotify.DefaultRules` has the same shape and is out of scope, recorded for a follow-up.
- **N7 at the HTTP layer (refuted).** An oversized kind list cannot reach the store over HTTP, because Go stops parsing at 10,000 parameters. It is not treated as a separate defect. The real HTTP defect is N7′.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `notification-realtime`:
  - WebSocket mark requests are authorized by a separate, replaceable mark policy, self-only by default.
  - Receiving ends with an error when a broadcaster's connection closes for good.
  - Open streams are closed when receiving stops.
  - Followers draw on a follower cap, separate from the recipient's own cap.
  - Default policies are immutable.
  - A malformed query string on stream and socket requests is `400`.
- `notification-http-api`: a query string that cannot be parsed, or a filter list over the limit, is `400`, never a silently unfiltered `200` or a `500`.
- `notification-inbox`: list filters are bounded, and the bound is validated identically for every store.

## Impact

- **Code:**
  - `notify`: `authorize.go`, `hub.go`, `http.go`, `store.go` (`ListQuery`), the generated mocks if any interface changes, and `notifytest/suite.go`.
  - `notify/websocket`: `handler.go`.
  - `notify/nats`: `broadcaster.go`.
  - notify docs: `docs/realtime-operations.md`, `docs/notifications.md`, and package godoc.
  - `examples/notifications/main.go` (uses `notify.SelfOnly`).
- **API (untagged, so breaking is free):**
  - `Hub.Subscribe` gains an `actor` parameter.
  - `SelfOnly` and `AllowAll` become functions.
  - Additions: `MarkAuthorizer`, `MarkAuthorizerFunc`, `websocket.WithMarkAuthorizer`, `Subscription.Done`, `WithMaxFollowerStreamsPerRecipient`, `DefaultMaxFollowerStreamsPerRecipient`, `MaxListFilterValues`.
- **Behaviour a host may notice:**
  - Supervisors who marked a report's notifications read over a follow connection now get `forbidden` until the host installs a mark policy.
  - Malformed query strings that were silently tolerated are now `400`.
- **Split rule:** unchanged. `notify/**` imports no `hmntsk` package.
