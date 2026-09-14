# inbox-buckets

An inbox built from queries: "mine", "available", "overdue" and a team's queue
are not things hmntsk stores. They are queries the host names, ordered,
paged and counted.

## What it shows

- **Orderings (Go API):**
  - creation (the default), priority, due date and urgency, plus a descending
    ordering;
  - under due date and urgency, tasks without a deadline sort last.
- **Paging:** exact paging with a cursor, two tasks per page. A cursor from one
  ordering is refused under another.
- **Buckets:** `Service.CountBuckets` counts several named buckets in one call.
- **HTTP, default:** queries answer only for the caller's own inbox
  (`transportcore.SelfOnly`).
  - `candidate=me` and `assignee=me` are allowed.
  - Another person's inbox and a group's queue are refused with 403.
  - The removed `order` parameter is refused with 400.
- **HTTP, override:** a host `transportcore.WithQueryAuthorizer` policy lets a
  supervisor (carol) read the `finance-approvers` queue. Every other query goes
  back to the self-only default.

## Context

The shared invoice-approval domain.

- **Tasks:** five approvals, INV-1 to INV-5, with priorities and due dates
  chosen so that every ordering comes out differently. alice has claimed one of
  them.
- **Identity:** the demo's actor header stands for your authentication
  middleware.

## What it leaves out

- **Reading a single task:** that authorization is in `record-page`.
- **Linking a task to its page:** that is in `context-links`.
- **A browser page over these buckets:** that is `contextual-ui`.

## Run it

From the `examples/` directory:

```sh
go run ./inbox-buckets
go test ./inbox-buckets
```

It serves the HTTP contract on a free loopback port while it runs. No Docker is
needed.

## Read next

- [Building an inbox](../../docs/inbox.md)
