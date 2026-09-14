# record-page

What an invoice's own page needs: every task about that invoice, and who may
read one of those tasks over HTTP.

## What it shows

- **The record page (Go API):**
  - it lists tasks by correlation (`OwnerType` and `OwnerRef`), whatever their
    status;
  - the host has already decided the viewer may see the invoice, so it lists
    server-side.
- **HTTP queries:** a bare correlation filter is refused, because it names
  everybody's tasks. `candidate=me` combined with the correlation filter is
  allowed.
- **Single-task reads, default (`transportcore.ParticipantsOnly`):**
  - a candidate or the creator may read the task;
  - someone taking no part is refused with 403;
  - with no acting user the answer is 403, before the task is looked up;
  - an unknown task is 404.
- **Single-task reads, override:** a `transportcore.WithTaskReadAuthorizer`
  policy lets auditors read any task and hands everyone else back to the
  default. It still never serves a read without an acting user.

## Context

The shared invoice-approval domain.

- **Tasks:**
  - INV-42 has a review completed by bob and an approval waiting;
  - INV-43 has an approval waiting.
- **People:** dave, the auditor, takes part in no task.

## What it leaves out

- **Inbox buckets:** queries across many records are in `inbox-buckets`.
- **Links:** how a task links back to this page is in `context-links`.

## Run it

From the `examples/` directory:

```sh
go run ./record-page
go test ./record-page
```

No Docker is needed.

## Read next

- [Who may read a task](../../docs/inbox.md#who-may-read-a-task)
