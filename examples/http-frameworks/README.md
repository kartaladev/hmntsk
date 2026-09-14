# http-frameworks

The same hmntsk HTTP contract, and the same notification handlers, served by
[Gin](https://gin-gonic.com) and by [Fiber](https://gofiber.io), answering every
request identically.

## What it shows

- **The contract is written once.** `transportcore.New(svc)` defines the routes,
  request and response shapes, status codes and policies. `transport/gin` and
  `transport/fiber` only translate each framework's request into the contract's
  and back. Fiber's binding does this without converting to `net/http`.
- **Notification handlers mount on both.** `notify`'s handlers are standard
  library `http.Handler`s:
  - on Gin, wrap them with `gin.WrapH`;
  - on Fiber, wrap them with `github.com/gofiber/fiber/v3/middleware/adaptor`.
- **Identical answers.** The scenario sends the same requests to both
  frameworks, each over a freshly seeded engine, and compares the transcripts.
  The requests are:
  - your own inbox;
  - an unread count;
  - someone else's inbox (403);
  - a claim;
  - a read by someone taking no part in the task (403);
  - a team queue (403 by default);
  - an unknown route (404, in the contract's error shape).
- **Default, then override.**
  - *Default:* both frameworks with the contract's defaults. Queries are self-only,
    and single-task reads are for participants only.
  - *Override:* one query policy, `transportcore.WithQueryAuthorizer`, set once
    on the contract, lets carol read her team's queue. It takes effect on Gin and
    Fiber alike, with no per-framework code.

## Context

The shared, fictional invoice-approval domain from
[`internal/invoicing`](../internal/invoicing):

- `alice` and `bob` are in `finance-approvers`;
- `carol` is in `finance-managers`;
- `dave` is an auditor who takes part in no task.

One approval for invoice `INV-42` is created and offered to the approvers before
any request is made.

The acting user comes from an `X-Demo-User` request header, read by an actor
function you give each binding. That stands for your authentication
middleware: hmntsk authenticates nobody.

## What it leaves out

- The standard library binding (`transport/http`), which the other HTTP
  scenarios use.
- Server-sent event streams through Fiber's adaptor. They do stream (see
  [notify's mounting notes](../../notify/docs/notifications.md)), but a stream's
  output is timing-dependent. [`notifications`](../notifications) shows streaming.
- Framework features such as middleware stacks, CORS and TLS. They are yours,
  as with any other route.

## Run it

From the `examples/` directory. No database, broker or Docker is needed.

```sh
go run ./http-frameworks
go test ./http-frameworks
```

## Read next

- [HTTP in the root README](../../README.md#http): the contract, its defaults
  and its error mapping.
- [notify's HTTP handlers and mounting](../../notify/docs/notifications.md).
- [`inbox-buckets`](../inbox-buckets) and [`record-page`](../record-page): the
  query and read policies in detail.
