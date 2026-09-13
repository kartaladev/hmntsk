## Context

See proposal.md for why. Constraints that shape the approach:

- **The notifier will be split out.** `notify/**` will become its own repository before its first tag (decision P2). It SHALL import only `sqlkit`, the standard library and its own third-party dependencies, never `github.com/kartaladev/hmntsk`. `depguard` and `make split-check`, added by the `sqlkit` change, enforce this.
- **Storage runs on `sqlkit`.** The notification store is written once, against `sqlkit.Executor`, and runs on the three executor modules. Per the final `sqlkit` design:
  - **Executor:** `Dialect() Dialect`, `Exec(ctx, Statement) (int64, error)`, `Query(ctx, Statement, scan func(rows Rows) error) error` (a callback; rows never escape), `Do(ctx, fn func(ctx) error) error`, `InTransaction(ctx) bool`. Every executor is also an `Execer` and a `Querier`.
  - **Types and SQL building:** `Statement{SQL, Args}`, `Rows{Next, Scan, Err}`, and `NewWriter(dialect)` with `Write`, `Bind`, `BindAll`, `Done`.
  - **Schema:** `RenderSchema(document, prefix)`, `ApplySchema`, `DropTables`, and `VerifySchema(ctx, querier, dialect, prefix, SchemaExpectation)` with `TableExpectation{Columns, IdentifierColumns, Indexes}`, reporting `SchemaError`, which matches `ErrSchemaMismatch`.
  - **Codecs:** `EncodeTime`/`DecodeTime`, `DecodeJSON`, `DecodeString`, `DecodeInt`, `EncodeRaw`, `NormalizeTime`.
  - **Executor modules:** `sqlkit/stdsql` (`stdsqlexec.New(*sql.DB, sqlkit.Dialect)`), `sqlkit/pgx` (`pgxexec.New(*pgxpool.Pool)`), `sqlkit/gorm` (`gormexec.New(*gorm.DB, sqlkit.Dialect)`).
  - **Test helpers:** container helpers `RunTestPostgres`, `RunTestMySQL`, `RunTestSQLite` in `sqlkit/sqlkittest`.
- **The hmntsk house style applies:**
  - functional options with named defaults, and ports with a `Func` adapter;
  - sentinel errors plus concrete types that `Unwrap` to them;
  - nil or contradictory wiring is a configuration error at construction;
  - host-driven loops (`Sweeper.Sweep`/`Run`, `Relay.Relay`/`Run`) that start nothing on their own;
  - one conformance suite across every store combination;
  - no build tags for integration tests.
- **The library-design rule** (`.claude/rules/library-design.md`) requires every decision below to state its default and how a consumer overrides it.

## Goals / Non-Goals

**Goals:**
- A generic, durable notification inbox with idempotent publishing and version-safe closing, identical on memory and on 7 SQL combinations.
- Retention a host can reason about: bounded by default, strategy selectable, enforced by a pass the host drives.
- Realtime change signals that never block a publisher and never leak content.
- An HTTP contract a host mounts in a few lines.

**Non-Goals:**
- Any knowledge of tasks. Task rules, kinds and links belong to `tasknotify`.
- WebSocket, and Redis or NATS broadcasters (`notify-realtime-adapters`); email (`notify-email`).
- Native `store/*` transaction sharing. Notification writes are never part of a task transaction (see decision 9).
- Group-addressed notifications. Every notification is per recipient; a publisher expands groups before publishing.
- Replay of missed signals. User preferences, digests and localisation.

## Decisions

### 1. Module layout

| Module | Contents | Imports |
|---|---|---|
| `notify` | model, `Service`, ports, in-memory `Store`, in-process `Broadcaster`, `Hub`, `Pruner`, HTTP and SSE handlers, UUIDv7 generator | stdlib only (tests: testify, goleak, mock) |
| `notify/notifytest` | `Run(t, factory)` conformance suite for any `notify.Store` | `notify`, testify |
| `notify/sqlstore` | `Store` over `sqlkit.Executor`, DDL per dialect, schema expectation for `VerifySchema` | `notify`, `sqlkit`; tests: `sqlkit/stdsql`, `sqlkit/pgx`, `sqlkit/gorm`, `sqlkit/sqlkittest` (container helpers, reused rather than rewritten) |

- Docs live in `notify/docs/` (`notifications.md` and `schema.md`).
- **Alternative rejected:** a store module per driver, as `store/*` does. It triplicates the watermark and pruning rules; see the D2 decision.
- **Trade-off:** `notify/sqlstore`'s `go.mod` lists the three drivers as test dependencies. Module graph pruning keeps them out of consumers' builds.

### 2. Own ports, errors, clock and IDs, and no shared code with hmntsk

- `notify.Clock { Now() time.Time }` with `SystemClock` and `ClockFunc`. It has the same method set as `hmntsk.Clock`, so a host passes its engine clock unchanged.
- **Errors** (sentinels, matched with `errors.Is`):
  - `ErrNotFound` (404)
  - `ErrValidation` (400)
  - `ErrUnauthorized` (403)
  - `ErrConfiguration` (construction)
  - `ErrTooManyStreams` (429)
  - `ErrUnavailable` (503)
- **Error types:**
  - `ConfigurationError{Detail}` unwraps to `ErrConfiguration`.
  - `ValidationError{Subject, Issues []ValidationIssue{Pointer, Detail}}` unwraps to `ErrValidation`, and has the same wire shape as hmntsk's.
- **IDs:** an internal UUIDv7 generator, a copy of hmntsk's `id.go` algorithm. Notification IDs are UUIDv7 strings. `WithIDGenerator(IDGenerator)` overrides it.
- **No `GroupResolver` in `notify-core` (deviation from the proposal).** Every notification is addressed to one recipient, so a subscription never needs group membership: the default policy is "own notifications only". A publisher that addresses groups, such as `tasknotify`, expands them itself through hmntsk's resolver. The port can be added later without a breaking change if a group-level subscription is ever needed.

### 3. Model

```go
type State string
const (StateActive State = "ACTIVE"; StateRead State = "READ"; StateClosed State = "CLOSED")

type Notification struct {
    ID             string            `json:"id"`
    Recipient      string            `json:"recipient"`
    SourceID       string            `json:"sourceId"`
    Subject        string            `json:"subject"`
    SubjectVersion int64             `json:"subjectVersion"`
    Kind           string            `json:"kind"`
    State          State             `json:"state"`
    ClosedReason   string            `json:"closedReason,omitempty"`
    Title          string            `json:"title,omitempty"`
    Links          map[string]string `json:"links,omitempty"`   // relation -> href, opaque
    Data           json.RawMessage   `json:"data,omitempty"`    // opaque, byte-exact
    CreatedAt      time.Time         `json:"createdAt"`         // UTC, microseconds
    ReadAt         *time.Time        `json:"readAt,omitempty"`
    ClosedAt       *time.Time        `json:"closedAt,omitempty"`
    InactiveAt     *time.Time        `json:"inactiveAt,omitempty"` // first left ACTIVE; age is measured from it
}

type Draft struct { // what a publisher supplies
    Recipient, SourceID, Subject, Kind, Title string
    SubjectVersion int64
    Links map[string]string
    Data  json.RawMessage
    // Coalesce: create nothing when the recipient already has a non-CLOSED
    // notification of this kind on this subject (amendment A2, decision 13).
    Coalesce bool
}

// Successor is a notification a close publishes, in the same transaction, to
// each recipient it closed (amendment A1, decision 13).
type Successor struct {
    SourceID, Kind, Title string
    SubjectVersion        int64
    Links                 map[string]string
    Data                  json.RawMessage
}
```

**Validation** (a `ValidationError`, nothing written):
- `Recipient`, `SourceID`, `Subject` and `Kind` are required.
- `SubjectVersion` must be ≥ 0.
- `Kind` has at most 100 bytes, `Subject` and `SourceID` at most 255, and `Recipient` the same as hmntsk identifiers.
- `Data` must be valid JSON.

The core defines no kinds and no link relations. Those are the publisher's documented constants.

### 4. Service, the only entry point a publisher or handler uses

```go
func New(store Store, opts ...Option) (*Service, error)
// Options: WithClock, WithIDGenerator, WithBroadcaster (default: NewInProcessBroadcaster()),
//          WithSignalErrorHandler (default: no-op, documented as silent)

func (s *Service) Publish(ctx context.Context, drafts ...Draft) (PublishResult, error)
func (s *Service) Close(ctx context.Context, req CloseRequest) (CloseResult, error)
func (s *Service) Get(ctx context.Context, recipient, id string) (Notification, error)
func (s *Service) List(ctx context.Context, q ListQuery) (Page, error)
func (s *Service) CountActive(ctx context.Context, recipient string) (int64, error)
func (s *Service) MarkRead(ctx context.Context, recipient string, ids ...string) (MarkResult, error)
func (s *Service) MarkAllRead(ctx context.Context, recipient string, through time.Time) (MarkResult, error)
func (s *Service) Broadcaster() Broadcaster

type PublishResult struct { Created []Notification; Duplicates, Suppressed, Coalesced int }
type CloseRequest  struct {
    Subject string; Kinds []string /* empty = every kind */; Version int64; Reason, Except string
    Successor     *Successor // optional: published to each recipient this call closed (A1)
    SuccessorSkip []string   // recipients who get no successor
}
type CloseResult   struct { Closed int64; Recipients []string; Successors []Notification; SuccessorsSuppressed int }
type MarkResult    struct { Marked int64 }
type ListQuery     struct { Recipient string; States []State; Kinds []string; Subject string; Limit int; Cursor string }
type Page          struct { Notifications []Notification; NextCursor string }
const DefaultListLimit = 50; const MaxListLimit = 500
```

- **Grouping and ordering.** `Publish` groups drafts by subject and writes each subject in its own transaction, in sorted subject order, so lock order is deterministic.
- **Signals after commit.** After a write commits, the service broadcasts one `Signal` per affected recipient: creations, closes, marks. A broadcast failure goes to the signal error handler and never fails the write; the store is the source of truth.
- **Rejected alternative:** handlers calling the store directly. Signals would then depend on every caller remembering to broadcast.

### 5. Store port

```go
type Store interface {
    Insert(ctx context.Context, subject string, notifications []Notification) (InsertResult, error)
    Close(ctx context.Context, req CloseRequest, at time.Time, ids IDGenerator) (CloseResult, error) // ids stamps successors
    Get(ctx context.Context, recipient, id string) (Notification, error)
    List(ctx context.Context, q ListQuery) (Page, error)
    CountActive(ctx context.Context, recipient string) (int64, error)
    MarkRead(ctx context.Context, recipient string, ids []string, at time.Time) (MarkResult, error)
    MarkAllRead(ctx context.Context, recipient string, through, at time.Time) (MarkResult, error)
    Prune(ctx context.Context, req PruneRequest) (PruneResult, error)
}
type InsertResult struct { Created []Notification; Duplicates, Suppressed, Coalesced int }
```

- **Method contracts:**
  - `Insert` receives notifications for one subject that the service has already stamped (ID, `CreatedAt`, state ACTIVE). Each keeps its draft's `Coalesce` flag, which the store applies inside the subject's serialised transaction.
  - `Close` publishes `req.Successor` to the recipients it closed, minus `req.SuccessorSkip`, in the same transaction. Those recipients are only known inside the transaction, so the service passes its `IDGenerator` to `Store.Close`, and the store stamps each successor with a generated ID, `CreatedAt = at` and state ACTIVE (decision 13).
  - Every method is its own transaction.
  - `Get` returns `ErrNotFound` for a wrong recipient.
- **Default:** `notify.NewMemoryStore()`, for tests and single-process hosts. **Override:** `sqlstore.New(executor sqlkit.Executor, opts ...Option) (*Store, error)`, or any host implementation that passes `notifytest`.
  - `sqlstore.New` takes the dialect from `executor.Dialect()`; a nil executor is a `ConfigurationError`. Options: `WithTablePrefix`.
  - It also exposes `Schema() string` (the rendered DDL), `Migrate(ctx)` via `sqlkit.ApplySchema` (tests and development only) and `VerifySchema(ctx)` via `sqlkit.VerifySchema` with the store's `SchemaExpectation`.
  - Reads use `Executor.Query` with a scan callback, decoding with sqlkit's codecs. Statements are built with `sqlkit.NewWriter`.

### 6. Watermarks and the per-subject serialisation that makes them race-free

**Table `notify_watermarks`:**

| Column | Type | Notes |
|---|---|---|
| subject | varchar(255) | part of PK |
| kind | varchar(100) | part of PK; `*` means every kind |
| version | bigint | highest close version |
| updated_at | timestamp | |

Primary key (subject, kind).

**Algorithm, inside one transaction per subject:**

1. **Serialise.** Upsert the row (subject, `*`), creating it with version `-1` or leaving its version unchanged. PostgreSQL `ON CONFLICT DO UPDATE` and MySQL `ON DUPLICATE KEY UPDATE` take a row lock that is held to commit. SQLite has one writer. So every publish and close for the same subject serialises without `SELECT ... FOR UPDATE`, which fits the engine's no-row-locking rule.
2. **Close.**
   - Upsert (subject, kind) for each kind, or (subject, `*`) when `Kinds` is empty, to `GREATEST(version, :v)`. The expression is computed portably as a conditional update.
   - Then `UPDATE ... SET state='CLOSED', closed_reason, closed_at, inactive_at=COALESCE(inactive_at,:at) WHERE subject=:s AND kind IN (...) AND subject_version <= :v AND state <> 'CLOSED' AND recipient <> :except`.
   - Select the distinct recipients first, for `CloseResult`.
   - **Successors (A1).** When `Successor` is set, build one notification per closed recipient not in `SuccessorSkip`, stamped with `ids` and `at`, and run step 3 on them in this same transaction (after the watermark raise, so a successor below a newer watermark is suppressed). `CloseResult.Successors` lists those created; suppressed ones are counted in `SuccessorsSuppressed`. A retried close selects no recipients, so it creates no further successors; the first attempt's rows stay.
3. **Insert.**
   - Read the watermarks for (subject, `*`) and (subject, each draft kind).
   - Suppress a draft when `SubjectVersion < max(watermark[kind], watermark[*])`.
   - **Coalesce (A2).** For drafts with `Coalesce`, select recipients that already have a non-CLOSED notification of the draft's kind on the subject, and drop those drafts, counting them in `Coalesced`. This runs after the serialising upsert, so two concurrent coalescing publishes cannot both insert.
   - Insert the rest with a conflict-ignoring insert on the unique key (source_id, recipient).
   - Select by (source_id, recipient IN ...). A row whose `id` equals the generated ID was created, and anything else is a duplicate.

**Why the version comparisons differ.** A close closes versions **≤** v, and suppression applies to versions strictly **<** the watermark. That lets one event close kind K for everyone but its new holder (`Except`) and publish kind K at the same version, in either order. The alternative of suppressing ≤ breaks delegation, which closes and publishes "assigned" at one version.

**Rejected alternative:** per-row compare-and-set without the serialising upsert. A close and an older publish racing on separate connections can each miss the other's uncommitted rows. The concurrency case in `notifytest` exists to catch that.

**Dependency on `sqlkit`:** a conflict-ignoring insert. Today's `UpsertSuffix` renders an update with at least one assignment, and MySQL's `ON DUPLICATE KEY UPDATE` needs one. `sqlstore` uses a no-op self-assignment (`id = id`), unless `sqlkit` offers a dedicated capability. `INSERT IGNORE` is rejected because it also swallows unrelated errors.

### 7. SQL schema (`notify/sqlstore`)

**Table `notify_notifications`.** Identifier columns use the dialect's `IdentifierCollation`, JSON columns use `JSONColumnType` (text, byte-exact), and timestamps use `TimestampColumnType`.

| Column | Type | Nullable |
|---|---|---|
| id | varchar(36) | PK |
| recipient | varchar(255) | not null |
| source_id | varchar(255) | not null |
| subject | varchar(255) | not null |
| subject_version | bigint | not null |
| kind | varchar(100) | not null |
| state | varchar(16) | not null |
| closed_reason | varchar(255) | null |
| title | text | null |
| links | JSON text | null |
| data | JSON text | null |
| created_at | timestamp | not null |
| read_at | timestamp | null |
| closed_at | timestamp | null |
| inactive_at | timestamp | null |

**Keys and indexes:**
- unique `(source_id, recipient)`: idempotency;
- `(recipient, created_at, id)`: list order and keyset paging;
- `(recipient, state)`: count, count-bound selection;
- `(subject, kind, state)`: close;
- `(state, inactive_at)`: age pruning.

**Schema delivery:**
- The DDL is published per dialect under `notify/sqlstore/ddl/` as documents with sqlkit's `PrefixToken`, rendered with `sqlkit.RenderSchema(document, prefix)`, and `WithTablePrefix` works as in `store/*`.
- `Migrate` (`sqlkit.ApplySchema`) exists for tests and development only; hosts apply the published DDL through their own pipeline.
- `VerifySchema` calls `sqlkit.VerifySchema` with a `SchemaExpectation` holding a `TableExpectation{Columns, IdentifierColumns, Indexes}` for both tables. It reports every missing table, column, identifier collation or index as a `SchemaError`.

**Paging:** a keyset cursor over `(created_at, id)`, descending, encoded opaquely. A cursor from another recipient or filter set is a validation error.

### 8. Pruner and retention

```go
func NewPruner(svc *Service, opts ...PruneOption) (*Pruner, error)
func (p *Pruner) Prune(ctx context.Context) (PruneResult, error)
func (p *Pruner) Run(ctx context.Context, interval time.Duration) error  // returns ctx.Err() on cancel

// Options and defaults
WithMaxPerRecipient(n int)            // default DefaultMaxPerRecipient = 500
WithoutMaxPerRecipient()
WithMaxAge(d time.Duration)           // default DefaultMaxAge = 90 * 24h, inactive only, from InactiveAt
WithoutMaxAge()
WithRetentionStrategy(s RetentionStrategy) // default EvictOldestActive; or RetainActive
WithWatermarkRetention(d time.Duration)    // default DefaultWatermarkRetention = 7 * 24h
WithPruneBatch(n int)                 // default DefaultPruneBatch = 1000 rows per statement
WithPruneErrorHandler(func(ctx, error))    // default no-op, documented as silent

type RetentionStrategy string
const (EvictOldestActive RetentionStrategy = "EVICT_OLDEST_ACTIVE"; RetainActive RetentionStrategy = "RETAIN_ACTIVE")
type PruneResult struct { DeletedForAge, DeletedForCount, EvictedActive, WatermarksDeleted int64; Recipients []string }
```

**Configuration errors:**
- n ≤ 0 or d ≤ 0;
- an unknown strategy;
- `With` and `Without` for the same bound;
- both bounds removed;
- a batch ≤ 0.

This follows `delivery/redis/retention.go`: pointer-typed bounds, and an explicit zero is a mistake, not "off".

**Why these default values:**
- **500 per recipient.** A heavy user receiving about 5 notifications a working day keeps roughly five months, an inbox UI never pages past that, and the per-recipient index scans stay small.
- **90 days after becoming inactive.** One quarter of history covers "what happened to that task last month?" without keeping a compliance archive, which is the host's concern.
- **7-day watermark retention.** The relay's defaults (5 attempts, 30s backoff doubling, 1h ceiling, 5-minute lease) redeliver within about half an hour. 7 days covers a host that raised attempts more than a hundredfold. Dead-letter replay is manual and documented as re-creating notifications.
- **1,000 rows per statement.** Transactions stay short on MySQL and SQLite, and bind lists stay under dialect parameter limits.

**A pass. Every step selects IDs with a portable `SELECT ... ORDER BY ... LIMIT :batch`, then deletes `WHERE id IN (...)`.** That avoids `DELETE ... LIMIT` (absent on PostgreSQL, compile-flag-only on SQLite) and MySQL's refusal of a `LIMIT` subquery on the delete target.

1. **Age.** Repeatedly select IDs `WHERE state <> 'ACTIVE' AND inactive_at < :now - maxAge ORDER BY inactive_at, id`, delete them, and stop on a short batch.
2. **Count.**
   - Select recipients `GROUP BY recipient HAVING COUNT(*) > :cap`, in batches.
   - For each one, compute the excess.
   - Select the excess oldest inactive IDs (`ORDER BY created_at, id`) and delete them.
   - If the excess remains and the strategy is `EvictOldestActive`, select and delete the oldest ACTIVE IDs, counting them in `EvictedActive` and adding the recipient to `Recipients`.
3. **Watermarks.** Delete watermark rows with `updated_at < :now - watermarkRetention` whose subject has no notifications (`NOT EXISTS`). The watermark table is not the delete target of the subquery, so this is portable.
4. **Signals.** Broadcast a `Signal{Change: ChangePruned}` for each recipient in `Recipients`.

**Multiple instances:** passes running at once on several instances are safe. Deleting an already-deleted ID is a no-op, and two passes can only over-delete up to the bound, never below it. Documented as approximate.

**Rejected alternative:** trimming on insert, as the Redis sink does. A fan-out to 500 recipients would run 500 trims inside the projector's transaction.

### 9. Notification writes never join a host transaction (stated limit)

- `sqlstore` runs each method in `executor.Do` on a context it does not share with `store/*`. The executors keep their own context key.
- **Default:** its own transaction. **Override:** none, deliberately.
- Publishers use at-least-once delivery plus idempotency, so a shared transaction buys nothing and would couple split repositories.
- This is documented in `notify/docs/notifications.md`.

### 10. Realtime: broadcaster, hub and SSE

```go
type Change string // "created" | "read" | "closed" | "pruned"
type Signal struct { Recipient string; Change Change; At time.Time }

type Broadcaster interface {
    Broadcast(ctx context.Context, signals []Signal) error
    Listen(ctx context.Context, deliver func(Signal)) error // blocks until ctx is done
}
func NewInProcessBroadcaster() Broadcaster

type Hub struct{ /* ... */ }
func NewHub(b Broadcaster, opts ...HubOption) (*Hub, error)
func (h *Hub) Run(ctx context.Context) error   // host-driven: calls b.Listen; Running() reports it
func (h *Hub) Running() bool
// HubOption: WithHeartbeat (DefaultHeartbeat = 25s), WithWriteTimeout (DefaultWriteTimeout = 10s),
//            WithMaxStreamsPerRecipient (DefaultMaxStreamsPerRecipient = 8)

// Public subscription (R1, decision 13): every transport, SSE included, subscribes through it,
// so the per-recipient cap counts SSE streams and WebSocket connections together.
func (h *Hub) Subscribe(recipient string) (*Subscription, error) // ErrUnavailable if !Running(); ErrTooManyStreams over the cap
func (s *Subscription) Ready() <-chan struct{}                    // capacity 1: a signal is pending
func (s *Subscription) Take() (Signal, bool)                       // the latest pending signal; clears it
func (s *Subscription) Close()                                     // releases the cap slot; idempotent
func (h *Hub) Heartbeat() time.Duration
func (h *Hub) WriteTimeout() time.Duration

// Signal codec (R3, decision 13): the wire format cross-instance broadcasters share.
const SignalFormatVersion = 1
var ErrUnknownSignalFormat error
func EncodeSignals(signals []Signal) ([]byte, error) // {"v":1,"signals":[{"recipient","change","at"}]}
func DecodeSignals(data []byte) ([]Signal, error)    // a v other than 1 matches ErrUnknownSignalFormat

type SubscriptionAuthorizer interface { AuthorizeSubscription(ctx context.Context, actor, recipient string) error }
type SubscriptionAuthorizerFunc func(ctx context.Context, actor, recipient string) error
var SelfOnly SubscriptionAuthorizer   // default: actor == recipient and actor != ""
var AllowAll SubscriptionAuthorizer   // explicit opt-out
```

- **Hub concurrency:**
  - The hub keeps `map[recipient]map[*stream]struct{}` under a `sync.RWMutex`.
  - Each stream has a `chan struct{}` with capacity 1. Delivery does a non-blocking send, so pending signals coalesce and a publisher never waits.
  - The stream's own request goroutine (the HTTP server's) loops over `select` on its channel, the heartbeat ticker and `r.Context().Done()`.
  - Writes are wrapped in `http.ResponseController.SetWriteDeadline(now+writeTimeout)`, and a failed write ends the stream.
  - The hub starts no goroutine except inside `Run`.
  - `goleak` verifies that `Run` and every stream exit on cancellation.
- **SSE wire format:**
  - headers `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`;
  - the connection opens with an initial `: connected` comment;
  - a signal is `event: unread-changed` and `data: {"change":"created","at":"..."}`;
  - a heartbeat is the comment line `: heartbeat`;
  - the stream flushes after every write.
- **Refusals:**
  - `503 unavailable` when `!hub.Running()`;
  - `429` beyond the per-recipient stream cap on this instance;
  - `403` when the authorizer refuses.
- **The stream's recipient** is the acting user by default. The optional `?recipient=` parameter exists only for host policies such as supervisors, and `SelfOnly` refuses any other value.
- **Rejected alternative:** a per-connection queue of N signals with drop-oldest. Signals carry no content, so one pending flag conveys everything a queue would, with no memory growth.

### 11. HTTP handlers

```go
func NewHandler(svc *Service, hub *Hub, opts ...HandlerOption) (*Handler, error) // implements http.Handler
// HandlerOption: WithActor(func(*http.Request) (string, error)) REQUIRED -> ConfigurationError if absent;
//                WithBasePath (DefaultBasePath = "/v1"); WithSubscriptionAuthorizer (default SelfOnly; nil -> ConfigurationError)
func (h *Handler) Routes() []Route // {Method, Pattern, Handler http.Handler} for routers that register per route
```

- **Routes** use Go 1.22 `ServeMux` patterns: `GET {base}/notifications`, `GET {base}/notifications/count`, `POST {base}/notifications/{id}/read`, `POST {base}/notifications/read-all` (body `{"through": RFC3339}` optional), `GET {base}/notifications/stream`.
- **Errors:**
  - bodies are `{"error":{"code","message","issues"?}}`, the same shape and code vocabulary as `transport/core` (`validation`, `forbidden`, `not_found`, plus `too_many_streams`, `unavailable`, `internal`);
  - the shape is copied, not imported, because of the split rule;
  - 500 bodies carry a generic message;
  - the mapping is exported as `notify.WriteError(w http.ResponseWriter, err error)` (R2, decision 13), which every handler here uses and which other transports (`notify/websocket`) call instead of copying it.
- **Mounting:**
  - stdlib: `mux.Handle("/v1/notifications", h)`;
  - Gin: `r.Any("/v1/notifications/*path", gin.WrapH(h))`;
  - Fiber: `adaptor.HTTPHandler(h)`, a stated limit. SSE through the Fiber adaptor must be verified by an example test, and is documented as unsupported if it buffers.
- **Rejected alternative:** reusing `transportcore`'s DTO seam. It imports hmntsk and cannot stream.

### 12. Split plan (P2)

- `notify/**` never imports hmntsk. Docs, DDL and tests live under `notify/`.
- Release order: `sqlkit` → `notify` → `notify/notifytest` → `notify/sqlstore`.
- Before the first `notify` tag, the tree moves to its own repository with `git filter-repo --path notify/ --path-rename notify/:`.
- The module paths change once, before any consumer exists, and hmntsk-side consumers (`tasknotify`) update their imports in the same step.
- **Release gate:** as with `sqlkit`, no `notify` module is tagged from this repository. hmntsk's first release that includes `tasknotify` is gated on `notify` (and `sqlkit`) having moved out.

### 13. APIs added for the dependent changes

Five additions came from the changes that build on `notify-core`. They are generic, so they belong here rather than being copied into each consumer.

| # | Addition | Needed by | Default and override |
|---|---|---|---|
| A1 | `CloseRequest.Successor`/`SuccessorSkip`, `CloseResult.Successors`/`SuccessorsSuppressed`; `Store.Close` takes the `IDGenerator` | `tasknotify` (claim tells the other candidates "taken") | No successor unless set. Only the store can close and publish atomically, so a two-call version would lose successors when a retry finds nothing left to close. |
| A2 | `Draft.Coalesce`; `PublishResult.Coalesced`, `InsertResult.Coalesced` | `tasknotify` (widening offers only to actors without one) | Off per draft. Decided inside the subject's serialised write. |
| R1 | `Hub.Subscribe` → `Subscription{Ready, Take, Close}`; `Hub.Heartbeat`, `Hub.WriteTimeout`; SSE built on it | `notify-realtime-adapters` (WebSocket) | One per-recipient cap across every transport. |
| R2 | `notify.WriteError(w, err)` | `notify-realtime-adapters` | One error-to-status mapping for every transport. |
| R3 | `EncodeSignals`, `DecodeSignals`, `SignalFormatVersion = 1`, `ErrUnknownSignalFormat` | `notify-realtime-adapters` (Redis, NATS) | One wire format, versioned; unknown versions are reported, not delivered. |

- **Why the coalescing and successor checks sit after the serialising upsert:** the same race argument as decision 6 applies. A check before the lock could see no open notification while a concurrent publish is inserting one.
- **Tests:** notifytest cases 3.5 (successors) and 3.6 (coalescing) run on every store; tasks 8.4, 8.5, 9.4 and 9.5 cover R1–R3.

## Risks / Trade-offs

- [Serialising upsert holds a row lock per subject for the length of a publish transaction] → Transactions contain only this subject's rows. Publish writes subjects in sorted order to avoid deadlocks. The concurrency conformance case runs on every dialect.
- [MySQL gap locks on `(subject, kind, state)` during close under REPEATABLE READ] → Close only updates by an indexed equality and a version range. Document READ COMMITTED as recommended, and test under MySQL's default isolation.
- [Default `EvictOldestActive` silently removes unread notifications for users over 500] → `PruneResult.EvictedActive` plus a signal. The docs recommend `RetainActive` for hosts whose notifications carry obligations.
- [SSE behind proxies: buffering and idle timeouts] → `X-Accel-Buffering: no`, a 25s heartbeat and a documented proxy checklist. WebSocket arrives in `notify-realtime-adapters`.
- [In-process broadcaster in a multi-instance deployment misses signals] → Documented limit. The store stays correct, and clients re-read on reconnect.
- [Watermark expiry lets a very late redelivery recreate notifications] → A 7-day default, far beyond the relay's retry horizon, configurable and documented.
- [A test-only driver dependency in `notify/sqlstore/go.mod`] → Pruned from consumers' module graphs, and documented.
- [Copying the ID generator and error shape duplicates hmntsk code] → Accepted as the cost of the split. Both are small and stable.

## Migration Plan

- The change is purely additive: new modules and new tables. There is no data migration.
- Hosts apply `notify/sqlstore/ddl/<dialect>.sql` through their own pipeline and call `VerifySchema` at startup.
- **Rollback:** stop running the pruner, hub and handlers, then drop the two tables. Nothing in hmntsk depends on them.

## Resolved open questions

- **Whether Fiber's adaptor streams SSE without buffering.** Decided: settled during apply by the example test in task 9.6. The outcome changes documentation only: if it buffers, `notify/docs/notifications.md` states SSE is unsupported through Fiber's adaptor.
- **A dedicated conflict-ignoring insert in `sqlkit`.** Decided: keep the `UpsertSuffix` no-op self-assignment (`id = id`) for now. A dedicated `sqlkit` statement is a follow-up change; it alters rendered SQL only, not behaviour, and `notify-email` uses the same workaround.
