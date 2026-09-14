# escalation

What happens to a task nobody finishes in time. An overdue invoice approval is
escalated by the sweeper, first under the task type's own policy and then under
the ways a host can change that.

## What it shows

| Section | What you see |
| --- | --- |
| default | `invoice.approve`'s `DefaultEscalation` widens an overdue approval to `finance-managers`, so carol, who could not claim it before, can |
| override | a policy one task carries itself (widen to user carol), and a sweeper limited to one task type with `WithSweepTypes` |
| override | `Service.Escalate` called directly by an operator, on a task that is not even overdue: the same path as the sweep |
| override | `ExemptInProgress`: a task alice has started is claimed by the sweep but left alone, and counted as exempted |
| override | `MaxEscalations`: widening does not move the deadline, so once the sweeper's lease expires an uncapped task escalates again; a capped one does not |
| override | `EscalationSupersede`: the overdue task becomes `OBSOLETE`, with an obsolescence event instead of an escalation |

## Context

Every example uses the same made-up business: invoices that need approving.
`alice` and `bob` are in `finance-approvers`, `carol` is in `finance-managers`,
and `dave` is an auditor. An approval is offered to `finance-approvers` and is
due 24 hours after it is created.

The scenario runs on the in-memory store with a clock it advances itself, so the
deadlines and the output are the same on every run. The engine notifies nobody
when a task escalates: it records an event, and a consumer (an event handler,
the relay, `tasknotify`) tells people.

## Left out

- `Sweeper.Run`, the loop a host starts. The scenario calls `Sweep` once per step
  so that its output is deterministic.
- Several sweepers in several instances sharing the work through leases, and the
  lease duration option. See `WithLeaseDuration`.
- Telling anyone about the escalation: see [`notifications`](../notifications)
  and [`event-delivery`](../event-delivery).

## Run it

From the `examples` directory:

```sh
go run ./escalation
go test ./escalation
```

No database, broker or network is needed.

## Read more

- [Escalation](../../README.md#escalation) in the repository README
- `EscalationPolicy`, `Sweeper` and `Service.Escalate` in the package documentation
