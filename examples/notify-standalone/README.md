# notify-standalone

`notify` on its own, with no tasks at all.

`notify` is a generic, durable inbox of notifications, each addressed to one
recipient. It knows nothing about hmntsk: whoever publishes decides who is told
what, and `notify` stores it, keeps it in step with its subject and serves it.
The `tasknotify` adapter used by [`notifications`](../notifications) is built on
exactly the calls this example makes by hand.

## What it shows

**Default: publish once per source, then read**

- `notify.New(notify.NewMemoryStore())` with nothing else configured.
- `Publish` of two comments. Publishing the same sources again creates nothing
  and counts duplicates: a notification is identified by its source and
  recipient, which is what makes an at-least-once publisher safe.
- `List`, `CountActive`, `MarkRead` and `MarkAllRead`.

**Override: a coalescing draft**

- A reminder with `Coalesce: true` creates nothing while the recipient already
  has an open reminder on that subject.

**Override: closing a subject with a successor, and its watermark**

- `Close` of the subject's comments at a version, with a reason, sparing one
  recipient (`Except`), and publishing a `Successor` (the decision) to everyone
  it closed, in one transaction.
- The close raises the subject's watermark: a comment delivered late for an
  older version is suppressed, while a newer one is created.

## Context

The same fictional business as every example: invoice approval. The subject is
the invoice `invoice/INV-42`; the kinds (`comment`, `reminder`, `decision`) are
this example's own, because `notify` defines none. alice and bob are the finance
approvers. A fixed clock keeps "newest first" and the output identical on every
run.

## What it leaves out

- Tasks. For task events projected into notifications, see
  [`notifications`](../notifications).
- HTTP, streaming, retention and email, which are in
  [`notifications`](../notifications).
- Redis and NATS broadcasters and the WebSocket endpoint, which are in
  [`realtime-scaling`](../realtime-scaling).
- A SQL store. `notify/sqlstore` behaves identically; `notifications` uses it.

## Run it

From the `examples/` directory, with Go only:

```sh
go run ./notify-standalone
go test ./notify-standalone
```

## Read more

- [Notifications](../../notify/docs/notifications.md): the model, publishing and
  closing, watermarks, retention, realtime and HTTP.
- [Schema](../../notify/docs/schema.md) for the SQL store.
