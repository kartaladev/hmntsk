## Context

See proposal.md for why. The state this design works from, verified on main at f7ca513:

- `hmntsk.Event` (`event.go:119`) carries `Assignee` (after the transition) but no pool, previous holder or creator.
- Every event is built in exactly one place, `Task.record()` (`transitions.go:324`). It receives the task before the transition (`t`), clones it into `next`, applies the operation's `mutate` to `next`, and only then builds the event. The pool after an escalation widening is therefore already in `next.Candidates` when the event is built.
- `Task.CreatedBy` is set once at creation from the request's actor (`create.go:62`) and never changes.
- Terminal transitions clear only the lease fields (`record()`); cancel, obsolete, fail and fault leave `Assignee` as it was.
- **Stores.** SQL stores record the event as JSON (`store/sqlcore/scan.go:420`, `json.Marshal(event)`). `memstore` keeps the `Event` value itself (`memstore/memstore.go:194`).
- **Sinks.** Three sinks render the event differently:
  - `delivery/redis` writes `json.Marshal(event)` into its `event` field (`redis.go:343`), beside flat routing fields.
  - `delivery/nats` publishes `json.Marshal(event)` as the body (`message.go:29`), beside routing headers.
  - `delivery/webhook` does **not** marshal the whole event. It copies a fixed set of fields into `webhook.PayloadEvent` (`payload.go:66`, mapped at `payload.go:150`), so new `Event` fields do not reach webhook bodies unless mapped.

## Goals / Non-Goals

**Goals:**
- One snapshot, computed once, in the pure transition, identical on every store and every sink.
- Additive to every published body: no field renamed, removed or retyped.

**Non-Goals:**
- Deciding who to notify. That is `tasknotify`'s job.
- Expanding groups into members. The snapshot names groups exactly as the pool does. Membership is resolved when it is used, as `GroupResolver` documents.
- New routing headers or flat fields on the Redis and NATS sinks.
- A `PreviousCandidates` field (decision 4).

## Decisions

### 1. Field names, types and JSON

`Event` gains three fields, placed after `Assignee`:

| Field | Type | JSON | When empty |
|---|---|---|---|
| `Candidates` | `CandidatePool` | `candidates,omitzero` | omitted only when the pool has no users, groups or exclusions |
| `PreviousAssignee` | `string` | `previousAssignee,omitempty` | omitted |
| `CreatedBy` | `string` | `createdBy,omitempty` | omitted (a task created with no actor) |

- Reusing `CandidatePool` keeps the body's shape identical to the task's `candidates`, so a consumer that already parses tasks parses this unchanged. The names match `Task`'s own JSON names.
- Alternative considered: a nested `audience` object. It was rejected because it duplicates `Assignee`, which already sits at the top level, and gives two places to read the holder from.
- **Override:** none. The event body is the engine's published contract, and a consumer that does not want the fields ignores them. The library-design rule allows a decision without an override when the reason is stated: an optional snapshot would make every consumer that relies on it (notably `tasknotify`) fail silently on hosts that turned it off.

### 2. `PreviousAssignee` is set exactly when a holder was replaced

- **Rule:** `PreviousAssignee = t.Assignee` when `t.Assignee != ""` and `t.Assignee != next.Assignee`. Otherwise it is empty.
- **Consequences:**
  - release and delegation carry it;
  - a claim from the pool does not, because nobody held the task;
  - creation, start, completion, suspension, resumption, escalation, cancellation, obsolescence and fault do not, because none of them changes `Assignee`.
- Alternative considered: always copy `t.Assignee`. It was rejected because it repeats `Assignee` on most events and makes "the holder changed" something a consumer has to compute.
- Alternative considered: set it on any change, including from empty. It was rejected because the result is identical, since empty is omitted, and the rule would be harder to state.
- The rule lives in `record()`, not in each operation, so a future operation that changes the holder gets it for free.

### 3. `Candidates` is the whole pool after the transition, exclusions included

- It is a clone of `next.Candidates`, so the event never aliases the task's slices.
- Exclusions are included because an audience decision that ignores them would offer work to someone who can never do it.
- **Normalisation:** empty slices are stored as nil. `memstore` keeps the value while SQL stores round-trip through JSON with `omitempty`, and a non-nil empty slice would otherwise compare unequal after a SQL round trip. The store conformance suite pins this.
- **Override:** none, for the reason given in decision 1.

### 4. No `PreviousCandidates`

- Pools only change by escalation widening, which only adds (`appendMissing`, `transitions.go:265`).
- A consumer that needs "who was added" either keeps its own record of whom it already told (as `tasknotify` will), or compares against the previous event on the same task, which `Version` orders.
- A second full pool on every widening event would double the size of the largest events for a question most consumers never ask.
- **Stated limit:** a consumer holding only one event cannot tell which candidates a widening added. Adding the field later is additive if a real consumer needs it.

### 5. `CreatedBy` comes from the task, not from the actor of the transition

- It is `next.CreatedBy`, which is immutable after creation, so every event on a task carries the same value.
- A consumer can tell the owner that their task finished without reading the task.

### 6. One construction point

- All three fields are filled in `record()` after `mutate` runs, so the order the widening already relies on stays correct.
- Operations that bypass `record()` do not exist today, and `TestEventCatalogueCoversEveryTransition` already proves every operation produces its event through it.

### 7. Sink bodies

- **Redis `event` field and NATS body:** they marshal the whole event, so they gain the fields with no code change. Their body tests are extended to assert the fields arrive.
- **Webhook:** `PayloadEvent` gains `Candidates`, `PreviousAssignee` and `CreatedBy` with the same JSON names, mapped from the event. Without this, the webhook would be the one destination missing the snapshot, and the spec's "every destination" would be false.
- **Redis flat fields and NATS headers are not extended.** They are routing hints. A pool is unbounded, and a header cannot carry one safely. The body stays authoritative, as `docs/delivery.md` already says.
- **Override:** a host that must not disclose pools to a particular webhook receiver has no switch in this change. It supplies its own `relay.Sink`, or filters at the receiver. The webhook body already discloses `actor` and `assignee`, so the disclosure class is not new. A per-sink field filter is deferred rather than guessed.

## Risks / Trade-offs

- **[Event size grows with the pool]**
  - The pool lists user identifiers and group names, never expanded members, so it stays proportional to what the host wrote at creation.
  - Outbox columns can hold it: PostgreSQL `text`, MySQL `LONGTEXT`, SQLite `TEXT`.
  - NATS refuses bodies above `max_payload`, which is 1 MB by default, and the sink already classifies that as permanent. A pool that large would already be pathological for eligibility checks.
  - Mitigation: document the growth in `docs/delivery.md`, alongside the existing `max_payload` row.
- **[Webhook receivers see more identifiers]**
  - Mitigation: documented in `docs/delivery.md`. The receiver is the host's own endpoint, authenticated by the signature. An opt-out is deferred (decision 7).
- **[memstore and SQL stores diverge on nil versus empty slices]**
  - Mitigation: normalise in `record()`, and add a conformance case that round-trips a snapshot through every store.
- **[Consumers that compare whole event bodies exactly]**
  - Any consumer asserting a fixed body breaks on new fields.
  - Mitigation: the fields are additive, nothing is tagged, and the in-repository tests that pin body contracts (Redis field set, NATS body, webhook payload) are updated in this change.

## Migration Plan

- No schema change: outbox rows store the event as opaque JSON.
- Events recorded before this change have no snapshot, and decode with empty fields.
- Hosts draining a pre-change backlog see those events without an audience. `tasknotify` is not released before this change, so no consumer depends on it yet.
- Rollback is a code revert. Rows written with the fields decode on the old code, which ignores unknown JSON fields.

## Resolved open questions

- **No webhook switch to omit the audience (`WithoutAudience()`).** Decided: none in this change. A host that must not disclose pools to a receiver filters at the receiver or supplies its own `relay.Sink`. The webhook body already carries `actor` and `assignee`, so the disclosure class is not new. A per-sink field filter can be added later without a breaking change (decision 7).
