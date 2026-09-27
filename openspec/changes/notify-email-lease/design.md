## Context

- See proposal.md (Why) for the four defects. The requirements are in `specs/notification-email/spec.md` and `specs/notification-inbox/spec.md`.
- The evidence is in the verified audit, section H. Every item has a failing reproduction in the scratch module `$V/notify`, where `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`:
  - N4: `email_test.go` `TestEmailLeaseLapseMidPass`, on SQLite through `notify/sqlstore`.
  - N5a: `email_test.go` `TestAtLeastOnceInDoubtResendsAreBounded`, on the memory store.
  - N5b: `email_test.go` `TestAtLeastOnceResendCoversExactlyTheOriginalNotifications`, on the memory store.
  - N8: `sqlstore_test.go` `TestLongGeneratedIDsPublishOnEveryDialect/mysql`.
  - Run any of them with `cd $V/notify && go test -count=1 -run '<Name>' .`.
- Current behaviour, as observed in the code:
  - `emailPass.record` (`notify/email_dispatcher.go`) stamps every record with `p.now`, the pass's start time.
  - `RecordEmails` (`notify/memory_email.go`, `notify/sqlstore/email.go`) filters on `owner` only.
  - `SENDING` keeps the existing `lease_until`. It does not renew it.
  - `send` returns without sending when `record` changed fewer rows than asked. The rows it did move to `SENDING` stay there under a key that was never sent.
  - `resolveDoubt` never checks `Attempts` or the notification's age.
  - `send` rechecks in-doubt candidates like fresh ones, so it drops read or closed notifications and still reuses the batch key.
  - `ClaimEmails` applies `LIMIT` across every due row, so it can split one in-doubt message.
  - IDs come from `Service.ids`, which `WithIDGenerator` replaces, unchecked. They are used for notification IDs (`Publish`, `CloseRequest.SuccessorInsertions`), the dispatcher's generated owner and email batch keys. MySQL declares `id`, `notification_id` and `batch_id` as `VARCHAR(64)`. PostgreSQL and SQLite use `TEXT`.
- There is no git tag yet, so port and schema contracts can change without a version bump (`library-design.md` rule 7). This change records them as intentional.

## Goals / Non-Goals

**Goals:**
- A send starts only under a live, renewed lease, and its outcome is never lost to a takeover that the fence could have prevented.
- At-least-once repeats end: they are bounded by the attempt limit and the maximum lag.
- An idempotency key is never reused for a different set of notifications.
- One generator behaves the same on every store.

**Non-Goals:**
- Exactly-once delivery, or bounding the mailer call itself with a context deadline. The host owns its `Mailer` and its timeouts.
- Widening identifier columns or supporting identifiers longer than 64 bytes.
- Changing retry backoff, the at-most-once default, or the claim window for fresh notifications.

## Decisions

### 1. Fence the start of a send on a live lease; fence outcomes on ownership

**What.** `RecordEmails` treats `SENDING` specially. It changes a row only when all of these hold:
- `owner = record.Owner`;
- `lease_until > record.At`;
- every one of `record.IDs` passes those two checks in the same transaction (all-or-nothing).

When it changes the rows, it sets `lease_until = record.At + record.Lease`.

Every other status keeps today's `owner = record.Owner` fence. `ClaimEmails` always overwrites `owner`, and releasing a row clears it. So a matching owner proves that nobody took the row over since this dispatcher held it, whether or not the lease has lapsed. That is exactly the condition under which the dispatcher's outcome is the truth. The dispatcher fills in `record.At` from `service.clock.Now()` on every call, and never uses the pass start.

**Why not require a live lease for every record,** as the audit first suggested? A send that overruns its lease with nobody taking over would then fail to record `SENT`. The next claimer would find the row `SENDING` and, under the default `AtMostOnce`, record an accepted email as `ABANDONED`. That is the very outcome N4 reports. The hazard is only in *starting* work under a lapsed lease, because a concurrent claimer can take over after the start.

**Refused start.** A `SENDING` record that changes 0 rows means the dispatcher does not call the `Mailer`. It records `CLAIMED` (a release) for the message's IDs. That release is fenced by owner, so it touches only the rows this dispatcher still holds. The attempt is not counted, and the rows are counted as `Unrecorded`.

**Port change.** `EmailRecord` gains `Lease time.Duration`, used only with `SENDING`. A `SENDING` record whose `Lease` is not positive is refused as a programming error. `EmailRecord` also gains `BatchSize int` (decision 4).

**Default and override.** The default lease is `DefaultEmailLease`, five minutes. `WithEmailLease` overrides it. Its godoc changes from "should outlast one pass" to "must outlast one send". A send that outlasts a whole renewed lease may be taken over and is then in doubt. This is a stated limit (rule 4), documented in `notify/docs/email.md`. The fence itself has no override point: it is the guarantee that concurrent dispatchers never send one notification twice, and relaxing it would break that guarantee.

### 2. Bound at-least-once repeats with the existing attempt limit and maximum lag

**What.** In `resolveDoubt`, under `AtLeastOnce`, before any lookup or send:
- **Attempts used up.** If the candidates' `Attempts >= d.attempts`, record `FAILED` with reason "in doubt after N attempts", count the rows `Failed`, and report the error.
- **Too old.** If the oldest candidate was created before `now - maxLag`, record `ABANDONED` with reason "in doubt past the maximum lag", count the rows `Abandoned`, and report the error.

A resend's `SENDING` record already counts an attempt (`Attempt: true`), so attempt 1 is the original send. With `WithEmailMaxAttempts(3)` the scratch test therefore observes exactly three sends.

**Why `ABANDONED` for lag but `FAILED` for attempts.** The spec already defines `FAILED` as "used up its attempts". Lag expiry is a policy decision not to repeat a message whose outcome is unknown, and `ABANDONED` ("in doubt, not repeated") describes that exactly.

**Default and override.** The defaults are `DefaultEmailMaxAttempts` (5) and `DefaultEmailMaxLag` (24h). `WithEmailMaxAttempts` and `WithEmailMaxLag` override both the retry bound and the repeat bound. There is deliberately no separate "in-doubt attempts" option: one budget is simpler to reason about, and a host that wants more repeats raises the one limit.

### 3. A repeat covers exactly the original notifications; an irreproducible message gets a new key

**What.** An in-doubt resend no longer goes through `recheck`'s state filter. It reads each original notification with `Store.Get`:
- **Every original notification still exists,** whatever its state. The message is re-rendered from exactly those notifications, in the original oldest-first order, and sent under the original key. The in-doubt message may already have been delivered, so a read notification in it is not news to the recipient. The key's contract matters more.
- **Any original notification is missing.** This is detected either by `Get` returning `ErrNotFound` or by the claimed group being smaller than the recorded `BatchSize` (decision 4). The deleted ones, and the ones never returned, are recorded `SKIPPED` with reason `deleted`. The remaining candidates go through the fresh-send path: `recheck` drops inactive ones, a new key is minted, and the batch is re-sent.

**Alternatives considered.**
- *(b) Always abandon a shrunken message.* This loses the survivors, which contradicts the purpose of `AtLeastOnce`.
- *(c) Resend the shrunken set under the old key.* This is today's defect: a provider that deduplicates on the key drops or rejects the message.

The new-key path can duplicate the survivors. That is the cost `AtLeastOnce` already accepts, and it is documented.

**Default and override.** The default is `AtMostOnce`, under which none of this applies: in-doubt sends are abandoned. `WithDeliveryGuarantee(AtLeastOnce)` opts in. Within `AtLeastOnce` the rule has no override point. The key's meaning is a contract with the host's sender, and letting it vary would make the key meaningless. A host that wants different handling uses `AtMostOnce` and re-sends from its own records.

### 4. Record the message size, and claim an in-doubt message whole

**What.**
- **Schema.** `notify_email_deliveries` gains `batch_size` (nullable `INT`/`INTEGER`) in `ddl/email/{postgres,sqlite,mysql}.sql`, and the email `SchemaExpectation` includes it. The memory store gains a matching field.
- **Recording.** A `SENDING` record writes `BatchSize = len(IDs)`, and `EmailCandidate.BatchSize` returns it.
- **Claiming.** After `ClaimEmails` selects due rows, it extends the selection with every lapsed `SENDING` row that shares a `batch_id` with a selected `SENDING` row and whose notification exists. This can exceed `Limit` by less than one message. Both the SQL and memory takeover are conditional on the lease being lapsed and run in one transaction, so of two concurrent claimers only one gets any given row. The whole-message property follows because both claimers expand the same batch.

**Why a column,** rather than counting rows with the same `batch_id` at claim time? `PurgeEmailRecords` deletes the delivery rows of deleted notifications, so after a purge a count shrinks together with the message and hides the loss. Recording the size at `SENDING` time survives the purge.

**Default and override.** This is internal state with no option. It is what makes decision 3's guarantee checkable.

### 5. A documented identifier bound, checked at construction and at every mint

**What.** Add `notify.MaxIDBytes = 64`, documented next to `MaxIdentifierBytes`, which remains the 255-byte bound on recipient, source and subject. `Service` wraps its generator in a checking generator: `NewID` returns an error when the minted value is empty or longer than `MaxIDBytes`. The error is a `*ConfigurationError` naming the bound, because the fault is the host's wiring, not the request. The HTTP layer already maps a non-validation error to a server error.
- **At construction.** `notify.New` calls the wrapped generator once and returns its error. Generators promise uniqueness, not density, so consuming one value is harmless. This is recorded as an assumption.
- **At every mint.** `Publish`, `CloseRequest.SuccessorInsertions` (through the service's wrapped generator), the dispatcher's owner and email batch keys all use the wrapped generator. A bad value fails before any write, so the outcome is identical on every store.
- **Godoc.** `ConfigurationError`'s godoc changes from "found at construction" to "found at construction where possible, and otherwise before anything is written".

**Alternatives considered.**
- *Widen the MySQL columns to 255.* That only moves the cliff, and it enlarges the composite index keys.
- *Use `ValidationError`.* That would blame the API caller for a host wiring fault, and map to 422.

**Default and override.** The default `UUIDv7Generator` mints 36 bytes. `WithIDGenerator` replaces it with any generator within 64 bytes. The bound itself is not configurable, because it is fixed by the library-owned schema (rule 4: stated and enforced, not silently relaxed).

### 6. Tests live in the shared suites

- **Store-level fencing** (`SENDING` refused when lapsed, renewal, all-or-nothing, whole-message claim, `batch_size` round trip) goes in `notifytest.RunEmail`.
- **Dispatcher behaviour** (N4, N5a, N5b) goes in `notifytest.RunEmailDispatch`. It already drives an injectable clock over any `EmailFactory`, so memory and every SQL driver and dialect combination run it.
- **The identifier bound** goes in `notify` (service tests on the memory store) and in `notify/sqlstore`'s harness, so that every dialect, MySQL included, runs the scratch N8 case.

The port of `TestEmailLeaseLapseMidPass` keeps its deterministic hooks: the clock advances inside the mailer, and dispatcher B's pass runs inline. Its assertion is restated for the fixed behaviour. A no longer hands bob to the mailer at all, so B's pass is run after A's instead of inside bob's send. The test asserts that bob is sent exactly once and ends `SENT` (or `RETRY` for the transient case).

## Risks / Trade-offs

- [A host `EmailStore` that ignores the new `SENDING` fence reintroduces N4.] → The new `RunEmail` cases fail against such a store. The `EmailStore` godoc states that a host store must pass `notifytest.RunEmail`.
- [Re-applying the email schema drops delivery state in development databases.] → Nothing is tagged. This is recorded in Migration Plan and in the change log.
- [Probing the generator at construction consumes one identifier and can fail if the generator depends on something not yet ready.] → Documented on `WithIDGenerator`. A host can construct the service after its dependencies.
- [The new-key path for an irreproducible message can duplicate the survivors.] → This is inherent in `AtLeastOnce`, and is documented in `notify/docs/email.md`.
- [The claim can exceed its limit by up to one batch minus one.] → Documented on `EmailClaim.Limit`. It is bounded by `WithEmailBatchLimit`.

## Migration Plan

This is pre-tag. The DDL documents change in place (`batch_size` added). A development database drops and re-applies `notify_email_deliveries`, or runs `ALTER TABLE ... ADD COLUMN batch_size` by hand. There is no data migration. Rolling back reverts the commit and the column. Host `EmailStore` implementations must add the fence and `BatchSize`, and must pass `notifytest.RunEmail`.

## Assumptions (recorded instead of asking)

- An in-doubt resend may include notifications read or closed meanwhile (decision 3), because honouring the key outweighs trimming the content.
- Lag expiry of an in-doubt message is `ABANDONED`, and attempt exhaustion is `FAILED` (decision 2).
- Outcome records stay owner-fenced, not lease-fenced (decision 1). This deliberately refines the audit's suggestion that every record require a live lease.
- A `ConfigurationError` returned from `Publish` for an over-long identifier is acceptable (decision 5).

## Open Questions

- Whether `DispatchResult` should count refused send starts separately from `Unrecorded`. This is deferrable: it adds a counter and does not change behaviour.
