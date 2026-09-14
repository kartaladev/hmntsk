## Context

See proposal.md for why, and `specs/task-notifications/spec.md` for the required behaviour. The primitives this design builds on are final in other changes:

- **`event-audience-snapshot`.** Every event carries:
  - `Candidates`, the pool after the transition, exclusions included, groups unexpanded;
  - `PreviousAssignee`, set only when a holder was replaced (release, delegation);
  - `CreatedBy`;
  - existing `Actor`, `Assignee`, `Status`, `Version`, `TaskType` and `Correlation`.
- **`notify-core`.** `notify.Service` exposes:
  - `Publish(ctx, drafts ...Draft)`: idempotent on (SourceID, Recipient); a draft is suppressed when its `SubjectVersion` is below the subject's close watermark for its kind or for every kind;
  - `Close(ctx, CloseRequest{Subject, Kinds, Version, Reason, Except, Successor, SuccessorSkip})`: closes versions ≤ Version, optionally publishes a successor to each recipient it closed in the same transaction, and returns `CloseResult{Closed, Recipients, Successors, SuccessorsSuppressed}`;
  - `Draft.Coalesce`: a coalescing draft creates nothing when the recipient already has a non-CLOSED notification of that kind on that subject (`PublishResult.Coalesced`).

  Each call is its own transaction, serialised per subject. notify-core has no `GroupResolver`: publishers expand groups.
- **`task-read-authorization`.** `GET /tasks/{id}` defaults to `ParticipantsOnly` (holder, creator, eligible candidate), so every recipient of a task link can follow it while they stay eligible. That change adds `Service.Eligible`.
- **The relay** (`relay/sink.go`, `relay/relay.go`):
  - attempts events oldest first, and records acceptance per sink;
  - a retryable failure reschedules only the sinks that failed;
  - a `Permanent` outcome from any sink dead-letters the event, attempts count per event;
  - so retries reorder events relative to newer ones.
- **The engine:**
  - keeps the `GroupResolver` private (no accessor);
  - `hmntsk.ResolveCandidates(ctx, resolver, pool)` returns sorted, de-duplicated members with exclusions removed;
  - `ExpandRoute(template, Task)` reads only `task.id`, `task.type` and `correlation.*`;
  - `TypeSpec.Metadata[MetadataRoute]` holds a type's route;
  - `Status.IsTerminal()` covers COMPLETED, FAILED, ERROR, EXITED and OBSOLETE.

## Goals / Non-Goals

**Goals:**
- Every rule in the spec expressed as an ordered list of `Close` and `Publish` calls, each of them idempotent, so that any prefix of the list followed by a full redelivery converges on the same state.
- Correct results under redelivery, retry and reordering, relying only on the event's version and notify's watermark.
- A projection a host wires in a few lines, with every default replaceable.

**Non-Goals:**
- Notifications for events the approved rules do not cover:
  - the creator on completion;
  - the holder on escalation that only announces;
  - suspension, resumption and start.

  A host adds these through `Rules` (decision 7).
- Email, realtime transport and retention, which belong to `notify` and its adapters.
- Re-offering existing tasks to actors who join a group later (stated limit, decision 5).
- Localised titles. The default titles are English; a host overrides them (decision 9).

## Decisions

### 1. Module and wiring

- **Module** `github.com/kartaladev/hmntsk/tasknotify`, package `tasknotify`, at the top level. It sits in `HMNTSK_MODULES`, and imports `hmntsk`, `hmntsk/relay` and `notify`.
  - `split-check` needs nothing new, because the `HMNTSK_MODULES` group is unchecked.
  - `split-check` does require that nothing in `notify/**` or `sqlkit/**` imports `tasknotify`.
  - It is released after `notify` moves to its own repository (notify-core decision 12). Its `notify` import path changes once, in that step.
- **Constructor:**

  ```go
  func New(engine *hmntsk.Service, notifier *notify.Service, opts ...Option) (*Projector, error)
  ```

  - **Configuration errors:** a nil engine or notifier, and every invalid option (decisions 6, 8, 9, 10).
  - `*Projector` implements `relay.Sink`. A host adds it with `relay.WithSinks(projector)`.
- **Group expansion.** The engine's resolver stays private, as `task-read-authorization` decided. This change adds one engine primitive next to `Service.Eligible`:

  ```go
  // ResolveCandidates expands a pool into the actors eligible for it, using the
  // service's own group resolver, exactly as creation does.
  func (s *Service) ResolveCandidates(ctx context.Context, pool CandidatePool) ([]string, error)
  ```

  - **Why:** a projector given its own resolver could disagree with the engine that decided eligibility. It would also add a wiring mistake (passing a different directory) that no constructor can detect.
  - **Alternative rejected:** `WithGroupResolver` on the projector. That means two sources of membership, and the mismatch is undetectable.
  - **Default:** the engine's resolver. **Override:** none. A host changes membership through the engine's `WithGroupResolver`. A pool with groups and no resolver configured is a `*GroupResolutionError`, classified as unfixable (decision 10) and reported.

### 2. Kinds, reasons and relations are documented constants

```go
const (
    KindOffer    = "offer"
    KindTaken    = "taken"
    KindAssigned = "assigned"

    ReasonTaken      = "taken"      // an offer closed by a claim
    ReasonReleased   = "released"   // a taken/assigned notification closed by a release
    ReasonReassigned = "reassigned" // an assigned notification closed by a delegation
    // closing statuses use the lower-cased status: "completed", "failed", "errored", "cancelled", "obsoleted"

    RelationTask    = "task"
    RelationContext = "context"

    DefaultSinkName = "tasknotify"
)
```

- **Subject and version.** Subject = `event.TaskID`, SubjectVersion = `event.Version`, SourceID = `event.ID`.
- **Recipients.** A rule set produces at most one draft per recipient per event, so (SourceID, Recipient) stays unique.
- **Override:** the constants are conventions a client matches on. A host may use other kinds by supplying its own `Rules` (decision 7).

### 3. Per-event call sequences (the default rules)

**Notation:**
- `V` is the event's version, `T` its task ID, `A` its actor.
- `eligible` is `engine.ResolveCandidates(event.Candidates)`, minus `A` (spec: the actor is never notified). It is resolved lazily, only for events that need it.

| Event | Condition | Ordered calls |
|---|---|---|
| `task.created` | `Status == READY` | 1. `Publish(offer → each of eligible)` |
| `task.created` | `Status == RESERVED` | 1. `Publish(assigned → Assignee)`, unless `Assignee == A` |
| `task.claimed` | | 1. `Close{T, [offer], V, ReasonTaken, Successor: taken, SuccessorSkip: [A]}` (amendment A1) |
| `task.released` | | 1. `Close{T, [taken, assigned], V, ReasonReleased}` 2. `Publish(offer → each of eligible)` |
| `task.delegated` | | 1. `Close{T, [assigned], V, ReasonReassigned, Except: Assignee}` 2. `Publish(assigned → Assignee)` |
| `task.escalated` | `Status == READY` and the pool grew (see below) | 1. `Publish(offer → each of eligible, Coalesce)` (amendment A2) |
| a closing status | status ∈ closing set (decision 6) | 1. `Close{T, [] (every kind), V, lower(status)}` |
| anything else | | none |

**Why each sequence is correct, and why it survives a retry:**

- **Claim.**
  - One call. It closes every offer ≤ V, the claimant's included, and in the same transaction publishes `taken` at V to each recipient it closed, except the claimant.
  - Without A1 the two-call version (close, then publish to `CloseResult.Recipients`) loses the `taken` notices when the process fails between the calls: on redelivery the close finds nothing left to close and returns no recipients.
  - **Offers opened later.** A later release publishes offers at V' > V, which a late redelivery of this claim does not close (≤ V).
  - **Taken notices after a release.** The `taken` successors are drafts at V. A release projected before this claim moves the `taken` watermark to V' > V, so the successors are suppressed. This is the spec scenario "A late claim after a release".
- **Release.**
  - The close runs first, so a release projected before its claim closes nothing yet. The claim, arriving later, closes only the older offers and has its `taken` successors suppressed.
  - Offers publish at V, which is at or above the `offer` watermark set by the claim (< V), so they are created.
  - The releaser is excluded as the actor.
  - A retry re-closes idempotently, and the offers are duplicates on (SourceID, Recipient).
- **Delegation.**
  - The close spares the new holder (`Except`), and the publish creates the new holder's `assigned` at V. notify's "close ≤ V, suppress < watermark" rule lets the two share a version, in either order.
  - The previous holder is the actor, so they receive nothing new.
  - `PreviousAssignee` is not needed to find them: every other `assigned` on the task is theirs, or stale.
- **Escalation widening.**
  - Offers only make sense while the task is claimable, hence `Status == READY`.
  - "The pool grew" means the `OpEscalate` event's policy widened. Every `task.escalated` event that did not supersede carries the pool, so the rule does not need to know the policy: coalescing (A2) makes a publish to an unchanged pool create nothing.
  - **Alternative rejected:** diffing against the previous pool. The snapshot has no `PreviousCandidates` (event-audience-snapshot decision 4), and reading earlier events would re-couple the projector to the outbox.
  - **Alternative rejected:** listing existing offers per recipient. `ListQuery` needs a recipient, so that is N queries for an N-member group.
- **Closing status.** Closing every kind at V moves the `*` watermark to V, so any older redelivery of any rule (offers, `taken`, `assigned`) is suppressed afterwards.

### 4. Amendments this change needs from `notify-core` (now included there)

Both amendments are now part of notify-core's design (decision 13), its `notification-inbox` spec and its tasks (3.5, 3.6). They are recorded here as this change's reason for them:

- **A1. Close with successors, atomically.**
  - `CloseRequest` gains `Successor *Successor` and `SuccessorSkip []string`, with `type Successor struct { SourceID, Kind, Title string; SubjectVersion int64; Links map[string]string; Data json.RawMessage }`.
  - In the same transaction as the close, the store publishes one notification from the successor for each recipient this call closed, except those in `SuccessorSkip`.
  - Successors are subject to the same idempotency and watermark suppression as `Publish`.
  - `CloseResult` gains `Successors []Notification`, so signals are broadcast for them.
  - *Why generic:* "tell everyone whose X was closed that Y happened" is not task-specific, and only the store can do it atomically.
  - **notifytest cases:** successors are created, successors are skipped, successors are suppressed under a newer watermark, and a retried close creates no successors.
- **A2. Coalescing drafts.**
  - `Draft` gains `Coalesce bool`. A coalescing draft creates nothing when its recipient already has a non-CLOSED notification of the same kind on the same subject.
  - `PublishResult` and `InsertResult` gain `Coalesced int`.
  - **notifytest cases:** an active notification coalesces, a read one coalesces, a closed one does not, and coalescing is decided inside the subject's serialised transaction.
- **If A1 is declined,** the fallback is to compute `taken` recipients from the snapshot (`eligible`, minus the claimant), publishing them first and closing second. That is idempotent, but it sends `taken` to eligible actors who never held an open offer (group joiners, the previous releaser). It is documented as the degraded mode, not the default.

### 5. Group expansion happens at projection time (stated limit)

- `eligible` is computed when the event is projected, not when it was recorded. A redelivery hours later can include or omit actors whose membership changed in between.
- That matches the engine's documented rule that membership is resolved at the moment of use.
- An actor who joins a group after the offers were written receives no offer, but the task still appears in their `candidate=me` inbox. This is documented in `docs/notifications.md`.
- **Fan-out.** `eligible` can be large. Drafts are published in batches of `WithPublishBatch(n)` (default `DefaultPublishBatch = 500`), each batch its own `Publish` call. Batches are safe to split because each is idempotent and version-ordered. A batch ≤ 0 is a configuration error.

### 6. Closing statuses

- **Option:** `WithClosingStatuses(statuses ...hmntsk.Status)`.
- **Default:** COMPLETED, FAILED, ERROR, EXITED and OBSOLETE.
- **Configuration errors, from `New`:**
  - the set omits COMPLETED or EXITED;
  - it contains a status for which `IsTerminal()` is false;
  - it is empty.
- **Duplicates** are ignored.

### 7. The `Rules` port

```go
type Rules interface {
    Plan(ctx context.Context, in Input) (Plan, error)
}
type RulesFunc func(ctx context.Context, in Input) (Plan, error)

type Input struct {
    Event hmntsk.Event
    // Eligible expands Event.Candidates through the engine, minus Event.Actor, memoised per Input.
    Eligible func(ctx context.Context) ([]string, error)
    // Draft builds a draft for recipient and kind with the projector's links, title and data.
    Draft func(ctx context.Context, recipient, kind string) (notify.Draft, error)
    // Closing reports whether Event.Status is in the configured closing set.
    Closing bool
}
type Plan struct{ Steps []Step }
type Step struct {
    Close   *notify.CloseRequest // exactly one of Close or Publish
    Publish []notify.Draft
}

var DefaultRules Rules // decision 3
```

- **Default:** `DefaultRules`. **Override:** `WithRules(r)`. A host extends the defaults by calling `DefaultRules.Plan` and appending steps. A nil `Rules` is a configuration error.
- **Contract, documented on the port:**
  - steps run in order;
  - every step must be idempotent under redelivery;
  - at most one draft per recipient per event.
- **Execution:** the projector executes steps in order, and batches `Publish` drafts per decision 5.
- **A plan that breaks the contract:**
  - two drafts for one recipient: the second is reported as a duplicate, which is harmless;
  - a draft naming another subject: rejected before execution as an unfixable error (decision 10).
- **Alternative rejected:** a callback per event type. It cannot express "close then publish" ordering, or cross-type rules, without more hooks.

### 8. Links

- **Task link.**
  - Default template `DefaultTaskLinkTemplate = "/v1/tasks/{task.id}"`, which matches `transportcore.DefaultBasePath`.
  - **Override:** `WithTaskLinkTemplate(template)` for a host UI URL, or `WithLinks(LinksFunc)` to build both links wholesale.
  - An empty template is a configuration error. Use `WithLinks` to omit the task link deliberately.
- **Contextual link.**
  - Built from `TypeSpec.Metadata[hmntsk.MetadataRoute]`, looked up in `engine.Registry()` by `event.TaskType`, and expanded with `hmntsk.ExpandRoute`.
  - Omitted when the type is unregistered on this instance or declares no route. That is not an error: an instance serving only some types can still project the rest.
- **Adapter.** `ExpandRoute` takes a `Task`. The projector passes `hmntsk.Task{ID: event.TaskID, Type: event.TaskType, Correlation: event.Correlation}`, which covers every placeholder `ExpandRoute` recognises (`task.id`, `task.type`, `correlation.*`). Any other placeholder stays as written, so a host can post-process it in `WithLinks`.
- **Escaping.** Values are inserted raw, and escaping is the host's, as `ExpandRoute` documents. The default task link contains only the task ID, which the default ID scheme keeps URL-safe.

### 9. Titles and data

- **Default titles (English):**
  - `offer`: "Task available: {taskType}"
  - `taken`: "Task taken by {actor}: {taskType}"
  - `assigned`: "Task assigned to you: {taskType}"
  - **Override:** `WithTitles(TitleFunc)`, where `TitleFunc func(ctx, TitleInput{Event, Kind, Recipient}) (string, error)`.
- **Default data** is JSON `{"taskId","taskType","eventType","status","actor","previousAssignee"?,"ownerType"?,"ownerRef"?,"activityKey"?}`.
  - It carries no input, output, correlation `extra` or pool, so notifications never copy consumer payloads.
  - **Override:** `WithData(DataFunc)`.
- A nil func for either is a configuration error.

### 10. The projector as a relay sink, and failure classification

- **Name:** `Name()` returns `DefaultSinkName`. **Override:** `WithSinkName(name)`; an empty name is a configuration error. The name must stay stable, because the relay records acceptance by sink name.
- **`Deliver(ctx, attempt)`** builds `Input` from `attempt.Event`, plans, executes, and classifies:
  - **Delivered:** every step succeeded, including steps that created nothing because of duplicates, suppression or coalescing.
  - **Retryable:** a context deadline or cancellation, `notify.ErrUnavailable`, a store or driver error, or a `*hmntsk.GroupResolutionError` whose cause is not a `*hmntsk.ConfigurationError`.
  - **Delivered and reported:** a deterministic failure no retry can change. That means `notify.ErrValidation` (for example, a title over the limit from a host `TitleFunc`), a plan that breaks the contract, or a resolver that is missing. The error goes to `WithErrorHandler(func(ctx, error))`, which by default does nothing and is documented as silent.
  - **Never Permanent.** A `Permanent` outcome dead-letters the whole event and stops delivery to every other sink (webhook, Redis, NATS). Retrying a deterministic failure would spend the event's shared attempt budget and dead-letter it anyway.
- **Alternative rejected: return Retryable for everything.** A misconfigured title function would dead-letter every event for every sink after `DefaultMaxAttempts`.
- **Crash and reorder safety** come from decision 3's idempotent sequences plus notify's watermark. No projector-side state exists.

### 11. Placement of each concern

| Concern | Where |
|---|---|
| Which notifications an event opens and closes | `tasknotify` rules |
| Group expansion | engine (`Service.ResolveCandidates`) |
| Idempotency, watermarks, atomic successors, coalescing | `notify` store |
| Durability and retry | relay |
| Read authorization for links | `transport/core` (`ParticipantsOnly`) |

## Risks / Trade-offs

- [A1 and A2 widen notify-core's store contract] → Both are now part of notify-core (its decision 13, tasks 3.5 and 3.6), conformance-tested on every store. Task 1.1 checks they are implemented before this change is applied. A1's fallback stays documented (decision 4) in case it is reversed in review.
- [Large group fan-out makes one event slow to project] → Publishing is batched (decision 5). A slow projection holds only this sink's retry, because the relay records acceptance per sink.
- [Membership changes between recording and projection] → Documented limit (decision 5), consistent with the engine's resolve-at-use rule.
- [Deterministic failures are swallowed as Delivered] → Reported to `WithErrorHandler`. The docs recommend logging it. The alternative dead-letters unrelated sinks.
- [Events recorded before `event-audience-snapshot` carry no pool] → Their creation, release and widening produce no offers. `tasknotify` is released after that change, and the docs say a backlog from older versions yields no offers.
- [Title text is English by default] → `WithTitles`. Clients can also render from `kind` and `data`, and ignore `title`.
- [A host's UI URL needs escaping of correlation values] → Stated, as for `ExpandRoute`. `WithLinks` gives full control.

## Migration Plan

- Additive: a new module, one new engine method, and no schema change in hmntsk. The notification tables come from `notify/sqlstore`.
- **Enabling:**
  1. Apply notify's DDL.
  2. Build `notify.Service`.
  3. Build `tasknotify.New(engine, notifier)`.
  4. Add the projector to the relay's sinks.
- **Disabling:** remove the sink. Its accepted events stay accepted. Re-adding it later projects only events not yet accepted by that sink name.
- **Renaming the sink** re-projects every retained event under the new name. Idempotency makes that harmless, but it costs load. Documented.
