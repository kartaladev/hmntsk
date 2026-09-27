## Why

The audit in section H confirmed four defects in notify's email delivery, each with a failing reproduction. A pass whose lease lapses mid-pass keeps sending: `RecordEmails` checks only the owner, the `SENDING` record uses the pass's start time and does not renew the lease, so a second dispatcher takes the send over. The result is an email the mailer accepted recorded `ABANDONED`, and a never-sent email abandoned and never retried (N4). Under `AtLeastOnce`, in-doubt resends ignore the attempt limit and the maximum lag, so they never stop (N5a). They also go out under the original idempotency key over a shrunken set of notifications, which breaks the documented promise of "exactly the same notifications" (N5b). Finally, a host ID generator that mints identifiers longer than 64 bytes is accepted, works on SQLite and PostgreSQL, and fails every publish on MySQL (N8). These are silent correctness failures in a library whose default is at-most-once email, and the library has no tag yet, so the contracts can still be tightened freely.

## What Changes

- **Lease fencing for sends.** Recording `SENDING` requires the owner's lease to still be live at the moment of recording (`lease_until > At`), and renews that lease for a full lease period. Every record carries a fresh clock reading, not the pass start time. A `SENDING` record is all-or-nothing: it changes every notification of the message or none. When it is refused, the dispatcher does not send, and it releases the rows it still holds to the next claim. Outcome records after a send (`SENT`, `RETRY`, `FAILED`, `ABANDONED`) still need only ownership, because ownership proves no other dispatcher took the rows over. An accepted send is therefore recorded `SENT` whenever no other dispatcher has taken it over.
- **`EmailRecord.Lease`** (new field): how long a `SENDING` record renews the lease. **BREAKING** for host `EmailStore` implementations, which must honour the new fence. There is no tag yet, so this is recorded rather than versioned.
- **Bounded in-doubt resends under `AtLeastOnce`.** Each resend counts against `WithEmailMaxAttempts`. A message whose attempts are used up is recorded `FAILED`. A message whose oldest notification is past `WithEmailMaxLag` is recorded `ABANDONED` and not resent.
- **Batch size is recorded.** The delivery table gains a nullable `batch_size` column. A `SENDING` record writes how many notifications the message covers, and a claim returns that size with each in-doubt candidate (`EmailRecord.BatchSize`, `EmailCandidate.BatchSize`). The dispatcher can then tell when a message's notification was deleted and purged, and so cannot be reproduced.
- **Exact resends.** An in-doubt resend covers exactly the original message's notifications, under the same key and re-rendered from them, even if some were read or closed meanwhile. If any was deleted, the message cannot be reproduced. The deleted ones are skipped, and the survivors that are still active go out as a new message under a new key. A claim takes every lapsed notification of an in-doubt message together, so a claim limit or a concurrent claimer never splits a message.
- **Identifier bound.** A new constant `notify.MaxIDBytes = 64` names the longest identifier the stores accept. `notify.New` refuses, with a `ConfigurationError`, a generator whose probe identifier is empty or longer. Every identifier minted later (notification IDs, successor IDs, email batch keys) is checked before anything is written, identically on every store.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `notification-email`: at-least-once resends are bounded by the attempt limit and the maximum lag, and cover exactly the original notifications. A send starts only under a live lease that the start renews. An accepted send is never recorded abandoned while its dispatcher still holds it. Delivery-state fencing is part of the shared store suite.
- `notification-inbox`: generated identifiers are bounded by a documented maximum that holds on every store. A generator that violates it fails at construction where possible, and otherwise before any write.

## Impact

- **Modules:** `notify` (`email_dispatcher.go`, `email.go`, `memory_email.go`, `service.go`, `id.go`, `docs/email.md`), `notify/sqlstore` (`email.go` claim and record SQL, `ddl/email/*.sql`, the email schema expectation), `notify/notifytest` (new `RunEmail` and `RunEmailDispatch` cases, which run on memory and every SQL driver and dialect combination).
- **Host-visible:** host `EmailStore` implementations must pass the new `RunEmail` cases. Host ID generators must mint at most 64 bytes. `WithEmailLease` godoc now says the lease must outlast one send, not one pass.
- **Schema:** `notify_email_deliveries` gains `batch_size` (nullable integer) on all three dialects, and `VerifyEmailSchema` expects it. Identifier columns stay `VARCHAR(64)` on MySQL, and the identifier bound is enforced in code instead. Nothing is tagged yet, so a development database re-applies the email schema; there is no ALTER migration.
