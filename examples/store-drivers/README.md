# store-drivers

The same invoice-task wiring on every database driver hmntsk supports, and the
host's own transaction in each driver's transaction type.

## What it shows

- **The same engine calls on every driver** (the default). Each driver runs the
  same steps: apply the schema, verify it, register the invoice types, create a
  correlated approval, then claim, start and complete it, and count the tasks for
  the invoice. Every driver prints the same result:
  - `store/sql` on PostgreSQL, through pgx's `database/sql` driver;
  - `store/pgx` on PostgreSQL, through a `pgxpool.Pool`;
  - `store/sql` on MySQL;
  - `store/gorm` on PostgreSQL and on MySQL.
- **The host's transaction, in each driver's own type** (the override). Each
  driver puts its transaction on the context with its own function:
  - `sqlstore.ContextWithTx` with a `*sql.Tx`;
  - `pgxstore.ContextWithTx` with a `pgx.Tx`;
  - `gormstore.ContextWithTx` with a `*gorm.DB`.
- **Commit and rollback.** The host writes its own invoice row and a task in
  one transaction.
  - A commit keeps both, and the task's event is only dispatched after the
    commit.
  - A rollback keeps neither.
- **Isolation.** Each combination uses its own table prefix, so they share one
  PostgreSQL and one MySQL database without colliding. Each run uses fresh
  prefixes, so a rerun against the same database starts clean.

## Context

The shared invoice-approval domain.

- **Tasks:** an `invoice.approve` task is offered to the `finance-approvers`
  group and created by `billing-service`.
- **People:**
  - alice and bob are approvers;
  - carol is a finance manager;
  - dave is an auditor.
- **Invoices:** stored in a small table of the host's own, next to the engine's
  tables.

## What it leaves out

- **SQLite:** covered by `correlated-tasks`.
- **Migrations:** applying the published statements through your own
  migrations, table prefixes as a feature, and schema-verification failures are
  in `schema-migrations`. Here `Migrate` is called for brevity. A host applies
  `sqlcore`'s statements through its own migration tool and calls
  `VerifySchema` at startup.
- **Everything above the store:** HTTP, events and notifications are the same
  on every driver, so they are in the other examples.

## Services needed

This example needs a real PostgreSQL 9.5 or later and MySQL 8.0 or later.
Start both with Docker:

```sh
docker run --rm -d --name hmntsk-postgres -p 5432:5432 \
  -e POSTGRES_USER=hmntsk -e POSTGRES_PASSWORD=hmntsk -e POSTGRES_DB=hmntsk \
  postgres:17.6-alpine

docker run --rm -d --name hmntsk-mysql -p 3306:3306 \
  -e MYSQL_ROOT_PASSWORD=hmntsk -e MYSQL_USER=hmntsk -e MYSQL_PASSWORD=hmntsk \
  -e MYSQL_DATABASE=hmntsk \
  mysql:8.4.6
```

Then tell the example where they are:

```sh
export HMNTSK_POSTGRES_DSN='postgres://hmntsk:hmntsk@127.0.0.1:5432/hmntsk?sslmode=disable'
export HMNTSK_MYSQL_DSN='hmntsk:hmntsk@tcp(127.0.0.1:3306)/hmntsk?parseTime=true&loc=UTC&clientFoundRows=true&multiStatements=true'
```

The MySQL DSN options are not optional:

- **`parseTime=true&loc=UTC`:** timestamps are read back as UTC times.
- **`clientFoundRows=true`:** a conditional update that matched its row counts
  as matched, not as a conflict.
- **`multiStatements=true`:** the schema can be applied in one go.

MySQL takes a little while to accept connections after its container starts.

## Run it

From the `examples/` directory:

```sh
go run ./store-drivers
```

Without the two variables, it exits and tells you which one is missing and how
to start the database.

```sh
go test ./store-drivers
```

The test starts its own PostgreSQL and MySQL containers with the repository's
testcontainers helpers, so it needs Docker but not the variables above. It takes
a minute or two the first time, while the images download.

Stop the databases afterwards:

```sh
docker stop hmntsk-postgres hmntsk-mysql
```

## Read next

- [Schema and migrations](../../docs/schema.md)
- [The transaction contract](../../README.md#the-transaction-contract)
