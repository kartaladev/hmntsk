# lifecycle-operations

Every operation a task goes through, beyond the happy path in
[`quickstart`](../quickstart), including the ones hmntsk refuses.

## What it shows

**Default:** one approval offered to the `finance-approvers` group.

- **Claim and release.** Only the holder may release; anyone else is refused as
  not authorised.
- **Delegation.** The holder hands the task to another eligible actor, work
  included. Delegating to someone outside the pool is refused.
- **Illegal transitions.** A reserved task cannot be completed before it is
  started, and a suspended task cannot be claimed.
- **Suspend and resume.** The task leaves circulation and resumes to exactly the
  state it came from.
- **Conditional writes.** A write made with a version the caller read earlier is
  refused with a `*hmntsk.ConflictError` naming the current version.
- **The implicit start.** The first progress save on a reserved task starts it,
  and produces no event.
- **Fail and cancel.** Failing records that the work could not be done, with a
  reason; cancelling closes the task as `EXITED`, and cannot happen twice.

**Override:** a candidate pool given to one task with `CreateRequest.Candidates`.

- One eligible actor: the task is reserved for them at creation.
- Nobody eligible: the task goes to `ERROR` with a reason, instead of sitting
  unclaimable.
- `Excluded` wins over group membership.

Refusals are told apart with `errors.Is` and `errors.As` on the library's errors,
never by matching their messages.

## Context

The shared invoice-approval domain from [`internal/invoicing`](../internal/invoicing):
`alice` and `bob` are in `finance-approvers`, `carol` in `finance-managers`,
`dave` in `auditors`. The store is in memory.

## What it leaves out

- Doing these operations over HTTP: see [`schema-form`](../schema-form) and
  [`record-page`](../record-page).
- Who is told about each change: see [`notifications`](../notifications).

## Run it

From `examples/`:

```sh
go run ./lifecycle-operations
go test ./lifecycle-operations
```

No database, broker or network is needed.

## Read more

- [The transaction contract and conditional writes](../../README.md#the-transaction-contract)
- [Errors and their HTTP statuses](../../README.md#http)
