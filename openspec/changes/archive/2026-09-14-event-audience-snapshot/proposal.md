## Why

An `Event` records who holds a task *after* a transition, but not the candidate pool, the previous holder, or who created the task. A consumer that decides who to tell about a change, such as the planned `tasknotify` projector, therefore has to re-read the task. Re-reading returns the task as it is *now*, not as it was when the event happened, so a delayed or retried delivery would notify the wrong people. It also cannot tell users "this task was taken" or "newly added groups can now claim it". The relay already rests on the principle that an event describes the transition as it happened. This change applies that principle to the audience.

## What Changes

- `hmntsk.Event` gains three fields, filled in by the transition that produced the event:
  - `Candidates`: the candidate pool after the transition.
  - `PreviousAssignee`: who held the task before the transition, set only when someone held it and the transition changed the holder (release, delegation). A claim from the pool has no previous holder.
  - `CreatedBy`: who created the task, copied from the task.
- Each event is stored whole in the outbox, so the new fields survive a crash and reach every sink. The webhook, Redis and NATS bodies gain the fields. The change is additive: no field is removed or renamed, and a consumer that ignores the new fields is unaffected.
- `PreviousCandidates` is deliberately left out. A notifier can work out who was offered a task from its own records, and a widened pool is visible from `Candidates` alone.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `task-events`: an event describes the task's audience at the moment of the transition (the pool after it, the previous holder when the holder changed, and the creator), so a consumer can decide who to notify without reading the task back.

## Impact

- **Code:** `event.go` (the new fields) and `transitions.go` (`record()` fills them from the task before and after). No store schema change: the outbox stores the event as opaque JSON.
- **Downstream:** `relaytest` fixtures and the body tests of `delivery/webhook`, `delivery/redis` and `delivery/nats` gain the fields. `docs/delivery.md` documents the body.
- **Compatibility:** additive to the public event body. Nothing is tagged yet, so there are no released consumers.
- **Unlocks:** `tasknotify`, which depends on this change.
- **Part of** the notification work, ordered: event-audience-snapshot, task-read-authorization, sqlkit, notify-core, tasknotify, notify-realtime-adapters, notify-email.
