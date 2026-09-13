## Why

The notifier needs what `store/sqlcore` already solved for tasks: SQL that behaves identically on PostgreSQL, MySQL and SQLite, published migrations, schema verification, and the same behaviour across `database/sql`, `pgx` and GORM. That groundwork is locked inside `sqlcore`, which imports the hmntsk core. The notifier is designed to become its own repository eventually, so it cannot depend on the task engine. The parts of `sqlcore` that know nothing about tasks move into a small toolkit both can use, before anything is tagged, while moving it is still free.

## What Changes

- **New module `sqlkit`** (`github.com/kartaladev/hmntsk/sqlkit`), extracted from `store/sqlcore` with no behaviour change:
  - the `Dialect` interface and its three dialects;
  - the migration runner (`Execer`, `Querier`);
  - schema and index verification primitives;
  - `Rows` and `Statement`;
  - UTC microsecond time encoding.
- `sqlkit` imports nothing from hmntsk. It gets its own configuration-error sentinel and its own time normalisation. `sqlcore`'s `SchemaError` keeps matching `hmntsk.ErrConfiguration`.
- **New `Executor` port** in `sqlkit`: execute a statement, query rows, run a function in a transaction, and report whether a transaction is active. Transactions follow the engine's existing rules: whoever begins commits, nested scopes join and flatten, and a panic rolls back and is re-raised.
- **Three executor modules:** `sqlkit/stdsql` (over `*sql.DB`), `sqlkit/pgx` (over `*pgxpool.Pool`) and `sqlkit/gorm` (over `*gorm.DB`, keeping GORM's `?` placeholders and the nested-transaction override). A store written once against `Executor` runs natively on all three.
- **`store/sqlcore` imports its dialect layer from `sqlkit`.** Moving `store/sql`, `store/pgx` and `store/gorm` onto the executors is *not* in scope.
- **Guardrails for the eventual split:**
  - `depguard` rules in `.golangci.yml` and a `make split-check` target fail the build if `sqlkit/**` imports hmntsk or `notify`, or if `notify/**` imports hmntsk.
  - The `Makefile` groups modules into `SQLKIT_MODULES`, `NOTIFY_MODULES` and `HMNTSK_MODULES`, each with its own lint, test and integration targets.
  - `sqlkit` docs live under `sqlkit/docs/`.

## Capabilities

### New Capabilities

- `sql-toolkit`: dialect-portable SQL support that knows nothing about any domain. It covers dialect capabilities, published migrations, schema verification, and an executor contract with identical transaction semantics on `database/sql`, `pgx` and GORM, asserted by one shared suite.

### Modified Capabilities

None. `task-persistence` requirements are unchanged; the task stores behave exactly as before.

## Impact

- **Code:** `store/sqlcore` shrinks by roughly 600 lines that move to `sqlkit`. `store/sql`, `store/pgx` and `store/gorm` change imports only.
- **Modules and tooling:** four new modules in `go.work` and in the release order (`sqlkit` before `store/sqlcore`). `.golangci.yml`, `Makefile` and `docs/releasing.md` change.
- **Tests:** the executor conformance suite runs on 7 combinations (stdsql × 3 dialects, pgx × PostgreSQL, gorm × 3 dialects) with testcontainers. The existing `storetest` suite must stay green.
- **Future:** `sqlkit` moves to its own repository before its first tag, so consumers never see a path change.
- **Unlocks:** `notify-core`.
