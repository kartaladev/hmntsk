## Why

Users need to know about work that concerns them. The notifications must:
- stay active until they are read;
- carry links to the work;
- survive a crash or a redeploy;
- not grow without bound.

None of that is specific to human tasks, and the notifier is designed to become its own repository eventually. So the core is a generic notification library that never imports hmntsk. Task semantics are added by `tasknotify`.

## What Changes

- **New module `notify`** (`github.com/kartaladev/hmntsk/notify`). It imports only `sqlkit` and third-party libraries, never hmntsk.
- **Model:** one `Notification` per recipient, with:
  - `Recipient`;
  - `Subject` and `SubjectVersion`, the thing it is about and that thing's version;
  - `Kind`;
  - `State`: ACTIVE, READ or CLOSED;
  - `ClosedReason`;
  - `Links` (by relation name), `Data` and timestamps.
  - The core names no domain: kinds, relations and data belong to whoever publishes.
- **Store operations:**
  - publish, idempotent on (source ID, recipient);
  - close notifications of given kinds on a subject below a version, optionally sparing one recipient, and optionally publishing a successor to each recipient closed, in the same transaction;
  - coalescing publishes, which create nothing when the recipient already has an open notification of that kind on that subject;
  - a per-subject version watermark, so that a retried older source can never reopen or create an active notification after a newer one closed the subject;
  - list, count unread, mark read, mark all read.
- **Ports, each replaceable:**
  - `Store` (default: `notify/sqlstore`, or in-memory);
  - `Clock`;
  - `SubscriptionAuthorizer` (default `SelfOnly`: a user follows only their own notifications, because every notification is addressed to one recipient; `AllowAll` to opt out; nil is a configuration error);
  - `Broadcaster` (default: in-process, which serves a single instance, a limit stated in the docs).
- **Retention, through a host-driven `Pruner`** (`Prune(ctx)`, `Run(ctx, interval)`; nothing starts on its own):
  - `WithMaxPerRecipient(n)`, one cap for every user, and `WithMaxAge(d)`, which can be combined. Both have documented defaults that apply when the host runs the pruner.
  - `WithRetentionStrategy`: `EvictOldestActive` (**default**) or `RetainActive`. Under `EvictOldestActive`, when a user is over the cap and no inactive notification is left to delete, the oldest active ones are evicted, which breaks "active until read". Under `RetainActive`, only READ or CLOSED notifications are ever deleted. The age bound never deletes an active notification under either strategy.
  - Bounds are approximate between passes. A zero or negative bound, or an unknown strategy, is a configuration error. `PruneResult` reports evicted active notifications separately from deleted inactive ones.
- **HTTP, as `net/http` handlers the host mounts:**
  - list, unread count, mark read, mark all read, and an SSE stream of "unread changed" signals;
  - the recipient comes from the host, as in `transport/core`;
  - Gin mounts the handlers directly, and Fiber through its adaptor (a stated limit).
- **Delivery semantics:** realtime signals are best effort. A reconnecting client re-reads from the store, which is the source of truth.
- **`notify/notifytest`:** a conformance suite that any `notify.Store` must pass.
- **`notify/sqlstore`:** one store over `sqlkit.Executor`, with published DDL per dialect and `VerifySchema`, passing `notifytest` on 7 driver and dialect combinations.
  - Stated limit: notification writes do not join the host's task transaction.
- **Docs** live under `notify/docs/`.

## Capabilities

### New Capabilities

- `notification-inbox`: per-recipient notifications with ACTIVE, READ and CLOSED states, idempotent publishing, subject closing with a version watermark, links, and read operations, identical on every store.
- `notification-retention`: per-recipient count and age bounds, the `EvictOldestActive` and `RetainActive` strategies, and host-driven pruning.
- `notification-realtime`: authorized subscriptions, the `Broadcaster` contract, and the SSE stream of unread-changed signals.
- `notification-http-api`: the HTTP contract for listing, counting and marking notifications read.

### Modified Capabilities

None.

## Impact

- **Modules:** new modules `notify`, `notify/notifytest` and `notify/sqlstore`, all in `NOTIFY_MODULES`. `go.work` and `docs/releasing.md` change.
- **Depends on:** `sqlkit`.
- **Tests:** `notifytest` on memory plus 7 SQL combinations with testcontainers; `goleak` for the hub and pruner goroutines.
- **Future:** `notify` moves to its own repository before its first tag.
- **Unlocks:** `tasknotify`, `notify-realtime-adapters`, `notify-email`.
