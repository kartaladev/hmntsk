# Notifying people about their tasks

The engine notifies nobody. `tasknotify` turns the events it records into
per-user notifications: offers to the candidates of a pooled task, a notice to
the others when someone takes it, an assignment to whoever holds it, and a close
for every one of them once the task is finished.

The notifications themselves live in [`notify`](../notify/docs/notifications.md),
which knows nothing about tasks: its model, retention, realtime signals and HTTP
handlers are documented there. This guide covers only what `tasknotify` adds.

Each part says what you get with no configuration, and how to change it.
[`examples/notifications`](../examples/notifications) runs the wiring below
end to end: the defaults, then custom links and titles, a supervisor's stream,
retention and email.

## Wiring

A projector is a relay sink. It needs the engine, a notification service, and a
relay to drive it:

```go
notifier, _ := notify.New(notifyStore)              // notify/sqlstore, or notify.NewMemoryStore()
projector, _ := tasknotify.New(engine, notifier)     // a configuration error is returned, never deferred
relay, _ := relay.NewRelay(engine, relay.WithSinks(projector, webhookSink))

go relay.Run(ctx, time.Second)
```

1. Apply notify's schema beside the engine's (`notify/sqlstore` publishes it).
2. Build the notification service.
3. Build the projector.
4. Add it to the relay's sinks.

Because the projector rides the relay, every notification an event should
produce survives a crash, a redeploy and a failed attempt: the event stays in the
outbox until the projector accepts it.

**Default:** group membership is expanded through the engine's own directory
(`Service.ResolveCandidates`). **Override:** none on the projector; change
membership through the engine's `WithGroupResolver`, so the projector can never
disagree with the engine about who may act.

## What each event does

**Default** (`tasknotify.DefaultRules`):

| Event | Opens | Closes |
| --- | --- | --- |
| created, pooled | `offer` to every eligible actor | nothing |
| created, reserved | `assigned` to the holder | nothing |
| claimed | `taken` to everyone whose offer it closed, except the claimant | every `offer`, reason `taken` |
| released | `offer` to every eligible actor except the releaser | `taken` and `assigned`, reason `released` |
| delegated | `assigned` to the new holder | every other `assigned`, reason `reassigned` |
| escalated, pooled and widened | `offer` to eligible actors with no offer open | nothing |
| a closing status | nothing | every notification of the task, reason the status |
| started, suspended, resumed, anything else | nothing | nothing |

The actor of an event is never notified of their own action.

**Override:** `WithRules(rules)`. A host derives its rules from the defaults by
calling `DefaultRules.Plan` and appending steps:

```go
tasknotify.WithRules(tasknotify.RulesFunc(func(ctx context.Context, in tasknotify.Input) (tasknotify.Plan, error) {
    plan, err := tasknotify.DefaultRules.Plan(ctx, in)
    if err != nil || in.Event.Type != hmntsk.EventTypeCompleted {
        return plan, err
    }

    draft, err := in.Draft(ctx, in.Event.CreatedBy, "done")
    if err != nil {
        return tasknotify.Plan{}, err
    }

    plan.Steps = append(plan.Steps, tasknotify.Step{Publish: []notify.Draft{draft}})

    return plan, nil
}))
```

**The contract a plan keeps:** steps run in order; every step is idempotent
under redelivery; at most one draft per recipient per event; every close and
draft names the event's task. A plan naming another task is refused before
anything runs, and reported (see below).

### Constants

Clients match on these; they are conventions, and a host with its own rules may
use others.

| Constant | Value | Meaning |
| --- | --- | --- |
| `KindOffer` | `offer` | a pooled task is yours to claim |
| `KindTaken` | `taken` | someone claimed a task you were offered |
| `KindAssigned` | `assigned` | a task is reserved for you |
| `ReasonTaken` | `taken` | an offer closed by a claim |
| `ReasonReleased` | `released` | a taken or assigned notification closed by a release |
| `ReasonReassigned` | `reassigned` | an assigned notification closed by a delegation |
| `RelationTask` | `task` | link to the task's details |
| `RelationContext` | `context` | link to where the work is done |

A closing status records its own name as the reason: `completed`, `failed`,
`errored`, `cancelled` or `obsoleted`.

## Closing statuses

**Default:** every final status closes a task's notifications: completed,
failed, errored, cancelled and obsoleted.

**Override:** `WithClosingStatuses(statuses...)`.

**Limit, stated:** the set must include `StatusCompleted` and `StatusExited`
(cancelled), and may contain only final statuses. Anything else, or an empty
set, is a configuration error from `New`.

## Links

Every notification carries two links by relation name.

- **Task link.** **Default:** `/v1/tasks/{task.id}`, which is the REST contract
  at its default base path. **Override:** `WithTaskLinkTemplate` for your own
  web application.
- **Contextual link.** Expanded from the task type's `hmntsk.route` metadata
  with `hmntsk.ExpandRoute`. It is left out when the type is not registered on
  this instance or declares no route, which is not an error.

**Override both:** `WithLinks(func)`.

**Limit, stated:** values are inserted raw, never escaped, exactly as
`ExpandRoute` documents. Escaping for wherever the link points is yours.

A recipient following a task link reaches `GET /tasks/{id}`, which is
participants-only by default (see [Who may read a task](inbox.md#who-may-read-a-task)),
so they can read the task for as long as they remain a participant.

## Titles and data

**Default titles**, in English: "Task available: {type}", "Task taken by
{actor}: {type}", "Task assigned to you: {type}". **Override:** `WithTitles`.

**Default data:** the task and event identity, the status, the actor, the
previous holder when there was one, and the three correlation fields. It never
copies the input, the output, correlation `extra` or the candidate pool.
**Override:** `WithData`.

## Group membership is resolved when a notification is written

**Limit, stated:** candidate groups are expanded when an event is projected. An
actor who joins a group afterwards receives no offer for that earlier event. The
task still appears in their `candidate=me` inbox, because the inbox resolves
membership at query time.

A retried event is expanded when it is retried, so membership that changed in
between is the membership used.

## Failures

A projection is classified like any relay sink, with one opinion:

| Failure | Outcome |
| --- | --- |
| none, including drafts that were duplicates, suppressed or coalesced | delivered |
| unavailable store, driver error, passed deadline, failing directory | retryable |
| invalid content from a host func, a plan breaking the contract, a pool with groups and no directory | **delivered and reported** |

The projector **never** reports a permanent failure. A permanent outcome
dead-letters the whole event for every sink, webhook and broker included, and a
failure no retry can change would spend the event's shared attempt budget and
dead-letter it anyway.

**Default:** reported failures are discarded, which is silent. **Override:**
`WithErrorHandler(func(ctx, err))`; log from it.

## Renaming the sink

**Default:** the relay records acceptance under `tasknotify`. **Override:**
`WithSinkName(name)`.

Renaming it re-projects every event the relay still holds under the new name.
Idempotency makes that harmless, but not free.

## Events recorded before the audience snapshot

Events carry their candidate pool, previous holder and creator from the
`event-audience-snapshot` change on. An event recorded before it has no pool, so
its creation, release and widening project no offers. Claims, delegations and
closes still work, because they need no pool.
