# event-delivery

How task events leave the engine and reach the code that acts on them: first an
in-process handler, then the durable relay delivering to a signed webhook, and
what the relay does when a destination is unreachable, refuses, or is one of
several.

## What it shows

| Section | Shows |
| --- | --- |
| default: an in-process event handler | `hmntsk.WithEventHandlers`: handlers called after the engine commits, in the same process |
| override: the relay and a signed webhook, default destination policy | `relay.NewRelay` + `webhook.New`: a callback on `127.0.0.1` refused by `webhook.DefaultPolicy` and dead-lettered at once |
| override: the same webhook, allowed to reach loopback | `webhook.AllowLoopback()`: every event delivered, signed, verified by the receiver with `webhook.NewVerifier`; reference parameters echoed; a tampered body rejected |
| override: a receiver that is down, retried with backoff | a 503 retried after `relay.WithBackoff`'s delay, not due before it, delivered after it; 429 retried, 410 dead-lettered |
| override: two sinks, each accepting on its own | a second `relay.Sink` failing once: acceptance recorded per sink, the retry skipping the sink that already took the event, and `relay.WithRelayErrorHandler` hearing about the failure |
| override: what an event says about its audience | the audience snapshot every event carries: `CreatedBy`, `Assignee`, `Candidates`, and `PreviousAssignee` after a release |

## Context

Every example uses the same made-up business, invoice approval. Approvals are
offered to the `finance-approvers` group (alice and bob). Overdue ones escalate
to `finance-managers` (carol). dave is an auditor and takes part in no task. A
billing service creates the tasks.

The relay and every sink start nothing on their own. The scenario calls
`Relay.Relay` for one pass at a time, and moves a fixed clock forward, so the
output is the same on every run. A host runs `relay.Run` on an interval and
keeps the default 20% jitter, which the retry sections switch off only to print
an exact schedule.

The webhook receivers run on a free loopback port inside the program; nothing
leaves your machine.

## What it leaves out

- Publishing to Redis streams, NATS subjects and JetStream: see
  [`event-bus`](../event-bus).
- Turning events into notifications for people: see
  [`notifications`](../notifications).

## Run it

From the `examples/` directory, with Go only:

```sh
go run ./event-delivery
go test ./event-delivery
```

## Read next

- [Delivering events](../../docs/delivery.md): the relay, the webhook contract,
  verifying deliveries, and the destination policy.
