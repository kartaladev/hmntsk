## Why

`notify` is deliberately generic, and hmntsk deliberately notifies nobody. Something has to turn task events into the right notifications:
- offers to the pool;
- "taken" to the other candidates when someone claims a task;
- assignments to the new holder;
- closing everything once a task is finished;
- all of it crash-safe, and correct when the relay retries or reorders events.

That translation is the only place that knows both worlds, and it stays with hmntsk when `notify` is split out.

## What Changes

- **New module `tasknotify`** (`github.com/kartaladev/hmntsk/tasknotify`), top-level and deliberately outside the `notify/` tree. It is the only module that imports both hmntsk and `notify`.
- **Projector as a relay sink:**
  - Durability comes from the outbox: it survives crashes and redeploys, and delivers at least once.
  - Publishing is idempotent on (event ID, recipient).
  - `Event.Version` feeds the subject watermark, so a retried older event never reopens notifications.
  - The sink never reports a permanent failure, so it cannot dead-letter an event for the other sinks.
- **Rules (defaults), keyed on the event audience snapshot:**

  | Event | Opens | Closes |
  |---|---|---|
  | created, pooled | offer to each eligible candidate (groups expanded when the notification is written) | none |
  | created, reserved | assigned to the assignee | none |
  | claimed | taken, to everyone else who was offered | every offer on the task (the claimant's closes silently) |
  | released | offers to the pool, except the actor who released it | earlier taken notices |
  | delegated | assigned to the new holder | the previous holder's assigned notification, silently (only the holder can delegate) |
  | escalated, widened | offers to newly eligible users only | none |
  | a closing status | none | every notification on the task |

- **Closing statuses:** `WithClosingStatuses` sets which final statuses close a task's notifications.
  - The default is all five final statuses: COMPLETED, FAILED, ERROR, EXITED and OBSOLETE.
  - Omitting COMPLETED or EXITED, or naming a status that isn't final, is a configuration error.
- **Links:** every notification carries a task-detail link and a contextual link built from the type's `hmntsk.route` with `ExpandRoute`, both under documented relation constants. Link templates are replaceable. Values are inserted raw, and escaping is the host's (a stated limit).
- **Constants and ports:**
  - Kind constants: `offer`, `taken`, `assigned`.
  - Relation constants: `task`, `context`.
  - A `Rules` port replaces the default rules.
  - Groups are resolved through the host's `GroupResolver`.
- **Stated limit:** a user who joins a group after an offer was written does not receive that offer. The task still appears in their inbox.

## Capabilities

### New Capabilities

- `task-notifications`: which notifications each task event opens and closes, the closing statuses, the task links, and the ordering and durability guarantees of the projection.

### Modified Capabilities

None. It consumes `task-events` (including the audience snapshot) and `event-relay` without changing them.

## Refinements from design

- The acting user is never notified of their own action.
- Releasing also closes the task's assigned notifications.
- Widening offers only while the task is in its pool, and only to actors with no open offer.
- By default, start, suspension, resumption and escalation without widening change nothing.
- The engine gains `Service.ResolveCandidates`, so group expansion uses the engine's own resolver.
- `notify-core` provides an atomic close-with-successors and coalescing drafts, added to it for this change (design decision 4; notify-core decision 13).

## Impact

- **Module:** new module `tasknotify` in `HMNTSK_MODULES`, released after `notify` and hmntsk core.
- **Depends on:** `event-audience-snapshot` and `notify-core`. Its links rely on `task-read-authorization` having landed.
- **Tests:** the rules as table tests; projection ordering under retried and reordered events; end-to-end through the relay on the SQL stores with testcontainers.
- **Docs:** a notifications guide alongside `docs/inbox.md`.
