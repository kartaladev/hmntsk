# contextual-ui

A browser demo of **contextual tasks**: hmntsk's tasks living inside the pages
of a small purchasing application, "Acme Purchasing", rather than in a separate
inbox. It is built with React and Material UI, served by a Go program.

**It is an illustration, not a UI library.** Its sign-in page asks for no
password and sets a cookie the server trusts, which is not authentication.

## What it shows

- **Sign-in and the avatar:** a sign-in page lists the demo's people. An
  unsigned viewer is sent there, and afterwards returns to the page they opened.
  The signed-in user's avatar sits at the top right; its menu shows who they
  are and signs them out.
- **Orders, where work starts:** erin, in purchasing, places an order. The
  server saves the order, its invoice and the invoice's review task in one
  SQLite transaction and dispatches after commit, as `correlated-tasks` does.
  Anyone else is refused.
- **The invoice page, where work is done:** every task links here through its
  type's `hmntsk.route`, `/invoices/{ownerRef}/{activityKey}?task={id}`,
  expanded in the browser exactly as `hmntsk.ExpandRoute` does. The page shows:
  - the invoice and its order;
  - the workflow: order, review, approval, outcome;
  - every task on the invoice, which the host reads through `Service.Query`
    filtered on correlation, as `record-page` does;
  - the viewer's task: claim, start and release, a form rendered from the
    type's output schema (progress saved as a JSON Patch), and completion with
    the server's validation errors shown.
- **A workflow after commit:** a relay sink, `invoice-workflow`, runs beside
  the notification projector.
  - A review that matches its order creates the approval task. One that
    doesn't disputes the order.
  - The approval decides whether the order is approved or rejected.
  - Every step is safe to repeat, because the relay delivers at least once:
    the approval is created with an ID derived from its invoice, so a repeat
    is answered with a conflict, which counts as done.
- **Inbox:** "Available to claim", "Mine" and "Overdue", each a query with its
  count, and the task list ordered by urgency.
- **Live notifications:** a badge fed by the server-sent event stream. Its
  links open the invoice page without a reload.
- **Default server behaviour:** self-only queries, participants-only reads, and
  the default notification rules.
- **Colour mode:** a light and dark toggle.

## Context

The shared invoice-approval domain, plus one person the demo adds.

- **People:**
  - alice and bob approve invoices (`finance-approvers`);
  - carol manages them;
  - dave audits;
  - erin is in `purchasing`. She is only in this demo's user list, not in the
    shared directory, so she takes part in no invoice task and nobody approves
    their own purchase.
- **Orders:** seven are seeded, ORD-101 to ORD-107, each billed by its invoice
  INV-101 to INV-107.
  - Six invoices wait at approval, at different priorities and deadlines.
    alice has already claimed INV-106.
  - INV-107 waits at review.
- **Storage:** tasks, orders, invoices and notifications live in one SQLite
  database in a temporary directory. The relay runs every second, turning task
  events into notifications and workflow steps.

## What it leaves out

- **Security:** authentication and CSRF protection, which belong to your app.
- **Notifying purchasing:** hmntsk notifies people about their tasks, not about
  a host's orders, so the orders and invoice pages re-read their records every
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
go test ./contextual-ui   # session, orders, invoice records, the workflow, defaults
```

Try it:

1. Sign in as **erin**. On Orders, place an order.
2. Sign out from the avatar menu and sign in as **alice**. Watch the badge, open
   the new review from the inbox or the notification, claim it, start it and
   complete it with `matchesOrder` on.
3. Within a second the invoice page shows the approval waiting. Sign in as
   **bob**, open it and approve or reject.
4. Sign in as erin again: the order shows the outcome. A review with
   `matchesOrder` off disputes the order instead.

## Changing the page

The page's source is in [`web/`](web), written in React, TypeScript, Vite and
Material UI v9. It follows MUI's official theming and styling guidance:

- one theme with light and dark colour schemes and CSS variables;
- `sx` for one-off layout, and theme defaults for app-wide styles.

It has no router library. [`pages.ts`](web/src/pages.ts) matches the four pages
and `history.pushState` moves between them.

Changing it needs Node 22.12 or newer. From the repository root:

```sh
make ui-test     # page matching, workflow steps, route expansion, the form walker, bucket queries (Vitest)
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
