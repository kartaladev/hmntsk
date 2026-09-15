## Why

The `contextual-ui` demo shows a single review-then-approve loop on an invoice
that appears the moment an order is placed. That hides the parts of a purchasing
application where contextual tasks matter most: an approval before anything is
bought, work whose form depends on the host's own data (the supplier registry),
and work that starts from something arriving later rather than from a click.
Its tables are also plain Material UI tables, which do not show how a host pages
a cursor-based task list in a real data grid.

## What Changes

- **BREAKING (demo only):** the order flow becomes approval → purchase order →
  invoice → review → approval. A placed order no longer creates an invoice and
  review; it waits for a budget holder's approval.
- After approval, purchasing issues the purchase order: sent by the application
  for a supplier on the supplier registry, or uploaded (PDF, PNG or JPEG, at most
  5 MB) for any other supplier. The application stores the document and
  completes the task in one transaction.
- After a random delay the supplier's invoice arrives with its review task; the
  existing review and approval then decide the order.
- **BREAKING (demo only):** the invoice page is replaced by an order page at
  `/orders/{ownerRef}/{activityKey}?task={id}`, the route every task type now
  links to. It shows the order, its purchase order documents, its invoice once it
  arrives, the workflow and every task on the order.
- The task panel chooses the form by `hmntsk.formKey`: the application's own form
  for purchase orders, a schema-rendered form otherwise.
- Every table in the page becomes an MUI X Data Grid. The inbox pages on the
  server by cursor and offers no client sorting or filtering; the orders grid
  sorts and filters in the browser.
- The demo declares its own purchasing model (task types, people, groups,
  supplier registry, records) instead of reusing `examples/internal/invoicing`.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `usage-examples`: the browser demo requirement changes from an order that
  starts an invoice review on an invoice page, to an order that is approved,
  issued as a sent or uploaded purchase order, and invoiced after a delay, worked
  on an order page with data grids.

## Impact

- **Code:** `examples/contextual-ui` only: Go server, workflow sink, records and
  tests; the React page in `web/` and its committed build in `dist/`.
- **Dependencies:** the page adds `@mui/x-data-grid` (MIT). The built bundle grows
  from about 591 KB to 1,049 KB (318 KB gzipped).
- **HTTP (demo API):** `GET /demo/invoices/{id}` is removed. Added:
  `GET /demo/suppliers`, `GET /demo/orders/{id}`,
  `POST /demo/orders/{id}/purchase-order/send`,
  `POST /demo/orders/{id}/purchase-order/upload`, `GET /demo/documents/{id}`.
- **Library:** none. `examples/internal/invoicing` stays for the other examples.
- **Docs:** `examples/contextual-ui/README.md` and the row in `examples/README.md`.
