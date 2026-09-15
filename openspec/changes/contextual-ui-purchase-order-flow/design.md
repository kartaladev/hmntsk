## Context

`contextual-ui` is an example, not library code: it shows a host embedding
hmntsk. Nothing in this change touches the library, so the library-design rule
applies to what the demo teaches a host to do, not to a public API. See
proposal.md for motivation and specs/usage-examples/spec.md for the required
behaviour.

Before the change the demo reused `examples/internal/invoicing`, tied every task
to an invoice (`ownerType: invoice`), and created the invoice with its review in
the order's transaction. A relay sink, `invoice-workflow`, created the approval
after a review completed.

## Goals / Non-Goals

**Goals:**

- Show a host workflow with a decision before work, work whose form depends on
  host data, and work started by something arriving later.
- Keep every workflow step idempotent under at-least-once delivery.
- Show a data grid over a cursor-paged task list without misleading the viewer.

**Non-Goals:**

- Real supplier integration: nothing is emailed; invoices are made up from the order.
- Preventing a purchase order task being completed through the generic task API
  with a made-up document. The README states this limit.
- Changing the shared `invoicing` package used by other examples.

## Decisions

### Tasks correlate to the order, not the invoice

Every task has `ownerType: order` and `ownerRef` the order ID, with activity keys
`approve-order`, `purchase-order`, `review-invoice` and `approve-invoice`. One
record page reads every task with one correlation query, and the invoice (which
does not exist until late in the flow) is shown on it.

*Alternative:* keep invoice tasks on `ownerType: invoice` and add order tasks
beside them. Rejected: two record pages, and the early steps would have no
invoice to link to.

**Default and override:** the route `/orders/{correlation.ownerRef}/{correlation.activityKey}?task={task.id}`
is the `hmntsk.route` metadata of every type; a host changes it per type.

### The demo declares its own model

Task types, groups, people and the supplier registry live in `purchasing.go`;
orders, invoices and documents in `store.go`. The new steps (budget holder,
purchasing issuing work) do not exist in the shared domain, and extending it
would change every other example's context.

### Purchase order issuing is the application's own form and endpoint

Two task types, `purchase.send-order` and `purchase.upload-order`, share the
`purchase-order` activity and name different `hmntsk.formKey` values. The order
page renders the application's own form for them and a schema form for the rest.
The endpoints check the task belongs to the order and is of the right type,
then store the document and call `Complete` as the signed-in user in one
transaction, dispatching after commit. The engine still decides whether that
user may complete the task and whether the version is current; its errors map to
the task contract's statuses (404, 403, 409, 400).

*Alternative:* one task type with the registration in its input, completed
through the generic task API with a document uploaded separately. Rejected: the
document and the completion could disagree, and the form choice would depend on
input rather than on `hmntsk.formKey`, which is the convention being shown.

**Which way an order is issued** is decided when the order is placed, from the
registry, and stored on the order, so the page and the workflow never disagree.

### Uploads are checked by content

The server sniffs the file's content and accepts only PDF, PNG and JPEG up to
5 MB; the browser's claimed type and name are ignored except for the displayed
file name, stripped of directories. Downloads are served as attachments with
`nosniff` and a sandboxing CSP, so an uploaded file never renders in the page's
origin. The page repeats the checks only to save a round trip.

### Idempotent workflow steps

The `order-workflow` sink moves the order with a conditional update (only from
the status the step expects), then creates the next task with an ID derived from
the order (`<activity>-<orderID>`), treating a conflict as done. The order moves
before the task is created, so a retried delivery never leaves a task whose
completion would find the order in the wrong status.

### The invoice arrives from a polling loop, not a timer

Completing a purchase order records `invoice_expected_at` on the order. A loop,
run beside the relay every second, receives every order whose time has passed:
in one transaction it moves the order to review (conditionally), saves the
invoice and creates the review with a derived ID. State lives in the database,
so a restart loses no invoice and two passes receive it once.

**Default and override:** the delay is random between 5 and 20 seconds;
`config.InvoiceDelay` replaces it, which the tests use for a fixed delay on a
fixed clock.

### Record page orders tasks by creation time itself

`OrderCreated` sorts by task ID, which is creation order only for engine-minted
IDs. The workflow's derived IDs are not, so the record sorts tasks by
`CreatedAt` before answering.

### Data grids

- The inbox uses server pagination: the grid pages by number, the task API by
  cursor, so the page keeps the cursor that starts each reached page and forgets
  later ones on every re-read. The bucket count is the row count. Sorting,
  filtering and the column menu are off, because the server orders by urgency.
- The orders grid loads every order and sorts, filters and searches in the
  browser.
- Grid defaults (compact density, no selection on click, auto height) are theme
  defaults, per MUI's theming guidance.

## Risks / Trade-offs

- [The bundle grows by about 458 KB] → accepted for an example; the build's
  chunk-size warning is left visible rather than silenced.
- [A single-person pool reserves the task at creation, so carol's and erin's
  tasks start claimed] → documented in the README; the tests read the task before
  claiming.
- [The generic task API can complete a purchase order task with a fake document]
  → stated as a limit in the README; a host that must prevent it checks
  completions it did not make.
- [Seeded orders past a step show that step as skipped] → the stepper labels it
  "not needed".
