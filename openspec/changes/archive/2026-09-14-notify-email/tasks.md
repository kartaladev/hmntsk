Conventions for every task:
- **Order:** apply only after `notify-core` (and therefore `sqlkit`) has landed; this change adds files to the `notify`, `notify/sqlstore` and `notify/notifytest` modules.
- **TDD, in this order:**
  - Red: write the test, and run `GOTOOLCHAIN=go1.26.8 go test -run '<Name>' -count=1 ./...` in the module to watch it fail for the intended reason (not a compile error or missing fixture).
  - Green: the smallest change that passes.
  - Refactor.
- **Tests:** table tests follow the `table-test` skill (`assert` closures, `ctx` modifier where context matters, `t.Context()`). Port doubles come from `use-mockgen` (`//go:generate ... -typed`, placed by consumer, never in the production build). Databases come from `use-testcontainers`, reusing `sqlkit/sqlkittest`'s `RunTestPostgres`, `RunTestMySQL` and `RunTestSQLite` through the module's existing `testutils.go`.
- **Tooling:** navigate with gopls (`/Users/zakyalvan/go/bin/gopls`). Never run `go mod tidy` or `make tidy`.

## 1. Contract: types, errors, ports and options (`notify`)

- [x] 1.1 Test-first, add `ErrMailRejected` and `ErrMailInDoubt`, the `EmailStatus` constants and the skip-reason constants. Verify with a table test of `errors.Is` on wrapped sentinels.
- [x] 1.2 Define `EmailStore`, `EmailClaim`, `EmailCandidate` and `EmailRecord`; `AddressBook`, `Mailer`, `EmailTemplate` and `EmailFilter` with their `Func` adapters, `EmailBatch`, `EmailContent` and `EmailMessage`. Add `//go:generate` typed mocks for the four host ports and `EmailStore`. Verify `go generate` produces the mocks and `go build ./...` succeeds.
- [x] 1.3 Test-first, write `EmailKinds(kinds...)`. Verify with a table test: listed kind accepted, other kind rejected, empty kinds refused at construction.
- [x] 1.4 Test-first, write `NewEmailDispatcher` and every `EmailOption` from design §4. Verify with a table test that the defaults are applied and that each configuration error is refused: nil mailer, address book or template; a store without `EmailStore`; non-positive grace, lag, batch, claim limit, lease, attempts or backoff; lag ≤ grace; ceiling < base; nil or empty filter; filter and kinds together; `With` and `Without` grace together; unknown guarantee; empty owner; nil error handler.

## 2. Email conformance suite (`notify/notifytest`)

- [x] 2.1 Write `RunEmail(t, factory)` cases for claim eligibility (ACTIVE only; created within grace and lag; RETRY not before `next_attempt_at`; SENT, SKIPPED, FAILED and ABANDONED never again), lease expiry and takeover, SENDING surviving takeover with its batch ID, `RecordEmails` affecting zero rows for a lost lease, and orphan purge. Verify the suite compiles and fails against a stub `EmailStore` that does nothing.
- [x] 2.2 Add the case "two owners claiming concurrently never both receive a notification": two goroutines, 200 iterations, asserting disjoint claims and full coverage. Verify it fails against a deliberately non-exclusive stub.

## 3. In-memory email store (`notify`)

- [x] 3.1 Implement `EmailStore` on `NewMemoryStore()` against `notifytest.RunEmail`, one failing case at a time. Verify all `RunEmail` cases pass with `-race`, and the existing `notifytest.Run` cases still pass.

## 4. SQL email store (`notify/sqlstore`)

- [x] 4.1 Write the `notify_email_deliveries` DDL documents for PostgreSQL, MySQL and SQLite under `ddl/email/`, with `PrefixToken`, identifier collation on `recipient` and `owner`, and the two indexes. Expose `EmailSchema()` and `MigrateEmail(ctx)`. Verify with a golden test per dialect and prefix, and `MigrateEmail` on each container.
- [x] 4.2 Test-first, write `VerifyEmailSchema(ctx)` with an email-only `SchemaExpectation`. Verify with a table test that drops each email object in turn on every dialect and checks the error matches `sqlkit.ErrSchemaMismatch` and lists every issue; and that `VerifySchema(ctx)` still succeeds when the email table is absent.
- [x] 4.3 Add `TestEmailStoreOn<Driver><Dialect>` entry points calling `notifytest.RunEmail` on the 7 executor factories already in `testutils.go`. Verify they compile and fail.
- [x] 4.4 Implement `ClaimEmails` (select, conflict-ignoring insert, conditional takeover, select-back by owner and lease). Verify the claim and concurrency `RunEmail` cases pass on all 7 combinations, including MySQL at its default isolation.
- [x] 4.5 Implement `RecordEmails` (owner- and lease-conditional update) and `PurgeEmailRecords` (select orphan IDs, delete by ID, batched). Verify all `RunEmail` cases pass on all 7 combinations.

## 5. Dispatcher pass (`notify`)

- [x] 5.1 Test-first, with mocked ports and the memory store, write grouping and batching: one message per recipient, at most `BatchLimit` notifications in `CreatedAt` order, remainder left for a later pass. Verify with a table test.
- [x] 5.2 Test-first, write the filter and address steps: filtered → SKIPPED `filtered`; no address → SKIPPED `no_address` with no error reported; filter or address-book error → RETRY with backoff and the error reported. Verify with a table test using the `ctx` modifier.
- [x] 5.3 Test-first, write the recheck: notifications read, closed or deleted after claiming are SKIPPED `inactive` or `deleted`, and a batch left empty sends nothing. Verify with a table test that reads, closes and prunes between claim and send.
- [x] 5.4 Test-first, write the send outcomes: SENDING with a fresh batch ID committed before `Send`; nil → SENT; `ErrMailRejected` → FAILED; other error → RETRY with jittered backoff, FAILED at the attempt limit; template error → FAILED. Verify with a table test asserting store state, `IdempotencyKey` and the error handler calls.
- [x] 5.5 Test-first, write in-doubt handling for both guarantees: expired SENDING and `ErrMailInDoubt` become ABANDONED under `AtMostOnce`, and are resent with the same batch ID over exactly the original notifications under `AtLeastOnce`, never merged with newer ones. Verify with a table test.
- [x] 5.6 Test-first, write `DispatchResult` counting and the purge step at the end of each pass. Verify with a table test of a mixed pass (the spec's counts scenario) and a pass after pruning.
- [x] 5.7 Test-first, write `Run(ctx, interval)`: a pass per interval, errors to the handler, returns `ctx.Err()` on cancel. Verify with a fake clock-driven test and `goleak`, and that constructing a dispatcher starts no goroutine.

## 6. Integration on every SQL combination

- [x] 6.1 Add an integration test running two dispatchers concurrently over the same notifications on each of the 7 combinations with a counting `Mailer`. Verify every notification is sent exactly once and no row is left CLAIMED or SENDING after leases lapse.
- [x] 6.2 Add crash-simulation tests on each combination: a `Mailer` that sends and then cancels the pass before recording. Verify `AtMostOnce` never resends and records ABANDONED, and `AtLeastOnce` resends once with the same idempotency key.
- [x] 6.3 Add a redeploy test: dispatch and record, construct a new dispatcher over the same database, dispatch again. Verify no second send.

## 7. Documentation and checks

- [x] 7.1 Write `notify/docs/email.md`: wiring (`NewEmailDispatcher`, `VerifyEmailSchema`, applying the email DDL), every default and its override, both delivery guarantees and their stated limits, the recheck window, skip finality, and preferences and quiet hours through `EmailFilter`. Verify the doc's Go snippets compile through an example test.
- [x] 7.2 Add a note to the notifications guide that email is optional and needs its own table. Verify links resolve.
- [x] 7.3 Verify `make split-check` passes (`notify/**` imports no hmntsk module; `notify` stays stdlib-only).
- [x] 7.4 Run `/simplify` on the files this change touched, re-run the affected tests, then run `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln` and verify it passes.
