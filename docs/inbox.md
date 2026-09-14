# Building an inbox

A task inbox is a handful of lists — my work, work I could pick up, my team's
queue, what is overdue — each with a badge, each in a useful order, each linking
to the form where the work is done. The engine gives you every one of those from
plain queries. It holds opinions about what is safe and what pages exactly, and
every opinion has an override.

This guide goes through them in the order a host usually meets them. Each part
says what you get with no configuration, and how to change it.

## Buckets are queries

There is no bucket type. A bucket is a `hmntsk.Query` you name, and the engine
answers it with the filters it already has:

```go
buckets := map[string]hmntsk.Query{
    "mine":      {Assignee: actor, Statuses: open},
    "available": {Candidate: actor, Statuses: []hmntsk.Status{hmntsk.StatusReady}},
    "team":      {Group: "finance-approvers", Statuses: open},
    "overdue":   {Candidate: actor, DueBefore: &now, Statuses: open},
    "invoices":  {Candidate: actor, OwnerType: "invoice"},
    "INV-42":    {OwnerType: "invoice", OwnerRef: "INV-42"},
}
```

`Candidate` is everything an actor may act on: work they hold, and pooled work
within their reach, with group membership resolved through your directory at the
moment of the query. `Assignee` is only what they hold. Correlation filters are
how a record's page lists the tasks about that record.

**Limit, stated:** a query filters on stored columns only. Payload fields,
correlation `extra` and type metadata are not filterable, because none of them
is indexed on every dialect. Anything you need to filter on belongs in one of
the three correlation fields.

## Ordering

**Default:** creation order, oldest first. A query that says nothing about order
behaves exactly as it always has.

**Override:** set `Query.OrderBy` to one of the supported orderings, and
`Query.Descending` to reverse it.

| Ordering | Wire value | Key |
| --- | --- | --- |
| `hmntsk.OrderCreated` | `created` | creation |
| `hmntsk.OrderPriority` | `priority` | priority, most urgent first, then creation |
| `hmntsk.OrderDue` | `due` | due date, earliest first, then creation |
| `hmntsk.OrderUrgency` | `urgency` | priority, then due date, then creation |

```go
page, err := svc.Query(ctx, hmntsk.Query{
    Candidate: actor,
    OrderBy:   hmntsk.OrderUrgency,
    Limit:     20,
})
```

Tasks without a deadline sort **last** under `due` and `urgency`, in both
directions. Having no deadline is the least pressing thing a due date can say,
and "descending" reverses the order of the deadlines, not that meaning. Every
store orders them identically: the three SQL dialects disagree about where
`NULL` sorts, so the engine never relies on any of them.

Paging is exact under every ordering. `Page.NextCursor` continues the query
after the last task of the page, compared on the ordering's whole key, so no
task is repeated or skipped however many tasks are created between pages. A
cursor is bound to the ordering and direction that produced it: continuing a
different one is a validation error, not a quiet restart.

**Limits, stated:**

- **No custom sort keys.** An ordering must be a key every store can page
  through exactly and every dialect can serve from an index. Sorting by a
  payload field or an arbitrary column can do neither, so it is not offered.
- **An unsupported ordering is refused**, with a validation error, never
  treated as creation order.
- Under `due` and `urgency` the no-deadline flag is an expression, so on some
  dialects the index narrows each page rather than serving it outright. Pages
  are bounded, so this stays fast; see [schema.md](schema.md) for the indexes
  and for how to narrow a very large unfiltered query.

## Counting

**Default:** a count applies every filter a query applies, eligibility included,
and ignores the ordering, the page size and the cursor. A task is counted once,
even when it is within an actor's reach both by name and through a group.

```go
available, err := svc.Count(ctx, buckets["available"])
```

For a row of badges, `CountBuckets` counts a set of named queries in one call and
returns one count per name. Each equals what `Count` would say for that bucket
alone. Each distinct candidate's groups are resolved once for the whole call,
and the counts run on the context you pass, so inside your transaction they read
the same state and agree with each other.

```go
counts, err := svc.CountBuckets(ctx, buckets)   // counts["mine"], counts["team"], ...
```

**Limit, stated:** `CountBuckets` takes at most `hmntsk.MaxCountBuckets`
buckets, which is 32. More is a validation error and nothing is counted, so that
an unbounded badge row cannot issue an unbounded number of queries.

**Override:** the limit is on the convenience, not on counting. A host that
needs more badges calls `Count` for each, or groups them into several
`CountBuckets` calls, and decides for itself how many queries a page may cost.

## Team queues

**Default:** no group filter.

**Override:** `Query.Group` lists the tasks whose candidate pool names that
group. It combines with every other filter.

```go
queue, err := svc.Query(ctx, hmntsk.Query{
    Group:    "finance-approvers",
    Statuses: []hmntsk.Status{hmntsk.StatusReady},
    OrderBy:  hmntsk.OrderUrgency,
})
```

It is the queue as configured, not as one member sees it. Membership is not
resolved, and exclusions are not applied, because a queue has no actor to apply
them to. Held work stays in the queue, so a supervisor can see who holds what.

Who may look at a team's queue is a question for your authorization, not for the
engine. Over HTTP that is the query policy described below.

## Linking a task to its form

**Default:** a task type carries no metadata.

**Override:** give `TypeSpec.Metadata` any string keys and values. The engine
stores them, returns them unchanged from the registry, the database and
`GET /task-types`, and never interprets them. They take part in registration
conflicts, so re-registering a type with different metadata is refused.

Two keys are conventions the library defines:

| Constant | Key | Meaning |
| --- | --- | --- |
| `hmntsk.MetadataFormKey` | `hmntsk.formKey` | A form the client knows how to render |
| `hmntsk.MetadataRoute` | `hmntsk.route` | A link to where a task's work is done, as a template |

```go
svc.Register(hmntsk.TypeSpec{
    Name: "invoice-approval",
    Metadata: map[string]string{
        hmntsk.MetadataFormKey: "invoice-approval-v2",
        hmntsk.MetadataRoute:   "/invoices/{correlation.ownerRef}/approve?task={task.id}",
        "acme.icon":            "receipt",
    },
})
```

Keys under `hmntsk.` are reserved for the library. Every other key is yours, and
a client that ignores the well-known keys loses nothing else.

`hmntsk.ExpandRoute(template, task)` expands a route template for one task. It
replaces `{task.id}`, `{task.type}`, `{correlation.ownerType}`,
`{correlation.ownerRef}`, `{correlation.activityKey}` and
`{correlation.extra.<key>}`, and leaves every other placeholder exactly as
written, so a template can carry placeholders of your own for a later pass.

**Limit, stated:** values are inserted **raw**. Whether a value needs path
escaping, query escaping or none at all depends on where the template points,
which only you know, so escaping is yours to do before the link is rendered or
followed. The helper is optional; expand templates any way you like.

**Limit, stated:** metadata is not copied onto tasks and cannot be filtered on.
A client fetches `/task-types` once and caches it.

## Over HTTP

`GET /tasks` takes the query as parameters, and `GET /tasks/count` takes the
same filters and answers `{"count": n}`.

| Parameter | Query field | Notes |
| --- | --- | --- |
| `candidate`, `assignee` | `Candidate`, `Assignee` | `me` names the acting user |
| `group` | `Group` | |
| `status`, `type` | `Statuses`, `Types` | repeatable |
| `ownerType`, `ownerRef`, `activityKey` | correlation | |
| `dueBefore` | `DueBefore` | RFC 3339 |
| `orderBy` | `OrderBy` | `created`, `priority`, `due` or `urgency`; queries only |
| `direction` | `Descending` | `asc` (the default) or `desc`; queries only |
| `limit`, `cursor` | `Limit`, `Cursor` | queries only |

An unsupported `orderBy` or `direction` is `400`.

### Who may query

**Default: `transportcore.SelfOnly`.** An actor may query and count only their
own inbox:

- a query must name a candidate or an assignee, and every one it names must be
  the acting user;
- a query by `group` is refused;
- a query naming neither is refused, because every task is nobody's own inbox;
- with no acting user established, everything is refused.

A refusal is `403`. Each request is handled in one order: the parameters are
parsed (a malformed request is `400` before anyone is asked whether it may run),
then `me` is resolved to the actor your middleware established (with none, a
query naming `me` is `403`), then the policy decides, and only then does the
engine run the query.

```
GET /v1/tasks?candidate=me&orderBy=urgency          200, your inbox
GET /v1/tasks/count?candidate=me&status=READY       200, {"count": 4}
GET /v1/tasks?candidate=bob                         403 unless you are bob
GET /v1/tasks?group=finance-approvers               403
```

**Override: `transportcore.WithQueryAuthorizer`.** Your policy replaces the
default wholesale and decides every query and count alone. Nothing is wrapped or
chained, so a policy that extends the default calls it itself.
`transportcore.QueryAuthorizerFunc` turns a function into a policy:

```go
supervisors := transportcore.QueryAuthorizerFunc(
    func(ctx context.Context, actor string, q hmntsk.Query) error {
        if q.Group != "" && directory.Supervises(actor, q.Group) {
            return nil
        }

        return transportcore.SelfOnly.AuthorizeQuery(ctx, actor, q)
    })

api, err := transportcore.New(svc, transportcore.WithQueryAuthorizer(supervisors))
```

The policy sees the query with `me` already resolved. Any error it returns
refuses the request with `403`, carrying the error's message, so write messages
that are safe to show the caller.

`transportcore.AllowAll` permits every query. It is for a host that authorizes
queries somewhere else, such as middleware in front of the contract, and it is
named so that serving every inbox to every caller is never an accident. Passing
a nil policy is a configuration error from `transportcore.New`.

**Limit, stated:** the policy covers the endpoints that list and count tasks.
Reading one task has a policy of its own, below. The lifecycle operations keep
the engine's own eligibility and assignee rules and pass through neither.

A Go host calling `Service.Query` directly holds the actor itself, and no policy
runs: authorization is where the actor arrives as an input, which is the HTTP
layer.

### Who may read a task

**Default: `transportcore.ParticipantsOnly`.** An actor may read a task, and its
history, when they:

- hold it;
- created it; or
- are eligible for it: a candidate user, or a member of a candidate group, and
  not excluded. This is the engine's own rule, the one a claim applies.

Anyone else is refused. The holder and the creator are checked first, so neither
costs a directory call.

`GET /v1/tasks/{id}` and `GET /v1/tasks/{id}/history` check things in one order:

1. no acting user established: `403`, before the task is looked up, so an
   anonymous caller cannot probe which identifiers exist;
2. no such task: `404`;
3. the policy refuses: `403`, with the policy's message and nothing of the task;
4. the directory could not say whether the actor is eligible: `500`, because the
   engine could not decide, which is not deciding against the caller.

```
GET /v1/tasks/01J...            200 if you hold it, created it or may claim it
GET /v1/tasks/01J.../history    403 if you are none of those
GET /v1/tasks/no-such-task      404 with an acting user, 403 without one
```

**Override: `transportcore.WithTaskReadAuthorizer`.** Your policy replaces the
default wholesale and decides every single-task and history read alone.
`transportcore.TaskReadAuthorizerFunc` turns a function into a policy. The policy
receives a `TaskRead` with the actor and the task, and `read.Eligible(ctx)`
resolves eligibility by the engine's rule only when you call it:

```go
auditors := transportcore.TaskReadAuthorizerFunc(
    func(ctx context.Context, read transportcore.TaskRead) error {
        if directory.IsAuditor(read.Actor) {
            return nil
        }

        return transportcore.ParticipantsOnly.AuthorizeRead(ctx, read)
    })

api, err := transportcore.New(svc, transportcore.WithTaskReadAuthorizer(auditors))
```

Any error your policy returns refuses the read with `403` and its message,
except an error matching `hmntsk.ErrGroupResolution`, which answers `500`.
`transportcore.AllowAll` permits every read, as it permits every query; each
policy is still replaced only through its own option. A nil policy is a
configuration error from `transportcore.New`. To test a policy of your own,
describe reads with `transportcore.NewTaskRead`.

**Limit, stated:** an acting user can tell a task that does not exist (`404`)
from one they may not read (`403`). The default identifiers are UUIDv7 and not
guessable in practice. If you supply guessable identifiers through
`CreateRequest.ID` and must conceal existence, enforce it in your middleware.

**Limit, stated:** the contract never serves a single-task read without an
acting user, whatever the policy would say.

### Telling people about their inbox

An inbox answers when it is asked. To tell people when a task arrives, is taken
from them or is assigned to them, see [Notifying people about their tasks](notifications.md).

### Unreleased breaking changes

Nothing is tagged yet, so these land free:

- **Queries are self-only by default.** A client that read another actor's inbox
  now gets `403`. Supply a policy, or `AllowAll` to restore the old behaviour.
- **Reading one task is participants-only by default.** A client that read a
  task it neither holds, created nor may claim, or read one with no acting user,
  now gets `403`. Supply a read policy, or pass `AllowAll` to
  `WithTaskReadAuthorizer` to restore the old behaviour.
- **`order` is now `direction`.** The direction parameter is `direction=asc|desc`,
  beside `orderBy`. The old `order` parameter is refused with `400`, naming its replacement, rather than kept as a
  second spelling.
- **Cursors changed encoding.** A cursor issued before the change is refused as a
  validation error, and the client starts again from the first page.
