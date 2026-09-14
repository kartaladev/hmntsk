# context-links

How an inbox links each task to the place its work is done, the invoice's
approval screen, rather than to a generic task page.

## What it shows

- **Type metadata:** a task type carries metadata. The library defines two
  documented keys, `hmntsk.route` (a link template) and `hmntsk.formKey` (a form
  name).
- **Expansion:** `hmntsk.ExpandRoute` fills the template from the task's ID,
  type and correlation.
  - Values are inserted raw, so the host escapes them for where the link points.
  - An invoice numbered `INV 7/B` shows why.
  - Placeholders the library does not recognise, such as `{tenant}`, are left
    for the host.
- **Override:**
  - the host adds its own metadata keys (`acme.icon`, `acme.doneRoute`), stored
    and returned unchanged;
  - a host link resolver picks a different link once the task is completed and
    fills in its own `{tenant}` placeholder.

## Context

The shared invoice-approval domain.

- **The route template:**
  `/invoices/{correlation.ownerRef}/{correlation.activityKey}?task={task.id}`.
- **IDs in the output:** task identifiers are shown as placeholders such as
  `<approval-42>`, so the output is the same on every run.

## What it leaves out

- **Rendering the form:** building a form from the type's schemas is in
  `schema-form`.
- **Links in notifications:** these links reappear in notifications; see
  `notifications`.

## Run it

From the `examples/` directory:

```sh
go run ./context-links
go test ./context-links
```

No Docker is needed.

## Read next

- [Linking a task to its form](../../docs/inbox.md#linking-a-task-to-its-form)
