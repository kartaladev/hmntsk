## Why

Some users are not looking at the application when work arrives. Email is the fallback, but it must not duplicate messages, flood inboxes, or ride on the event relay's shared attempt budget, where a slow mail server would delay webhooks. Reading from the notification store instead keeps email durable, de-duplicated, and consistent with what the user sees in the app.

## What Changes

- **An email dispatcher in the `notify` package** (no new module: the sender is a host port, so there is no mail dependency). It reads notifications from the notification store and records delivery state per notification in its own table, so by default a notification is emailed at most once however often, and on however many instances, the dispatcher runs.
- **It is host-driven:** `Dispatch(ctx)` for one pass and `Run(ctx, interval)` to loop. Nothing starts on its own.
- **Ports the host supplies, with no library defaults for identity or transport:**
  - `AddressBook` (recipient to address; "no address" is not an error);
  - `Mailer` (sending; it receives a stable idempotency key per message);
  - `EmailTemplate` (subject and body from one recipient's batch of notifications).
- **Opinionated defaults the host can replace:**
  - email only ACTIVE notifications that pass an `EmailFilter` port (default: every kind; `EmailKinds(...)` restricts);
  - wait a grace delay (5 minutes), so a notification read or closed in the app within that window is never emailed;
  - never email a notification older than a maximum lag (24 hours), so enabling email does not send a backlog;
  - batch one recipient's pending notifications into one message per pass (at most 20 per message);
  - at-most-once delivery; at-least-once with the same idempotency key is the opt-in.
- **Out of scope:** user preferences, quiet hours and unsubscribe handling belong to the host. The `EmailFilter` and `AddressBook` ports are where they plug in.
- **Wiring safety:** a missing `Mailer`, `AddressBook` or `EmailTemplate`, a notification store that cannot record email deliveries, or a non-positive duration or limit is a configuration error at construction.

## Capabilities

### New Capabilities

- `notification-email`: emailing active notifications at most once per notification, with a grace delay, per-recipient batching, and host-supplied addressing, templating and sending.

### Modified Capabilities

None. Email delivery state is a separate store port and table, so `notification-inbox` is unchanged; every email requirement, including the store's delivery-state behaviour, lives in `notification-email`. (`notification-inbox` is also still unarchived in `notify-core`, so a delta against it could not validate yet.)

## Impact

- **Modules:** `notify` (dispatcher, ports, in-memory email store), `notify/sqlstore` (a separate `notify_email_deliveries` table with its own optional DDL and schema verification), `notify/notifytest` (an email conformance suite). No new module and no change to `notify.Store` or the notifications table.
- **Depends on:** `notify-core`. The dispatcher knows nothing about tasks; `tasknotify` documents which kinds a task host usually emails.
- **Tests:** at-most-once under concurrent dispatchers on every SQL combination, the grace delay, batching, and `Mailer` failure handling.
