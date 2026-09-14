# quickstart

One invoice approval, from creation to completion, with nothing configured
beyond what hmntsk requires. Every other example starts from this wiring and
changes one thing.

## What it shows

- **The two required pieces:** a store (here the in-memory `memstore`) and a
  directory (`hmntsk.WithGroupResolver`) that says who is in which group.
- **Registration:** task types are registered before use, carrying their
  schemas and defaults.
- **Correlation:** a task is created about one invoice through
  `CorrelationData`.
- **The type's defaults:** its candidate pool, resolved to the people who may
  claim the task.
- **The happy path:** claim, then start, then complete. The output is
  validated against the type's output schema.

## Context

These examples share a made-up invoice-approval business.

- **Tasks:** an `invoice.approve` task is offered to the `finance-approvers`
  group. `billing-service` creates tasks.
- **People:**
  - alice and bob are approvers;
  - carol is a finance manager;
  - dave is an auditor.

## What it leaves out

- **Other stores:** a real host passes `store/sql`, `store/pgx` or `store/gorm`
  over its own database; see `correlated-tasks` and `store-drivers`.
- **HTTP, events, escalation and notifications:** each has its own example.
- **Every non-happy path:** release, delegate, fail, cancel and conflicts are in
  `lifecycle-operations`.

## Run it

From the `examples/` directory:

```sh
go run ./quickstart
go test ./quickstart
```

No database, broker or Docker is needed.

## Read next

- [Wiring](../../README.md#wiring) and
  [the transaction contract](../../README.md#the-transaction-contract)
  in the root README
- [`lifecycle-operations`](../lifecycle-operations) for every other operation
