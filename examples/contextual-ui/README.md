# contextual-ui

A browser demo of **contextual tasks**: hmntsk's tasks living inside the pages
of a small purchasing application, "Acme Purchasing", rather than in a separate
inbox. It is built with React, Material UI and MUI X Data Grid, served by a Go
program.

**It is an illustration, not a UI library.** Its sign-in page asks for no
password and sets a cookie the server trusts, which is not authentication.

## What it shows

- **Sign-in and the avatar:** a sign-in page lists the demo's people. An
  unsigned viewer is sent there, and afterwards returns to the page they opened.
  The signed-in user's avatar sits at the top right; its menu shows who they
  are and signs them out.
- **An order's whole life, as tasks:**
  1. erin, in purchasing, places an order. The server saves it with its
     approval task in one SQLite transaction and dispatches after commit, as
     `correlated-tasks` does. Anyone else is refused.
  2. carol, the budget holder, approves or declines it.
  3. Once approved, erin issues the purchase order. The supplier registry
     decides how:
     - a **registered** supplier is sent the purchase order the application
       drafts, to their contact on the registry;
     - anyone else needs a purchase order agreed outside the application, and
       erin **uploads** it: a PDF, PNG or JPEG of at most 5 MB.
  4. After a random 5 to 20 seconds the supplier's **invoice arrives**, with its
     review task.
  5. alice or bob reviews the invoice against the order. A matching review asks
     for the invoice's approval; one that doesn't disputes the order.
  6. The invoice's approval decides whether the order is approved or rejected.
- **The order page, where work is done:** every task links here through its
  type's `hmntsk.route`, `/orders/{ownerRef}/{activityKey}?task={id}`, expanded
  in the browser exactly as `hmntsk.ExpandRoute` does. The page shows:
  - the order, its purchase order documents and its invoice once it arrives;
  - the workflow, from the order to its outcome;
  - every task on the order, which the host reads through `Service.Query`
    filtered on correlation, as `record-page` does;
  - the viewer's task: claim, start and release, and the form its type's
    `hmntsk.formKey` names.
- **Two kinds of form:**
  - approvals and reviews render a form from the type's output schema, with
    progress saved as a JSON Patch and the server's validation errors shown;
  - issuing a purchase order is the application's own form. Its API stores the
    document and completes the task, as the signed-in user, in one
    transaction. The engine still decides whether that user may complete it.
- **A workflow after commit:** a relay sink, `order-workflow`, runs beside the
  notification projector. Every step is safe to repeat, because the relay
  delivers at least once:
  - every task it creates has an ID derived from its order, so a repeat is
    answered with a conflict, which counts as done;
  - an order moves only from the status the step expects, and moves before
    the next task is created.
- **Data grids:** every table is an MUI X Data Grid.
  - Orders are sorted and filtered in the browser, because the page reads
    them all.
  - The inbox pages on the server by cursor, one page at a time. It offers no
    sorting or filtering: the server orders every page by urgency, and sorting
    one page in the browser would mislead.
- **Inbox:** "Available to claim", "Mine" and "Overdue", each a query with its
  count, and the task list ordered by urgency.
- **Live notifications:** a badge fed by the server-sent event stream. Its
  links open the order page without a reload.
- **Default server behaviour:** self-only queries, participants-only reads, and
  the default notification rules.
- **Colour mode:** a light and dark toggle.

## Context

The demo declares its own purchasing domain in [`purchasing.go`](purchasing.go)
rather than the shared invoicing one.

- **People:**
  - erin is in `purchasing`: she places orders and issues purchase orders;
  - carol holds the budget (`budget-holders`) and approves orders. She also
    manages finance, so overdue invoice approvals widen to her;
  - alice and bob review and approve invoices (`finance-approvers`);
  - dave audits and takes part in no task.

  A pool of one person is reserved for that person as the task is created, so
  carol's and erin's tasks start claimed.
- **Supplier registry:** Acme Paper, Globex Cloud, Hooli Travel, Initech Chairs
  and Umbrella Catering. Any other supplier is unregistered.
- **Orders:** eight are seeded, ORD-101 to ORD-108, one or two at every stage:
  awaiting approval, awaiting a sent or an uploaded purchase order, awaiting an
  invoice (it arrives soon after start), invoice review, and invoice approval.
  alice has already claimed ORD-107's.
- **Storage:** tasks, orders, invoices, documents and notifications live in one
  SQLite database in a temporary directory. The relay runs every second,
  turning task events into notifications and workflow steps, and the suppliers
  check every second for invoices that are due.

## What it leaves out

- **Security:** authentication and CSRF protection, which belong to your app.
- **Real suppliers:** nothing is emailed. A sent purchase order is stored with
  the contact it would go to, and invoices are made up from the order.
- **Completions outside the form:** the task API would also accept completing a
  purchase order task with a made-up document ID. A host that must prevent it
  checks completions it did not make, for instance in its sink.
- **Notifying purchasing:** hmntsk notifies people about their tasks, not about
  a host's orders, so the orders and order pages re-read their records every
  few seconds.
- **Scale:** multi-instance realtime is in `realtime-scaling`, and WebSocket is
  there too.
- **A complete form renderer:** nested objects and arrays fall back to a JSON
  text field.

## Run it

From the `examples/` directory, with Go only. The built page is committed in
`dist/` and embedded:

```sh
go run ./contextual-ui                                   # then open http://127.0.0.1:8080
HMNTSK_DEMO_ADDR=127.0.0.1:9000 go run ./contextual-ui   # another address
go test ./contextual-ui   # session, orders, records, documents, the workflow, invoices, defaults
```

Try it:

1. Sign in as **erin**. On Orders, place an order with **Globex Cloud**, and
   another with a supplier not on the registry.
2. Sign in as **carol**. Open each order from the inbox or the notification,
   start the approval and complete it with `approved` on.
3. Sign in as **erin** again. On the first order, start the purchase order task
   and send it. On the second, upload a PDF.
4. Wait a few seconds on the order page: the invoice arrives.
5. Sign in as **alice** and complete the review with `matchesOrder` on, then as
   **bob** and approve or reject the invoice.
6. As erin, the orders show their outcome. A review with `matchesOrder` off
   disputes the order instead, and an approval with `approved` off declines it.

## Changing the page

The page's source is in [`web/`](web), written in React, TypeScript, Vite,
Material UI v9 and MUI X Data Grid v9. It follows MUI's official theming and
styling guidance:

- one theme with light and dark colour schemes and CSS variables, the data
  grid's defaults included;
- `sx` for one-off layout, and theme defaults for app-wide styles.

It has no router library. [`pages.ts`](web/src/pages.ts) matches the four pages
and `history.pushState` moves between them.

Changing it needs Node 22.12 or newer. From the repository root:

```sh
make ui-test     # page matching, workflow steps, cursor paging, uploads, route expansion, the form walker, bucket queries (Vitest)
make ui-build    # typecheck and rebuild examples/contextual-ui/dist; commit the result
```

For hot reload, run `go run ./contextual-ui`, then `npm run dev` in `web/`. The
development server proxies `/v1` and `/demo` to the Go server. CI fails if the
committed `dist/` doesn't match the source.

## Read next

- [Building an inbox](../../docs/inbox.md)
- [Notifying people about their tasks](../../docs/notifications.md)
- [Delivering events](../../docs/delivery.md)
- [Notifications: HTTP and realtime](../../notify/docs/notifications.md)
