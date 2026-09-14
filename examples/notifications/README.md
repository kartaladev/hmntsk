# notifications

Telling people about their tasks: task events become notifications they list,
count, read and are told about as they happen, over HTTP and a server-sent event
stream.

The pieces are `tasknotify`, a relay sink that turns each task event into
notifications, and `notify`, which stores them, serves them over HTTP, and fans
change signals out to open streams through a hub. Nothing runs on its own: the
host starts the relay and the hub, and optionally a pruner and an email
dispatcher.

## What it shows

**Default: task events become notifications**

- An in-memory notification store, the default rules, links and titles, and the
  self-only stream policy.
- A stream opened by bob receives `unread-changed` when an approval is created.
- `GET /v1/notifications` and `/count`: an offer to each candidate, with a task
  link and a contextual link from the type's `hmntsk.route`.
- bob claims the approval: every offer closes as `taken`, and alice is told who
  took it. The claimant is never notified of their own action.
- `POST /v1/notifications/{id}/read`.

**Override: SQLite, the host's links and titles, a supervisor's stream, retention
and email**

- `notify/sqlstore` over a `sqlkit` executor on SQLite, schema applied and verified.
- `tasknotify.WithTaskLinkTemplate` and `WithTitles`, reading the invoice from the
  host's records.
- A stream for someone else's notifications: refused under the default policy,
  allowed by a supervisor policy.
- An email dispatcher with a host address book, template and mailer, after the
  default five-minute grace delay.
- A pruner with a count bound under `RetainActive`, which never deletes an unread
  notification.

**Override: releasing and delegating, under the default rules**

- A release closes the `taken` notices (`released`) and offers the task again to
  everyone except the releaser.
- A delegation tells the new holder, and closes the previous holder's assignment
  (`reassigned`).

**Override: a widening escalation offers the task to the newly eligible only**

- The sweeper widens an overdue approval to the finance managers. The offers are
  coalescing, so alice and bob are not offered it twice, and carol is offered it.

**Override: host rules, and which statuses close notifications**

- `tasknotify.WithRules` derived from `DefaultRules.Plan`, adding a `done`
  notification to whoever created a task when it is completed.
- `tasknotify.WithClosingStatuses` without `FAILED`: a failed approval's
  notifications stay open.

**Override: email for offers only, and a recipient with no address**

- `notify.WithEmailKinds` emails offers only; a `taken` notice is skipped by the
  filter, and a recipient the address book does not know is skipped, neither as
  an error.

**Override: retention by age, and the default strategy under a count bound**

- A pruner with no options deletes a notification read more than 90 days ago,
  and never an unread one.
- A count bound under the default strategy, `EvictOldestActive`, does evict an
  unread notification, and reports whose.

## Context

The same fictional business as every example: invoice approval.

| Person | Group |
| --- | --- |
| alice, bob | finance-approvers |
| carol | finance-managers |
| dave | auditors |

Every section runs its own engine and notifier on a fixed clock, calls the relay
once where a host would run it every second, and masks generated identifiers,
so the output is identical on every run. A request header stands for the host's
authentication middleware.

## What it leaves out

- Using `notify` without tasks, which is in
  [`notify-standalone`](../notify-standalone).
- Redis and NATS broadcasters across instances, and the WebSocket endpoint,
  which are in [`realtime-scaling`](../realtime-scaling).
- Mounting the handlers on Gin or Fiber, which is in
  [`http-frameworks`](../http-frameworks).

## Run it

From the `examples/` directory, with Go only:

```sh
go run ./notifications
go test ./notifications
```

## Read more

- [Notifying people about their tasks](../../docs/notifications.md): what each
  event does, closing statuses, links, titles, failures.
- [Notifications](../../notify/docs/notifications.md): the model, retention,
  realtime and HTTP.
- [Email](../../notify/docs/email.md) and
  [running realtime notifications](../../notify/docs/realtime-operations.md).
