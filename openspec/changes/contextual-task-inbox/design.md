## Context

See proposal.md for the motivation. Current state, from reading the code:

- **Query.** `hmntsk.Query` filters by assignee, candidate, statuses, types, owner type, owner ref, activity key and due date. It has `Limit`, `Cursor` and `Descending`. `Service.Query` resolves the candidate's groups through the `GroupResolver`, then calls `store.Query(ctx, ResolvedQuery)`.
- **Ordering.** Every store orders by task ID alone. The cursor is the last task ID, compared with `>` or `<` (`store/sqlcore/query.go:36-107`; `memstore/memstore.go:354-385`). Task IDs are time-ordered, so ID order is creation order. `Page.NextCursor` is documented as opaque.
- **Stores.** `store/sql`, `store/pgx` and `store/gorm` all build SQL through `store/sqlcore`.
- **Indexes.** The DDL indexes are `tasks(assignee,id)`, `(status,id)`, `(task_type,id)`, `(owner_type,owner_ref,activity_key)` and `(due_at,status)`, plus `task_candidates(kind,value,task_id)`. Nothing indexes priority.
- **Task types.** `task_types` holds name, title, description, both schemas and the defaults. Its SQL lives in `sqlcore/types.go` (`UpsertType`, `SelectType(s)`, `ScanTypes`).
- **Priority.** Priority is an `int` where lower is more urgent: 0 is the most urgent and 10 the least, and 5 is the default.
- **Transport.** `transport/core.New(service, opts ...Option)` has only `WithBasePath`. `Request.Actor` is set by the host's middleware. `queryTasks` never reads it.
- **Conformance.** `storetest.repositoryQuery` covers the filters and one page-cap case. It checks neither order nor exact paging.

The project's design rule (`.claude/rules/library-design.md`) applies to every decision below. Each states **Default** and **Override**.

## Goals / Non-Goals

**Goals:**

- A host can build every usual inbox bucket, correctly ordered and counted, from engine queries alone.
- Exact paging and identical results on every store for every ordering offered.
- Safe by default over HTTP, fully replaceable by the host.
- Metadata that lets a UI link a task to its business form, without the engine learning what a form or a route is.

**Non-Goals:**

- **Arbitrary sort fields or sorting by payload values.** They break exact paging and index use (D1).
- **Full-text search, and filtering on correlation `extra` or on metadata.** Unindexed; out of scope as before.
- **Authorization of in-process `Service` calls.** A Go host calling `Service.Query` directly holds the actor itself and decides for itself. Authorization is an HTTP-layer concern, where the actor is an input (D6).
- **Push updates to inboxes.** Hosts use events through the relay. The `usage-examples` change shows it.
- **A multi-bucket HTTP endpoint.** Clients call the count endpoint per bucket, or the host composes `CountBuckets` behind its own route (D4).

## Decisions

### D1. `Query.OrderBy` selects from a closed set of orderings; `Descending` is the direction

```go
type Ordering string
const (
    OrderCreated  Ordering = ""         // zero value
    OrderPriority Ordering = "priority"
    OrderDue      Ordering = "due"
    OrderUrgency  Ordering = "urgency"  // priority, due (no deadline last), id
)
```

Every ordering ends with `id` as the final tiebreak, so it is total. `Descending` reverses the whole key, including the tiebreak. The no-deadline position is part of the ordering's meaning ("no deadline is least pressing"), so it stays **last** in both directions.

- **Default:** the zero value is creation order, so every existing caller is unchanged.
- **Override:** pick any of the four orderings and a direction.
- **Limit, stated:** no custom keys. Each ordering needs a keyset the stores can page exactly and an index that serves it on three dialects. An unknown value is a validation error (400 over HTTP), never a silent fallback to creation order.

_Alternative:_ an `OrderBy []SortField` with arbitrary columns. Rejected: several combinations would need indexes that do not exist, or would page inexactly, which breaks the existing "stable pages" requirement.

### D2. Keyset continuation over the ordering's full key, bound to the ordering

The cursor encodes the ordering, the direction and the last row's key values, base64url JSON such as `{"o":"urgency","d":false,"k":[1,false,"2026-09-14T00:00:00Z","0192..."]}`. The next page's predicate is the row-value comparison "strictly after this key".

- **Portability.** SQL builds the tuple comparison as the expanded OR-chain `(a > ?) OR (a = ? AND b > ?) OR ...`, because MySQL and SQLite do not reliably use row-value comparisons with mixed directions.
- **Nulls.** `due_at` enters the key as `(due_at IS NULL, due_at)`. The flag is `0` or `1`, so no-deadline rows sort after deadline rows on every dialect, independent of each dialect's own `NULLS FIRST`/`LAST` default. That default is exactly what differs today across PostgreSQL, MySQL and SQLite. Inside the key, a null `due_at` compares as equal to a null.
- **Mismatch.** A cursor whose ordering or direction differs from the query's is a validation error (D2's spec scenario).
- **memstore.** It implements the same comparison in Go, so memstore and SQL agree by construction. The shared store tests check that.

_Default:_ none needed; the cursor stays opaque. _Override:_ none, and deliberately so. The encoding is the library's to change, and the `Page.NextCursor` contract already says so.

### D3. Indexes serve the new orderings and the group filter

Added on all three dialects:

- `tasks_priority_idx (priority, id)`
- `tasks_due_order_idx (due_at, id)`
- `tasks_urgency_idx (priority, due_at, id)`

The existing `tasks_due_idx (due_at, status)` stays, because the escalation sweep uses it. The group filter reuses `task_candidates_lookup_idx (kind, value, task_id)` with `kind = group`.

- **Limit, stated in `docs/schema.md`:** the `due_at IS NULL` flag is an expression. The index narrows each page's range but does not avoid a sort for the flag on every dialect. Pages stay fast because they are bounded by `Limit` (at most 500). An unfiltered urgency query over millions of open tasks is the case to measure. The host can always narrow a query by status or type, which the existing indexes serve first.
- `VerifySchema` and the per-dialect DDL change together, with the `docs_test` that pins them.

### D4. `Count` is a store method; `CountBuckets` is service composition

- `Repository.Count(ctx, ResolvedQuery) (int64, error)` reuses the query builder's filter clauses under `COUNT(DISTINCT tasks.id)`. `DISTINCT` is needed because the candidate join can match a task through a user row and a group row at once. Order, limit and cursor are ignored.
- `Service.Count(ctx, Query)` resolves groups exactly as `Service.Query` does.
- `Service.CountBuckets(ctx, map[string]Query) (map[string]int64, error)` resolves each distinct candidate's groups **once**, then counts each bucket in turn. It runs inside the host's transaction when the context carries one, so the badges agree with each other.

Details:

- **Default:** a bucket limit of `MaxCountBuckets = 32`, which stops an unbounded badge row from issuing unbounded queries. Exceeding it is a validation error.
- **Override:** a host needing more calls `Count` itself, or composes its own. `CountBuckets` is convenience, not a gate.
- **HTTP:** `GET /tasks/count` takes the query endpoint's filters and returns `{"count": n}`.

_Alternative:_ one SQL statement per call using `SUM(CASE ...)` per bucket. Rejected: buckets may differ in candidate joins, so the statement would be the union of every bucket's joins, and wrong-by-duplication when they differ.

### D5. `Query.Group` filters on the pool as configured

`EXISTS (candidates WHERE kind='group' AND value=?)` means tasks whose pool names the group.

- Membership is not resolved.
- Exclusions are not evaluated. Exclusions are per-actor, and a team queue has no actor.
- It combines with `Candidate`, `Assignee` and the rest by AND.

**Default:** absent, meaning no group filter. **Override:** set it. **Who may use it** is D6's decision, not the engine's.

### D6. HTTP query authorization: a `QueryAuthorizer` port with a self-only default

```go
// in transport/core
type QueryAuthorizer interface {
    AuthorizeQuery(ctx context.Context, actor string, query hmntsk.Query) error
}
type QueryAuthorizerFunc func(ctx context.Context, actor string, query hmntsk.Query) error
func WithQueryAuthorizer(a QueryAuthorizer) Option
var SelfOnly QueryAuthorizer   // the default
var AllowAll QueryAuthorizer   // explicit opt-out, named so it is never accidental
```

Order of operations for `GET /tasks` and `GET /tasks/count`:

1. Parse the request.
2. Resolve `me`: an empty actor with `me` gives 403.
3. `AuthorizeQuery`: a non-nil error gives 403, carrying the error's message.
4. `Service.Query` or `Service.Count`.

- **Default, `SelfOnly`:**
  - permit when the query names a candidate or an assignee, and each named one equals the actor;
  - refuse `Group`;
  - refuse a query naming neither, since "every task" is not anyone's own inbox;
  - an empty actor refuses everything.
- **Override:** `WithQueryAuthorizer(...)` replaces the policy **wholesale**, without wrapping or chaining. A host wanting self-only plus supervisors writes one function that calls `SelfOnly` first. That is one line, and it keeps policy composition in the host's hands.
- **Limit, stated:** authorization covers queries and counts, the endpoints that list tasks. Reading one task by ID and lifecycle operations keep their existing eligibility rules. Extending authorization to `GET /tasks/{id}` is a separate decision, noted under Open Questions.

**BREAKING (unreleased):** a client querying another actor today stops working without an authorizer. There is no tag, so this is free (rule 7). It is recorded in the proposal and the README.

_Alternative:_ a permissive default with a documented `SelfOnly` option. Rejected under rule 1: the safe behaviour must be the one a minimal host gets.

### D7. `TypeSpec.Metadata`: opaque data, reserved `hmntsk.` keys, optional route helper

- `Metadata map[string]string` goes into `Clone`, into `Equal` (so it takes part in the registration conflict check) and into JSON (`metadata,omitempty`).
- It is stored as a nullable text column `metadata` holding canonical JSON with sorted keys, so `Equal` after a round trip is stable. `UpsertType`, `ScanTypes` and `VerifySchema` change accordingly.
- Well-known constants: `MetadataFormKey = "hmntsk.formKey"` and `MetadataRoute = "hmntsk.route"`.
- `func ExpandRoute(template string, task Task) string` replaces `{task.id}`, `{task.type}`, `{correlation.ownerType}`, `{correlation.ownerRef}`, `{correlation.activityKey}` and `{correlation.extra.<key>}`, and leaves anything else untouched. Values are inserted **raw**. URL-escaping is the host's, because a template might target a path, a query or a non-URL scheme, and the engine cannot know which.

Details:

- **Default:** no metadata; nothing changes for existing types.
- **Override:** any key outside `hmntsk.`. The helper is optional; hosts may expand routes any way they like.
- **Limit, stated:** metadata is not filterable and not copied onto tasks. A client fetches `/task-types` once and caches it.

_Alternative:_ typed fields `FormKey` and `RouteTemplate` on `TypeSpec`. Rejected: they would hard-code one UI model, where the principle calls for conventions over opinions baked into types.

### D8. HTTP parameters

`GET /tasks` and `GET /tasks/count` gain:

- `orderBy=created|priority|due|urgency` (count ignores it);
- `direction=asc|desc`, mapped onto `Descending`, where `asc` is the default;
- `group=`.

`candidate=me` and `assignee=me` are handled by D6. `openapi.json` and its test move with them. All bindings pick this up through `transport/core`, and `transporttest` asserts it on each.

## Risks / Trade-offs

- [The urgency ordering does a small per-page sort on some dialects because of the null flag] → Pages are bounded; documented in `docs/schema.md` with the narrowing advice (D3).
- [The self-only default breaks existing HTTP callers who read other actors' inboxes] → Unreleased; the proposal, README and `WithQueryAuthorizer` godoc say how to restore the old behaviour (`AllowAll`) or write a policy.
- [Counts over large candidate joins] → `COUNT(DISTINCT)` uses the candidate lookup index. `MaxCountBuckets` bounds fan-out, and hosts can cache badge counts.
- [Cursor format changes, invalidating in-flight cursors at deploy] → A stale cursor is a 400 validation error, and the client restarts from the first page. Acceptable for an unreleased library; documented.
- [Raw insertion in `ExpandRoute` allows injection if a host renders it unescaped] → The godoc says values are raw and the host escapes. Correlation values are the host's own data.
- [Five stores and seven driver/dialect combinations must agree on ordering] → One shared exact-paging suite in `storetest`, with ties and nulls, runs on all of them.

## Migration Plan

Nothing is tagged. The DDL gains three indexes and one column, and `VerifySchema` reports them if a host's migrations lag. Hosts apply the updated DDL through their own pipeline, as with the outbox change. HTTP clients reading other actors' inboxes add an authorizer.

## Open Questions

- Should `GET /tasks/{id}` also pass through an authorizer, so a user cannot read an arbitrary task by ID? It is deferrable: it is a separate endpoint with its own eligibility semantics, and adding it later is additive (a second method on the port, or a sibling port) without changing this change's specs.
