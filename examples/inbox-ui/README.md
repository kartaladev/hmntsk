# inbox-ui

A browser demo of a contextual invoice inbox over hmntsk's HTTP contracts. It
is built with React and Material UI, served by a Go program.

**It is an illustration, not a UI library.** Its user switcher sets a cookie
the server trusts, which is not authentication.

## What it shows

- **Buckets:** "Available to claim", "Mine" and "Overdue", each a query with its
  count, and the task list ordered by urgency.
- **Links:** each task links to its invoice through the task type's
  `hmntsk.route`, expanded in the browser exactly as `hmntsk.ExpandRoute` does.
- **The task panel:**
  - claim, start and release;
  - a form rendered from the type's output schema, where progress is saved as a
    JSON Patch;
  - completion, with the server's validation errors shown.
- **Live notifications:** a badge fed by the server-sent event stream. Opening
  it lists unread notifications with links, and each can be marked read.
- **Default server behaviour:** self-only queries, participants-only reads, and
  the default notification rules.
- **Simulate a new invoice:** creates an approval task. Every approver's badge
  updates without a reload.
- **Colour mode:** a light and dark toggle.

## Context

The shared invoice-approval domain.

- **Invoices:** seven are seeded, INV-101 to INV-107, at different priorities
  and deadlines. alice has already claimed INV-106.
- **Storage:** tasks, invoices and notifications live in one SQLite database in
  a temporary directory. The relay turns task events into notifications every
  second.

## What it leaves out

- **Security:** authentication and CSRF protection, which belong to your app.
- **Scale:** multi-instance realtime is in `realtime-scaling`, and WebSocket is
  there too.
- **A complete form renderer:** nested objects and arrays fall back to a JSON
  text field.

## Run it

From the `examples/` directory, with Go only. The built page is committed in
`dist/` and embedded:

```sh
go run ./inbox-ui              # then open http://127.0.0.1:8080
HMNTSK_DEMO_ADDR=127.0.0.1:9000 go run ./inbox-ui   # another address
go test ./inbox-ui             # the server's routes, cookies and defaults
```

Try it:

1. Choose alice, then bob, and compare their buckets.
2. Open a task, claim it, start it, and complete it with a reason.
3. Press "Simulate a new invoice" and watch the badge.

## Changing the page

The page's source is in [`web/`](web), written in React, TypeScript, Vite and
Material UI v9. It follows MUI's official theming and styling guidance:

- one theme with light and dark colour schemes and CSS variables;
- `sx` for one-off layout, and theme defaults for app-wide styles.

Changing it needs Node 22.12 or newer. From the repository root:

```sh
make ui-test     # route expansion, the form walker, bucket queries (Vitest)
make ui-build    # typecheck and rebuild examples/inbox-ui/dist; commit the result
```

For hot reload, run `go run ./inbox-ui`, then `npm run dev` in `web/`. The
development server proxies `/v1` and `/demo` to the Go server. CI fails if the
committed `dist/` doesn't match the source.

## Read next

- [Building an inbox](../../docs/inbox.md)
- [Notifying people about their tasks](../../docs/notifications.md)
- [Notifications: HTTP and realtime](../../notify/docs/notifications.md)
