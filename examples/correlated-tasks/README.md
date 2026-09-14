# correlated-tasks

An invoice and the task to review it are written to the same SQLite database.
First the engine commits its own transaction; then the host's own transaction
carries both writes, and commits them together or rolls both back.

## What it shows

- **Store setup:** `store/sql` on SQLite, with `Migrate` and `VerifySchema`.
- **Engine-led (the default):** the engine opens, commits and then dispatches
  events itself. The invoice is saved separately, so a crash between the two
  writes can leave an invoice with no task.
- **Host-led (the override):**
  - the host puts its `*sql.Tx` on the context with `sqlstore.ContextWithTx`;
  - the engine joins that transaction;
  - the result's event dispatch waits for the host's commit
    (`Result.Pending`, `Result.Dispatch`);
  - a rollback leaves neither the invoice nor its task, and no event is sent.
- **Correlation as a filter:** `Service.Count` counts every task for one invoice
  by `OwnerType` and `OwnerRef`.

## Context

The shared invoice-approval domain.

- **Tasks:** `invoice.review` tasks are created by `billing-service`.
- **Invoices:** stored by the host in its own `invoices` table, next to the
  engine's tables.

## What it leaves out

- **Other databases:** PostgreSQL, MySQL, pgx and GORM are in `store-drivers`.
- **Migrations:** applying the published statements yourself, table prefixes
  and nested transaction scopes are in `schema-migrations`.
- **Durable delivery:** the relay is in `event-delivery`.

## Run it

From the `examples/` directory:

```sh
go run ./correlated-tasks
go test ./correlated-tasks
```

It creates a SQLite file in a temporary directory and removes it afterwards. No
database server or Docker is needed.

## Read next

- [The transaction contract](../../README.md#the-transaction-contract)
- [Schema and migrations](../../docs/schema.md)
