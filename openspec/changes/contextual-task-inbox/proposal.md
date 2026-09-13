## Why

A host building a task inbox gets the essentials from hmntsk: filter by assignee, eligibility, status, type and correlation, in stable pages. It still cannot build the inbox users expect. There are five gaps:

- **Order:** results come only in creation order, so "most urgent first" cannot be asked of the server.
- **Counts:** a page carries no count, so bucket badges ("Available (12)") need a full scan.
- **Team queues:** a supervisor cannot list a group's queue, because eligibility is always resolved for one actor.
- **Links:** a task type carries nothing a UI can use to link a task to its business form.
- **Authorization:** the HTTP query endpoint trusts whatever `candidate` and `assignee` the caller sends, so any authenticated user can read anyone's inbox.

These are the gaps between an engine and a usable contextual task list. Nothing is tagged yet, so the safer default for query authorization can land now without a compatibility cost.

## What Changes

Each item states the default behaviour and how the consumer overrides it.

- **Query ordering.** `Query.OrderBy` selects created (the default, unchanged), priority, due date, or urgency. Urgency means priority, then due date with no-deadline tasks last, then creation. Each ordering can run ascending or descending.
  - Paging stays exact under every ordering: no task is repeated or skipped across pages, and results are identical on PostgreSQL, MySQL and SQLite.
  - Orderings are limited to indexed keys. That limit is documented, not relaxed.
  - `Page.NextCursor` stays opaque. Its encoding changes, which no caller relies on.
  - **BREAKING (unreleased):** over HTTP the direction is `direction=asc|desc`, beside `orderBy`. It replaces the earlier `order=asc|desc` parameter, which is removed rather than kept as a second spelling.
- **Counts.** `Service.Count(ctx, Query)` returns how many tasks a query matches, with the same filters as `Query`. `Service.CountBuckets(ctx, map[string]Query)` returns every badge for a set of host-defined buckets in one call. HTTP adds `GET /tasks/count`.
- **Group buckets.** `Query.Group` selects tasks whose candidate pool names that group, for a supervisor's team queue. It combines with every other filter.
- **Task type metadata.** `TypeSpec.Metadata` is a `map[string]string` the engine stores and returns and never interprets. It is served by `/task-types`. Well-known keys are defined as documented constants:
  - `hmntsk.formKey`, for a client-side form identifier;
  - `hmntsk.route`, a route template over the task's fields and correlation.

  A consumer may use those keys or define its own.
- **Query authorization in the HTTP API.**
  - `candidate=me` and `assignee=me` resolve to the acting user.
  - The **default authorizer is self-only**: `candidate` and `assignee` must be the acting user, and `group`, or a query naming neither, is refused with `403`.
  - `transport/core.WithQueryAuthorizer` replaces that policy wholesale, for supervisors, admins or the host's own permission system.
  - **BREAKING (unreleased):** a caller querying another actor's inbox over HTTP without an authorizer now gets `403`.
- **Schema.** New indexes supporting the orderings and the group filter, and a `metadata` column on `task_types`, move in step across all three dialects, `VerifySchema` and `docs/schema.md`. **BREAKING (unreleased):** the DDL changes, free until the first tag.

## Capabilities

### New Capabilities

- `task-inbox`: engine-level inbox query semantics that no transport depends on. Covers the supported orderings and their exact paging, counting a query and a set of buckets, and filtering by a group's candidate pool.

### Modified Capabilities

- `task-http-api`: the inbox query endpoint accepts ordering and a group filter; a count endpoint is added; `me` resolves to the acting user; query authorization has a self-only default the host can replace; and task type responses include metadata.
- `task-types`: a task type carries opaque metadata beside its schemas and defaults, with documented well-known keys.

## Impact

- **Core (`hmntsk`):**
  - `Query` gains `OrderBy` and `Group`. The existing `Descending` becomes the direction for whichever ordering is chosen, so no new direction field is added.
  - `Service` gains `Count` and `CountBuckets`.
  - `TypeSpec` gains `Metadata`, plus the well-known key constants.
  - The `Repository` port gains a count method.
- **Stores:**
  - `memstore` gets ordering, counts, group filtering and metadata.
  - `store/sqlcore` gets the builder (composite keyset cursor, dialect-uniform null ordering, `COUNT`, group filter) and the DDL for all three dialects.
  - `VerifySchema` changes; `store/sql`, `store/pgx` and `store/gorm` pick these up through `sqlcore`.
- **Conformance:**
  - `storetest` gains ordering, exact-paging, counting and group cases, run against memstore and all seven driver × dialect combinations.
  - `transporttest` gains count, `me`, authorization and metadata cases, run against all three bindings.
- **Transport:** `transport/core` gets the count route, query parsing, `WithQueryAuthorizer` with a self-only default, and `openapi.json`.
- **Docs:** `docs/schema.md` (indexes, `metadata` column), README (HTTP and inbox sections), and a new inbox guide section covering buckets, ordering, counts, metadata keys and authorization.
- **Out of scope:** the `examples/` module is the follow-up `usage-examples` change. NATS delivery is a separate change in another session.
