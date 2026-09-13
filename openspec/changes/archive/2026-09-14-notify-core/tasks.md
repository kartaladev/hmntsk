Conventions for every task:
- **TDD, in this order:**
  - Red: write the test, and run `GOTOOLCHAIN=go1.26.8 go test -run '<Name>' -count=1 ./...` in the module to watch it fail for the intended reason.
  - Green: the smallest change that passes.
  - Refactor.
- **Tests:** table tests follow the `table-test` skill (`assert` closures, `t.Context()`). Port doubles come from `use-mockgen` (`//go:generate ... -typed`). Databases come from `use-testcontainers`, through one `RunTestX` helper per module in `testutils.go`.
- **Tooling:** navigate with gopls. Never run `go mod tidy` or `make tidy`.

## 1. Module scaffolding

- [x] 1.1 Create the `notify`, `notify/notifytest` and `notify/sqlstore` modules with `go.mod` and `doc.go`, add them to `go.work` and `NOTIFY_MODULES` in the `Makefile`, and verify `GOTOOLCHAIN=go1.26.8 go build ./...` succeeds in each
- [x] 1.2 Verify `make split-check` passes with the new modules and fails on a temporary import of `github.com/kartaladev/hmntsk` from `notify` (revert it afterwards)

## 2. Core types, errors, clock and IDs (`notify`)

- [x] 2.1 Test-first, write `errors.go`:
  - the `ErrNotFound`, `ErrValidation`, `ErrUnauthorized`, `ErrConfiguration`, `ErrTooManyStreams` and `ErrUnavailable` sentinels;
  - `ConfigurationError`, and `ValidationError` with its issues.
  - Verify with a table test of `errors.Is` matching.
- [x] 2.2 Test-first, write `Clock`, `SystemClock` and `ClockFunc`, and a UUIDv7 `IDGenerator` with monotonic ordering. Verify with ordering and uniqueness tests.
- [x] 2.3 Test-first, write `State`, `Notification`, `Draft` and `Draft.validate`. Verify with a validation table test covering each required field, the length limits, negative versions and invalid JSON data.

## 3. Store contract and conformance suite

- [x] 3.1 Define the `Store` interface and its request/result types, and `Page` with an opaque keyset cursor codec. Verify that the cursor round-trips, and that a cursor under another recipient or filter set is refused, with a table test.
- [x] 3.2 In `notify/notifytest`, write `Run(t, factory)` cases for the notification-inbox spec. Verify the suite compiles and fails against a stub store that does nothing. The cases:
  - states;
  - opaque round-trip, including byte-exact data;
  - idempotent publish;
  - a redelivery does not undo a read;
  - close by kind, by version and with `Except`;
  - READ→CLOSED keeps `ReadAt`;
  - watermark suppression, strict-less versus close-less-or-equal;
  - close of every kind (`*`);
  - list ordering and exact paging under concurrent inserts;
  - count of ACTIVE only;
  - mark read and mark all read up to an instant;
  - another recipient's notification is not found.
- [x] 3.3 Add the notifytest case "concurrent close and late publish never leave ACTIVE below the watermark". It runs close and publish on separate goroutines through the store, 200 iterations. Verify it fails against a deliberately non-serialising stub.
- [x] 3.4 Add the notifytest retention cases from the notification-retention spec, driven through `Store.Prune`. Verify they fail against the stub. The cases:
  - age from `InactiveAt` only;
  - ACTIVE is never deleted for age;
  - inactive-first count eviction;
  - `EvictOldestActive` versus `RetainActive`;
  - result counters and recipients;
  - watermark expiry only for empty subjects past retention.

- [x] 3.5 Add the notifytest cases for close with successors (amendment A1, design decision 13). Verify they fail against the stub. The cases:
  - successors are created for every recipient the close moved to CLOSED;
  - `SuccessorSkip` recipients get none;
  - a successor below a newer watermark is suppressed and counted;
  - a retried close creates no further successors, and the first attempt's successors remain;
  - `CloseResult.Successors` lists exactly the created notifications.
- [x] 3.6 Add the notifytest cases for coalescing drafts (amendment A2, design decision 13). Verify they fail against the stub. The cases:
  - an ACTIVE notification of the same kind and subject coalesces;
  - a READ one coalesces;
  - a CLOSED one does not;
  - a non-coalescing draft is unaffected;
  - `Coalesced` counts in `PublishResult`/`InsertResult`;
  - two concurrent coalescing publishes for one recipient create exactly one notification (200 iterations).

## 4. In-memory store (`notify`)

- [x] 4.1 Implement `NewMemoryStore()` against `notifytest.Run`, one failing case at a time, serialising per subject with a striped mutex. Verify all notifytest cases pass, with `-race`.

## 5. SQL store (`notify/sqlstore`)

- [x] 5.1 Test-first, write `sqlstore.New(executor sqlkit.Executor, opts...)`: a nil executor is a `ConfigurationError`, the dialect is taken from `executor.Dialect()`, and `WithTablePrefix` applies. Verify with a table test.
- [x] 5.2 Write the DDL documents for PostgreSQL, MySQL and SQLite under `ddl/`, using sqlkit's `PrefixToken`: the tables and indexes from design §7, with the dialect's identifier collation, JSON and timestamp types. Expose `Schema()` via `sqlkit.RenderSchema` and `Migrate(ctx)` via `sqlkit.ApplySchema`. Verify with a golden test per dialect and prefix, and `Migrate` on each container.
- [x] 5.3 Test-first, write `VerifySchema(ctx)` as `sqlkit.VerifySchema` with the store's `SchemaExpectation`/`TableExpectation{Columns, IdentifierColumns, Indexes}`. Verify with a table test that drops each object in turn on every dialect, and checks that the error matches `sqlkit.ErrSchemaMismatch` and lists every issue.
- [x] 5.4 In `testutils.go`, build the 7 executor factories from `sqlkit/sqlkittest`'s `RunTestPostgres`, `RunTestMySQL` and `RunTestSQLite` (reused, not rewritten): `stdsqlexec.New` × 3 dialects, `pgxexec.New` × PostgreSQL, `gormexec.New` × 3 dialects. Add `TestStoreOn<Driver><Dialect>` entry points calling `notifytest.Run`, and verify they compile and fail.
- [x] 5.5 Implement `Insert`: the serialising upsert of (subject,`*`), the watermark read, suppression, coalescing (A2), the conflict-ignoring insert on (source_id, recipient), and created-versus-duplicate detection. Verify the publish, watermark and coalescing notifytest cases pass on all 7 combinations.
- [x] 5.6 Implement `Close`: the serialising upsert, the kind or `*` watermark raise, the recipient selection, the conditional update, and successors (A1) inserted in the same transaction through the insert path. Verify the close, successor and concurrency notifytest cases pass on all 7 combinations, including MySQL at its default isolation.
- [x] 5.7 Implement `Get`, `List` (keyset over created_at and id, descending), `CountActive`, `MarkRead` and `MarkAllRead`. Verify the read-side notifytest cases pass on all 7 combinations.
- [x] 5.8 Implement `Prune` with portable select-IDs-then-delete-`IN` batches for age, count and watermarks. Verify the retention notifytest cases pass on all 7 combinations.
- [x] 5.9 Add a `NOTIFY_STORE_MATRIX` Makefile target mirroring `store-matrix`, and verify it runs all 7 entry points green.

## 6. Service (`notify`)

- [x] 6.1 Test-first, write `New`: a nil store is a `ConfigurationError`, and the defaults are an in-process broadcaster, the system clock and the UUIDv7 generator. Verify with a table test of the wiring errors and defaults.
- [x] 6.2 Test-first, write `Publish`:
  - validate drafts;
  - group them by subject in sorted order;
  - stamp ID, `CreatedAt` and ACTIVE;
  - call `Store.Insert`;
  - aggregate results;
  - broadcast `created` for created recipients only after the write.
  - Verify with mockgen doubles of `Store` and `Broadcaster`: a broadcaster error goes to the signal error handler and does not fail publish.
- [x] 6.3 Test-first, write `Close`, `MarkRead`, `MarkAllRead`, `Get`, `List` and `CountActive`, each broadcasting its change for the affected recipients. `Close` validates `Successor` like a draft, passes the service's `IDGenerator` to `Store.Close`, and broadcasts `created` for each successor as well as `closed` for each closed recipient; `Publish` reports `Coalesced`. Verify with mock expectations and a memory-store integration test.

## 7. Pruner (`notify`)

- [x] 7.1 Test-first, write `NewPruner` and its options. Verify with a table test:
  - defaults of 500, 90 days, `EvictOldestActive`, 7 days and 1000;
  - configuration errors for ≤0 bounds, an unknown strategy, `With`+`Without` on the same bound, both bounds removed and batch ≤0.
- [x] 7.2 Test-first, write `Prune`, which passes the request to `Store.Prune` and broadcasts `pruned` for recipients with evicted ACTIVE notifications, and `Run(ctx, interval)`, which returns on cancel and reports pass errors to the handler. Verify with a fake clock, the memory store, and `goleak.VerifyNone` after cancellation.

## 8. Realtime (`notify`)

- [x] 8.1 Test-first, write `NewInProcessBroadcaster`, where `Broadcast` delivers to every active `Listen`, and `Listen` blocks until ctx is done. Verify with delivery and cancellation tests under `-race` and goleak.
- [x] 8.2 Test-first, write `SubscriptionAuthorizer`, `SubscriptionAuthorizerFunc`, `SelfOnly` and `AllowAll`. Verify with a table test: own recipient permitted, other recipient refused, empty actor refused, and `AllowAll` permits everything.
- [x] 8.3 Test-first, write `Hub`: `NewHub` options and defaults (25s, 10s, 8), `Run`/`Running`, per-recipient registration, and capacity-1 coalescing delivery. Verify that 1,000 signals to a non-reading stream never block and leave one pending signal, that `Run` exits on cancel, and that goleak is clean.
- [x] 8.4 Test-first, write the public subscription (design decision 13, R1): `Hub.Subscribe(recipient) (*Subscription, error)`, `Subscription.Ready`, `Take`, `Close`, and `Hub.Heartbeat()`/`Hub.WriteTimeout()`. Verify with a table test under `-race` and `goleak.VerifyNone`:
  - `ErrUnavailable` when the hub is not running;
  - `ErrTooManyStreams` over the per-recipient cap;
  - `Take` returns the latest of 1,000 coalesced signals and clears it;
  - `Close` releases the cap slot and is idempotent.
- [x] 8.5 Test-first, write the signal codec (design decision 13, R3): `EncodeSignals`, `DecodeSignals`, `SignalFormatVersion = 1`, `ErrUnknownSignalFormat`. Verify with a golden file `testdata/signals.v1.json` and a table test:
  - a round trip of recipient, change and microsecond UTC time;
  - a `v` other than 1 matches `ErrUnknownSignalFormat`;
  - malformed JSON is an error;
  - the encoding contains no title, links, data, kind or subject.

## 9. HTTP handlers (`notify`)

- [x] 9.1 Test-first, write `NewHandler`: a missing `WithActor` and a nil authorizer are `ConfigurationError`s, the base path defaults to `/v1`, and `Routes()` is stable. Verify with a table test.
- [x] 9.2 Test-first, write list and count with filters, limit default and cap, and cursor. Verify with `httptest` table cases for the notification-http-api scenarios: only the caller's notifications, a bad cursor gives 400, and no actor gives 403.
- [x] 9.3 Test-first, write mark read (another recipient's or an unknown ID gives the same 404) and read-all with an optional `through`. Verify with `httptest` table cases, including byte-identical 404 bodies.
- [x] 9.4 Test-first, write the SSE stream on top of `Hub.Subscribe` (design decision 13, R1), so its streams count against the same per-recipient cap as any other transport:
  - headers and the initial comment;
  - `unread-changed` events;
  - heartbeats;
  - the write timeout closes a stalled client;
  - 429 over the cap, 503 when the hub is not running, 403 on a policy refusal;
  - a host policy permitting a supervisor.
  - Verify with a streaming `httptest` server, a fake heartbeat interval, `-race` and goleak.
- [x] 9.5 Test-first, write the exported `notify.WriteError(w, err)` (design decision 13, R2), used by every handler and by other transports: add error-body tests asserting the `{"error":{"code","message"}}` shape, the sentinel-to-status mapping (400, 403, 404, 429, 503, 500), and that 500 bodies carry no internal detail. Verify they pass.
- [x] 9.6 Add example tests mounting the handler on a stdlib mux, and document the Gin and Fiber mounting. Verify that the stdlib example runs, and record whether SSE streams through the Fiber adaptor in `notify/docs/notifications.md`.

## 10. Documentation

- [x] 10.1 Write `notify/docs/notifications.md`. Verify with a docs test that the documented constant values match the code, as `docs_test.go` does in other modules. It covers:
  - the model;
  - publishing and closing semantics with the watermark;
  - each default and its override;
  - retention defaults and strategies, and the approximate-bound limit;
  - the single-instance broadcaster limit;
  - reconnect-and-re-read;
  - the proxy checklist;
  - no host transaction joining;
  - mounting.
- [x] 10.2 Write `notify/docs/schema.md`, with the DDL per dialect, indexes, `WithTablePrefix` and `VerifySchema`, and godoc on every exported identifier naming the default it replaces. Verify that `make lint` reports no missing doc comments.

## 11. Verification

- [x] 11.1 Run `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln` and the notify store matrix. Verify all pass, and `make split-check` confirms `notify/**` imports no hmntsk package.
- [x] 11.2 Run `/simplify` on the new modules, re-run the full check, and verify it is still green.
