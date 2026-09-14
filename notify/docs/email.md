# Email

Email is optional. It reaches people who are not looking at the application: an
`EmailDispatcher` reads ACTIVE notifications from the store and sends each
recipient one message about the ones they have not read. The in-app inbox stays
the source of truth; email is the fallback.

A host that never emails needs none of this: no table, no schema check, no port.

## Wiring

The dispatcher needs three things only the host has: who a recipient's address is,
how a message is rendered, and how it is sent.

```go
dispatcher, err := notify.NewEmailDispatcher(svc, mailer, addressBook, template)
go dispatcher.Run(ctx, time.Minute) // or dispatcher.Dispatch(ctx) from a scheduler
```

| Port | Says |
| --- | --- |
| `AddressBook` | a recipient's address; `ok=false` when there is none, which is not an error |
| `EmailTemplate` | a message's subject and bodies, from one recipient's `EmailBatch` |
| `Mailer` | sends an `EmailMessage`; classifies a failure with `ErrMailRejected` or `ErrMailInDoubt` |
| `EmailFilter` | optional: which notifications are emailed |

Each has a `Func` adapter. A nil port is a configuration error, and so is a store
that does not record email deliveries: `NewMemoryStore()` and `notify/sqlstore`
do.

**The SQL store needs one more table.** Apply `Store.EmailSchema()` (the documents
under `notify/sqlstore/ddl/email/`) through your migration pipeline, and call
`Store.VerifyEmailSchema(ctx)` at startup alongside `Store.VerifySchema(ctx)`,
which does not require it.

Nothing emails on its own. The host runs a pass once with `Dispatch`, in a loop
with `Run`, or from its own scheduler; constructing a dispatcher starts nothing.

## Defaults and overrides

| Concern | Default | Override |
| --- | --- | --- |
| Grace delay before a notification is emailed | 5 minutes | `WithEmailGraceDelay`, or `WithoutEmailGraceDelay` |
| Oldest notification emailed | 24 hours old | `WithEmailMaxLag` |
| Notifications per message | 20 | `WithEmailBatchLimit` |
| Notifications per pass | 500 | `WithEmailClaimLimit` |
| Lease on a pass's claim | 5 minutes | `WithEmailLease` |
| Attempts before a delivery fails | 5 | `WithEmailMaxAttempts` |
| Delay between attempts | 1 minute, doubling, at most 1 hour, 20% jitter | `WithEmailBackoff` |
| Which notifications | every kind | `WithEmailKinds`, or `WithEmailFilter` |
| Delivery guarantee | `AtMostOnce` | `WithDeliveryGuarantee(notify.AtLeastOnce)` |
| Owner recorded in leases | `email-` and a UUIDv7 | `WithEmailOwner` |
| Failures in a pass | ignored, silently | `WithEmailErrorHandler` — supply one that logs |

- **The grace delay** keeps a notification someone reads in the application within
  a few minutes from ever being emailed.
- **The maximum lag** keeps enabling email, or resuming after an outage, from
  sending a backlog of old work. The in-app inbox still has everything.
- **Every kind is emailed by default**, because wiring a mailer, an address book and
  a template is already the opt-in. A noisy publisher should be narrowed:
  `WithEmailKinds` with the kinds that ask someone to act.
- A zero or negative duration or limit, a lag no longer than the grace delay, a
  backoff ceiling below its base, a filter and kinds together, or an unknown
  guarantee is a configuration error from `NewEmailDispatcher`.

## One pass

1. Claim up to the claim limit of notifications due: ACTIVE, past the grace delay,
   within the lag, and not already sent, skipped or failed. A claim is a lease; two
   dispatchers never hold the same notification.
2. Settle sends a previous pass left in doubt (below).
3. For each recipient: apply the filter, look the address up once, and send **one
   message** covering at most the batch limit of their notifications, oldest
   first. The rest are released at once, so the next pass, on any instance, sends
   them.
4. Just before rendering, read each notification again and drop any read, closed or
   deleted since it was claimed.
5. Record `SENDING` with the message's idempotency key, committed, then send, then
   record the outcome.
6. Remove delivery records whose notification retention deleted.

`Dispatch` returns an error only when claiming fails. Every other failure is
counted in the `DispatchResult` and reported to the error handler, and never stops
the pass.

| Outcome | Recorded as | Tried again |
| --- | --- | --- |
| sent | `SENT` | never |
| filtered out | `SKIPPED` (`filtered`) | never |
| no address | `SKIPPED` (`no_address`), with no error reported | never |
| read, closed or deleted before sending | `SKIPPED` (`inactive` or `deleted`) | never |
| `ErrMailRejected` | `FAILED` | never |
| a template error | `FAILED`: a rendering bug does not fix itself | never |
| any other send, lookup or filter error | `RETRY`, after the backoff | until the attempt limit, then `FAILED` |
| `ErrMailInDoubt`, or a pass that stopped mid-send | depends on the guarantee | see below |

**A skip is final.** A host that fixes an address or widens its filter later does
not email past notifications.

## Delivery guarantees

Every message carries an `IdempotencyKey` that is the same for every attempt of
that message.

- **`AtMostOnce`** (the default): a send in doubt — the mailer returned
  `ErrMailInDoubt`, or a pass stopped after recording `SENDING` and before recording
  the outcome — is recorded `ABANDONED` and never repeated. An email can be lost;
  it is never duplicated. The notification is still in the inbox.
- **`AtLeastOnce`**: a send in doubt is repeated once its lease lapses, with the
  same idempotency key, over exactly the same notifications, never merged with
  newer ones. With a sender that deduplicates on the key, the email arrives once;
  with one that does not, it can arrive twice.

## Preferences, quiet hours and unsubscribes

They belong to the host, and plug in through `EmailFilter`: a notification the
filter rejects is skipped for good, and an error from the filter is retried. A
filter that answers from the host's preference store might look like this:

```go
filter := notify.EmailFilterFunc(func(ctx context.Context, n notify.Notification) (bool, error) {
    return preferences.WantsEmail(ctx, n.Recipient, n.Kind)
})
dispatcher, err := notify.NewEmailDispatcher(svc, mailer, addressBook, template, notify.WithEmailFilter(filter))
```

## Stated limits

- **Under `AtMostOnce`, a pass that stops mid-send loses that message's email.**
  The window is one send; the notifications stay in the inbox and are counted as
  abandoned by the next pass.
- **Under `AtLeastOnce`, a sender that ignores the idempotency key can deliver a
  message twice.**
- **A notification read or closed between the recheck and the send is still
  emailed.** The window is one render and one send.
- **A skip is final**, and a notification older than the maximum lag is never
  emailed.
- **A send left in doubt for a notification that retention deleted is never
  settled**; its record is removed with the other orphans.
- **Email writes do not join the caller's transaction**, like every notification
  write.
