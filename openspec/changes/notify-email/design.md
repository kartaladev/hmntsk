## Context

- See proposal.md for why; requirements are in `specs/notification-email/spec.md`.
- Builds on `notify-core` as designed: `notify.Service`, the `Store` port, `Notification` (with `State`, `CreatedAt`, `InactiveAt`), the in-memory store, `notify/sqlstore` over `sqlkit.Executor`, the `Pruner`, the error set (`ConfigurationError`, `ErrConfiguration`, ...) and the option style.
- Builds on `sqlkit` as designed: `Executor` (`Exec`, `Query` with a scan callback, `Do`, `InTransaction`, `Dialect`), `NewWriter`, `RenderSchema`/`PrefixToken`/`ApplySchema`, `VerifySchema` with `SchemaExpectation`, codecs, and the `sqlkittest` container helpers.
- The host-driven loop, lease and backoff patterns follow hmntsk's `Sweeper` (`sweep.go`) and `relay.Relay` (`relay/relay.go`): nothing starts on construction, claims are conditional updates on lease columns committed before any work, no row locking.
- Split rule: this change adds code only under `notify/`; `notify` stays stdlib-only and `notify/sqlstore` imports only `notify` and `sqlkit`.

## Goals / Non-Goals

**Goals:**
- Email ACTIVE notifications without duplicates across passes, instances and restarts, with a stated bound on the one unavoidable window.
- Keep email entirely optional: no table, schema check or port a host that doesn't email has to care about.
- No change to the `notify.Store` interface, the notifications table or the `notification-inbox` requirements.

**Non-Goals:**
- A built-in SMTP or provider client, templates, localisation, preferences, quiet hours, unsubscribe links, bounce handling.
- Digests on a schedule ("daily summary"); a host builds those from `Service.List`.
- Exactly-once delivery. At-least-once can reach it only with a `Mailer` that honours the idempotency key.

## Decisions

### 1. Email state lives in its own store port and table (option a)

```go
// EmailStore records email delivery state. notify.NewMemoryStore() and
// sqlstore.Store implement it; a host store may too.
type EmailStore interface {
    ClaimEmails(ctx context.Context, claim EmailClaim) ([]EmailCandidate, error)
    RecordEmails(ctx context.Context, record EmailRecord) (int64, error)
    PurgeEmailRecords(ctx context.Context, limit int) (int64, error)
}

type EmailClaim struct {
    Now          time.Time
    Owner        string        // dispatcher identity, recorded in the lease
    Lease        time.Duration
    CreatedUntil time.Time     // Now - grace delay
    CreatedFrom  time.Time     // Now - max lag
    Limit        int
}

type EmailCandidate struct {
    Notification Notification  // read at claim time, state ACTIVE
    Status       EmailStatus   // CLAIMED, or SENDING for an in-doubt send
    BatchID      string        // set when a previous attempt reached SENDING
    Attempts     int
}

type EmailRecord struct {
    Owner         string
    IDs           []string      // notification IDs
    Status        EmailStatus
    BatchID       string
    Reason        string        // skip reason or last error
    NextAttemptAt *time.Time    // RETRY only
    At            time.Time
}

type EmailStatus string // CLAIMED, SENDING, SENT, RETRY, SKIPPED, FAILED, ABANDONED
```

**Table `notify_email_deliveries`** (prefix-aware, identifier collation on `recipient` and `owner`):

| Column | Type | Notes |
|---|---|---|
| notification_id | varchar(36) | PK; the notification's ID, no foreign key |
| recipient | varchar(255) | for grouping and diagnostics |
| status | varchar(16) | `EmailStatus` |
| batch_id | varchar(36) | null until SENDING; the message's idempotency key |
| owner | varchar(255) | null when unleased |
| lease_until | timestamp | null when unleased |
| attempts | int | attempts that reached the sender |
| next_attempt_at | timestamp | RETRY only |
| reason | text | skip reason or last error |
| sent_at | timestamp | SENT only |
| updated_at | timestamp | |

Indexes: `(status, lease_until)`, `(status, next_attempt_at)`.

**Why (a) over (b), columns on the notifications table:**
- (b) changes `notify-core`'s schema, `Store` contract and `VerifySchema` for every host, including hosts that never email. (a) is additive and optional: `sqlstore` publishes the email DDL as a separate document and verifies it separately.
- Lease and retry columns have a different write pattern (a claim per pass) from notification rows (publish, close, read). Keeping them apart keeps the notifications table's hot indexes and the serialising per-subject upsert out of the dispatcher's path.
- (b) would let `Pruner` delete delivery state together with the notification for free; (a) pays for that with a clean-up step (decision 6).

**No foreign key.** `notify-core` prunes by ID in batches across three dialects; SQLite only enforces foreign keys with a per-connection pragma the library can't guarantee. Orphans are removed by the dispatcher instead.

**Default:** `notify.NewMemoryStore()` and `sqlstore.Store` implement `EmailStore`. **Override:** any host store implementing both `Store` and `EmailStore` and passing `notifytest.RunEmail`.

**Claim algorithm** (portable, no row locks, like `ClaimOverdue`), inside `executor.Do`:
1. Select up to `Limit` notification IDs: `state = 'ACTIVE' AND created_at <= CreatedUntil AND created_at >= CreatedFrom`, left-joined to deliveries where the row is absent, or `status IN ('CLAIMED','RETRY')` with an expired or null lease and `next_attempt_at` null or due, or `status = 'SENDING'` with an expired lease. Order by `created_at, id`.
2. Insert missing delivery rows as CLAIMED with this owner and `lease_until = Now + Lease`, using the conflict-ignoring insert (the same `UpsertSuffix` no-op self-assignment `notify-core` uses).
3. Take over existing rows with a conditional update repeating step 1's status and lease predicate, keeping `status` for SENDING rows (so the in-doubt state survives) and setting CLAIMED otherwise.
4. Select back the rows whose `owner` and `lease_until` are this claim's, joined to their notifications. A row another dispatcher won is not returned.

`RecordEmails` updates only rows whose `owner` matches and whose lease hasn't been replaced; it returns rows affected, so a late write from a dispatcher whose lease lapsed changes nothing.

### 2. The dispatcher lives in package `notify`, not a `notify/email` module

- The only things email needs beyond `notify` are host ports; there is no SMTP or provider dependency to isolate.
- Living in `notify` lets the dispatcher reach the service's store and clock the way `Pruner` does, with no new exported accessor.
- **Default:** `notify.NewEmailDispatcher`. **Override:** a host that wants a different pipeline uses `EmailStore` directly, or `Service.List`.
- **Rejected:** `notify/email` module. It would need `Service` to export its store, and buys no dependency isolation.

```go
func NewEmailDispatcher(svc *Service, mailer Mailer, book AddressBook, template EmailTemplate,
    opts ...EmailOption) (*EmailDispatcher, error)
func (d *EmailDispatcher) Dispatch(ctx context.Context) (DispatchResult, error)
func (d *EmailDispatcher) Run(ctx context.Context, interval time.Duration) error // ctx.Err() on cancel
func (d *EmailDispatcher) Owner() string
```

The required ports are positional, so omitting one is visible at the call site and a nil one is a `ConfigurationError`. `NewEmailDispatcher` also refuses a service whose store does not implement `EmailStore`.

### 3. At-most-once by default; at-least-once with a stable idempotency key as the opt-in

```go
type DeliveryGuarantee string
const (
    AtMostOnce  DeliveryGuarantee = "AT_MOST_ONCE"  // default
    AtLeastOnce DeliveryGuarantee = "AT_LEAST_ONCE"
)
func WithDeliveryGuarantee(g DeliveryGuarantee) EmailOption
```

**One message's life**, per recipient batch:
1. Claimed rows arrive as CLAIMED (fresh) or SENDING (in doubt from an earlier attempt).
2. Fresh rows: mint a `BatchID` (UUIDv7) and record SENDING with it, committed, before calling the sender.
3. `Mailer.Send` with `EmailMessage.IdempotencyKey = BatchID`.
4. Nil error: record SENT. `ErrMailRejected`: record FAILED. `ErrMailInDoubt`: treat as in doubt (below). Any other error means "not sent": record RETRY with backoff, or FAILED once the attempt limit is reached.

**In doubt** (a SENDING row whose lease expired, or `ErrMailInDoubt`):
- `AtMostOnce`: record ABANDONED; never resent.
- `AtLeastOnce`: resend the same `BatchID` over exactly the notifications that carry it, still ACTIVE, never merged with newer ones, so the idempotency key keeps meaning the same message.

**Why at-most-once is the default:**
- The notification is still in the in-app inbox, which is the source of truth; email is the fallback. A crash between send and record loses one fallback email for one batch, while at-least-once would duplicate it.
- It matches what the proposal and user asked for: never duplicate messages.
- A host whose provider deduplicates by key (so a resend is harmless) opts into `AtLeastOnce` and loses nothing on a crash.

**Stated limits:**
- Under `AtMostOnce`, a process dying after SENDING is committed and before the sender returns loses that batch's email (counted as abandoned in a later pass).
- Under `AtLeastOnce`, the email is sent more than once unless the sender honours `IdempotencyKey`.
- Between the recheck in decision 5 and the send, a read or close is not observed.

### 4. Defaults and options

| Option | Default | Configuration error |
|---|---|---|
| `WithEmailGraceDelay(d)` / `WithoutEmailGraceDelay()` | `DefaultEmailGraceDelay = 5 * time.Minute` | d ≤ 0; both given |
| `WithEmailMaxLag(d)` | `DefaultEmailMaxLag = 24 * time.Hour` | d ≤ 0; d ≤ grace delay |
| `WithEmailBatchLimit(n)` (notifications per message) | `DefaultEmailBatchLimit = 20` | n ≤ 0 |
| `WithEmailClaimLimit(n)` (notifications per pass) | `DefaultEmailClaimLimit = 500` | n ≤ 0 |
| `WithEmailLease(d)` | `DefaultEmailLease = 5 * time.Minute` | d ≤ 0 |
| `WithEmailMaxAttempts(n)` | `DefaultEmailMaxAttempts = 5` | n ≤ 0 |
| `WithEmailBackoff(base, ceiling)` | 1 minute, doubling, ceiling 1 hour, 20% jitter | base ≤ 0; ceiling < base |
| `WithEmailFilter(f)` / `WithEmailKinds(kinds...)` | every kind | nil filter; empty kinds; both given |
| `WithDeliveryGuarantee(g)` | `AtMostOnce` | unknown value |
| `WithEmailOwner(s)` | `email-` + UUIDv7 | empty |
| `WithEmailErrorHandler(fn)` | no-op, documented as silent | nil |

**Why these values:**
- **5-minute grace.** Long enough for someone active in the app, where the realtime signal reaches them within seconds, to see and read it; short enough that assignment email still feels timely.
- **24-hour maximum lag.** Enabling email, or an outage longer than a day, must not flood inboxes with stale work; the in-app inbox still has everything.
- **20 per message.** One readable message; bursts spread over passes (one pass every interval the host picks).
- **Lease 5 minutes and 5 attempts with 1-minute to 1-hour backoff.** Mirrors the relay's shape, so operators reason about both the same way; a lease comfortably outlasts one pass of 500 notifications.
- **Every kind by default.** Email only runs when the host wires a `Mailer`, `AddressBook` and `EmailTemplate`, so wiring it is already the opt-in; narrowing is one option. `tasknotify`'s docs recommend `EmailKinds` with its offer and assigned kind constants.

The pass groups claimed candidates by recipient, applies the filter, looks the address up once per recipient, and sends each recipient **one message** per pass covering at most `BatchLimit` of their candidates, oldest `CreatedAt` first. Candidates beyond the batch limit are **released immediately**: `RecordEmails` with `Status: CLAIMED` clears their owner and lease (and nothing else), so the next pass, on any instance, can claim them without waiting for the lease to lapse.

- **Why immediate release is safe under at-most-once.** Released rows never reached SENDING and carry no batch ID, so no message was attempted for them. Releasing them cannot cause a duplicate; only SENDING rows are in doubt, and those are never released this way.
- **Rejected:** leaving them CLAIMED until the lease lapses. It delays the remainder by up to one lease (5 minutes) for no safety gain.

### 5. Retention and state interplay

- **Recheck before sending.** Just before rendering each message, the dispatcher re-reads its notifications through `Store.Get` (a batch spans subjects, so a subject-scoped `List` doesn't apply) and drops any that are no longer ACTIVE or no longer exist, recording them SKIPPED with reason `inactive` or `deleted`. If nothing remains, no message is sent.
- **Pruned before emailing.** A deleted notification is never claimed again (step 1 joins from the notifications table); a delivery row left behind is an orphan.
- **Orphans.** Each pass ends with `PurgeEmailRecords(ctx, ClaimLimit)`: select delivery IDs `NOT EXISTS` in the notifications table, delete by ID, batched. Portable for the same reason `notify-core`'s watermark clean-up is.
- **Evicted under `EvictOldestActive`.** Same as pruned; stated in docs.
- **READ or CLOSED at claim time.** Never claimed (step 1 requires ACTIVE).
- **No `Pruner` amendment.** The pruner stays ignorant of email; orphan clean-up is the dispatcher's.

### 6. Ports

```go
type AddressBook interface {
    // AddressOf returns ok=false when the recipient has no address; that is not an error.
    AddressOf(ctx context.Context, recipient string) (address string, ok bool, err error)
}
type AddressBookFunc func(ctx context.Context, recipient string) (string, bool, error)

type EmailTemplate interface {
    Render(ctx context.Context, batch EmailBatch) (EmailContent, error)
}
type EmailTemplateFunc func(ctx context.Context, batch EmailBatch) (EmailContent, error)
type EmailBatch   struct { Recipient, Address string; Notifications []Notification }
type EmailContent struct { Subject, TextBody, HTMLBody string; Headers map[string]string }

type Mailer interface {
    // Send returns nil when sent, ErrMailRejected for a permanent refusal,
    // ErrMailInDoubt when it cannot tell, and any other error for "not sent".
    Send(ctx context.Context, message EmailMessage) error
}
type MailerFunc func(ctx context.Context, message EmailMessage) error
type EmailMessage struct {
    To, Subject, TextBody, HTMLBody string
    Headers         map[string]string
    IdempotencyKey  string   // the batch ID; identical across attempts of this message
    Recipient       string
    NotificationIDs []string
}

type EmailFilter interface {
    ShouldEmail(ctx context.Context, n Notification) (bool, error)
}
type EmailFilterFunc func(ctx context.Context, n Notification) (bool, error)
func EmailKinds(kinds ...string) EmailFilter
```

- **Skip reasons** (constants): `EmailSkipNoAddress = "no_address"`, `EmailSkipFiltered = "filtered"`, `EmailSkipInactive = "inactive"`, `EmailSkipDeleted = "deleted"`. A skip is final.
- **Errors:** an `AddressBook` or `EmailFilter` error is retried like "not sent" (RETRY with backoff, counting an attempt). An `EmailTemplate` error is permanent (FAILED): a rendering bug won't fix itself by retrying. Every error reaches `WithEmailErrorHandler`, wrapped with the recipient and batch ID, and never aborts the pass.
- **New sentinels:** `ErrMailRejected`, `ErrMailInDoubt`.

### 7. `DispatchResult`

```go
type DispatchResult struct {
    Claimed, Sent, Messages                      int
    SkippedNoAddress, SkippedFiltered, SkippedInactive int // inactive includes deleted
    Retried, Failed, Abandoned                   int
    Purged                                       int64
    Unrecorded                                   int // RecordEmails affected fewer rows than expected (lease lost)
}
```

Counts are per notification except `Messages`. `Dispatch` returns an error only when claiming itself fails; everything after that is counted and reported to the error handler, as `relay.Relay` does.

### 8. SQL store additions (`notify/sqlstore`)

- DDL documents per dialect under `notify/sqlstore/ddl/email/`, rendered with `sqlkit.RenderSchema` and the store's table prefix.
- `(*Store).EmailSchema() string`, `(*Store).MigrateEmail(ctx)` (tests and development only, via `sqlkit.ApplySchema`) and `(*Store).VerifyEmailSchema(ctx)` (via `sqlkit.VerifySchema` with an email-only `SchemaExpectation`).
- `(*Store).VerifySchema(ctx)` from `notify-core` is unchanged and does not require the email table.
- `NewEmailDispatcher` cannot verify the schema without a context; the docs tell hosts to call `VerifyEmailSchema` at startup, alongside `VerifySchema`.

### 9. Conformance

- `notifytest.RunEmail(t, factory)` in `notify/notifytest`, where `factory` returns a store implementing both `Store` and `EmailStore`.
- Cases: claim eligibility (ACTIVE, grace, lag); exclusive claim by two owners concurrently (200 iterations); lease expiry and takeover; SENDING survives takeover with its batch ID; `RecordEmails` refused for a lost lease; RETRY not claimed before `next_attempt_at`; skipped, sent, failed and abandoned never claimed again; orphan purge; identical results on every store.
- Run on the memory store and the 7 SQL combinations from `sqlkit/sqlkittest` helpers.

## Risks / Trade-offs

- [At-most-once loses one batch's email on a crash between SENDING and the sender returning] → The notification remains in the in-app inbox; abandoned counts are reported; `AtLeastOnce` is one option away.
- [At-least-once duplicates email with a sender that ignores the idempotency key] → Documented; the key is exposed so a host can deduplicate in its own `Mailer`.
- [Read or closed between recheck and send] → Stated limit; the window is one render plus one send.
- [Delivery rows orphaned by pruning until the next pass] → Purged each pass; orphans are never claimed because claims join from notifications.
- [Every kind emailed by default floods users of a noisy publisher] → Grace delay, 24-hour lag, 20-per-message batching, and `EmailKinds` documented prominently; `tasknotify` docs recommend offer and assigned only.
- [The claim's left join adds load on the notifications table] → Bounded by `ClaimLimit` and the existing `(recipient, state)` and `created_at` access paths; a pass interval of a minute is plenty.
- [Skips are final, so a host that fixes an address or widens its filter later won't email past notifications] → Documented; the 24-hour lag makes re-emailing old notifications undesirable anyway.
- [Relies on the same conflict-ignoring insert workaround as `notify-core`] → Same open question; only rendered SQL changes if `sqlkit` adds the capability.

## Migration Plan

- Additive. Hosts that email apply `notify/sqlstore/ddl/email/<dialect>.sql` through their own pipeline, call `VerifyEmailSchema` at startup, and start the dispatcher.
- **Rollback:** stop the dispatcher and drop `notify_email_deliveries`. Notifications are untouched.
- **Required upstream amendments to `notify-core`: none.** This change adds files to modules `notify-core` creates (`notify`, `notify/sqlstore`, `notify/notifytest`) and implements `EmailStore` on its memory and SQL stores, so it applies after `notify-core`.

## Resolved open questions

- **Candidates beyond one message's batch limit.** Decided: released immediately, not left to their lease (decision 4). This is consistent with at-most-once: released rows never reached SENDING, so no message was attempted for them.
- **A dedicated conflict-ignoring insert in `sqlkit`.** Decided, as in `notify-core`: keep the `UpsertSuffix` no-op self-assignment for now; a dedicated `sqlkit` statement is a follow-up that changes only rendered SQL.
