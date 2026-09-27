Conventions for every task:
- **Evidence:** `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`. Before porting, re-run a scratch test with `cd $V/notify && go test -count=1 -run '<Name>' .` and confirm it still fails.
- **TDD, in this order, for every defect:**
  - Port: move the scratch reproduction into the repo's shared suite.
  - Red: run it with `go test -run '<Name>' -count=1 ./...` in the module, and watch it fail for the reason the audit states (not a compile error or missing fixture).
  - Green: make the smallest fix.
  - Refactor: consider `/simplify`, then re-run.
- **Tests:** table tests follow the `table-test` skill (`assert` closures, `ctx` modifier where context matters, `t.Context()`). Doubles come from `use-mockgen`. Databases come from `use-testcontainers` through `sqlkit/sqlkittest` and the module's existing harness, never hand-rolled fakes. Shared-suite cases go in `notify/notifytest` (`RunEmail` for store behaviour, `RunEmailDispatch` for dispatcher behaviour), so that the memory store (`notify/memory_email_test.go`) and every SQL driver and dialect combination (`notify/sqlstore/harness_test.go`, `internal/gormtest`) run them.
- **Tooling:** invoke `/golang-how-to` first. Navigate with gopls (`$(go env GOPATH)/bin/gopls`). Never run `go mod tidy`.

## 1. N4 — a send starts only under a live, renewed lease (store fence)

- [ ] 1.1 Red: add `RunEmail` "leases" cases derived from the scratch test `TestEmailLeaseLapseMidPass` (`$V/notify/email_test.go`), at store level. Cover four cases: a `SENDING` record whose `At` is past the owner's `lease_until` changes 0 rows; a `SENDING` record with `Lease: 5m` renews the lease, so another owner claiming 4 minutes later gets nothing; a `SENDING` record over two IDs of which the owner holds one changes neither; and a `SENT` record from the owner after its lease lapsed, with no takeover, still changes the row. Verify that `go test -run 'TestMemoryStoreEmailConformance' -count=1 ./...` in `notify` and `make notify-store-matrix` fail on the first three cases with "expected 0 changed, got N" or "claimed by other owner".
- [ ] 1.2 Green: add `EmailRecord.Lease` (godoc: `SENDING` only, must be positive). In `MemoryStore.RecordEmails`, and in `sqlstore.Store.RecordEmails` inside its existing transaction, make `SENDING` all-or-nothing, fenced on `owner` and `lease_until > At`, and set `lease_until = At + Lease`. Leave other statuses owner-fenced. Verify that the 1.1 cases pass on memory and on all combinations (`make notify-store-matrix`).
- [ ] 1.3 Refactor the fence into one helper per store, update the `EmailStore.RecordEmails` and `EmailRecord` godoc to state the fence, and re-run 1.1.

## 2. N4 — the dispatcher uses a fresh clock, renews on SENDING and releases on refusal

- [ ] 2.1 Port the scratch test `TestEmailLeaseLapseMidPass` (both cases: mailer accepts, and mailer fails transiently) into `notifytest.RunEmailDispatch`, keeping its deterministic hooks: the clock advances 2× the lease inside alice's send, and dispatcher B runs inline. Restate its assertions for the fixed behaviour: A does not hand bob to the mailer; after A's pass, B's pass sends bob exactly once; bob ends `SENT` (or `RETRY` for the transient case) and never `ABANDONED`; and alice (accepted during an overrun with no takeover) ends `SENT`. Red: verify it fails on memory and on SQLite and PostgreSQL through `make notify-store-matrix` with "bob recorded ABANDONED".
- [ ] 2.2 Green: in `emailPass.record`, stamp `record.At` with `service.clock.Now()` on every call. Pass `Lease: d.lease` with `SENDING`. When the `SENDING` record is refused, record `CLAIMED` for the message's IDs, without `Attempt`, and do not call the `Mailer`. Verify that 2.1 passes everywhere and that the existing `RunEmailDispatch` cases still pass.
- [ ] 2.3 Add a default-and-override case: the default lease (5m) with a sender taking 1m is never taken over, and `WithEmailLease(30*time.Minute)` with a sender taking 10m records every message `SENT` under two concurrent dispatchers. Verify it passes. Update the `WithEmailLease` godoc to "must outlast one send", and document the overrun limit in `notify/docs/email.md`.
- [ ] 2.4 Refactor (consider `/simplify` on `email_dispatcher.go`) and re-run the `notify` tests.

## 3. N5a — at-least-once repeats are bounded

- [ ] 3.1 Port the scratch test `TestAtLeastOnceInDoubtResendsAreBounded` (`$V/notify/email_test.go`; cases "attempt limit of 3" and "maximum lag of 5 minutes") into `notifytest.RunEmailDispatch`. Add a default case with no attempt limit set, expecting at most 5 sends. Red: verify it fails with "handed to the mailer 12 times" and "sent N times after its lag expired".
- [ ] 3.2 Green: in `resolveDoubt` under `AtLeastOnce`, record `FAILED` when `Attempts >= d.attempts`, and record `ABANDONED` when the oldest candidate is older than `now - maxLag`. Both are reported to the error handler and counted in `DispatchResult`. Verify that 3.1 passes on memory and all combinations, and assert the final statuses (`FAILED`, `ABANDONED`).
- [ ] 3.3 Refactor, update the `AtLeastOnce`, `WithEmailMaxAttempts` and `WithEmailMaxLag` godoc to say they bound repeats, and re-run.

## 4. N5b — a repeat covers exactly the original notifications

- [ ] 4.1 Port the scratch test `TestAtLeastOnceResendCoversExactlyTheOriginalNotifications` (`$V/notify/email_test.go`) into `notifytest.RunEmailDispatch`. Red: verify it fails with "must cover exactly the original notifications" on memory and all combinations.
- [ ] 4.2 Red: add `RunEmail` cases for the schema and claim: a `SENDING` record's `BatchSize` round-trips on `EmailCandidate.BatchSize`; a claim with `Limit: 2` over a lapsed 3-notification in-doubt message returns all 3; and two concurrent claimers over that message give all 3 to exactly one of them (200 iterations). There is no scratch reproduction for this, so the red run is the evidence. Verify the cases fail.
- [ ] 4.3 Green, store side: add `batch_size` to `ddl/email/{postgres,sqlite,mysql}.sql` and to the email `SchemaExpectation`, and update the golden DDL tests. Add `EmailRecord.BatchSize` and `EmailCandidate.BatchSize` to the memory store and to `sqlstore`. Extend `ClaimEmails` to take whole in-doubt messages. Verify that 4.2 passes on memory and all combinations, and that the `VerifyEmailSchema` tests still pass.
- [ ] 4.4 Red: add a `RunEmailDispatch` case for the irreproducible message. An in-doubt 2-notification message, one of whose notifications is deleted (and purged), is resent as a new message under a new key covering only the survivor, and the deleted one is recorded `SKIPPED` `deleted`. Verify it fails.
- [ ] 4.5 Green, dispatcher side: an in-doubt resend skips the state recheck, re-renders the exact original set under the original key, and falls back to the fresh-send path when `Get` returns `ErrNotFound` or the group is smaller than `BatchSize`. `SENDING` writes `BatchSize`. Verify that 4.1 and 4.4 pass everywhere, together with the existing "resend does not absorb newer notifications" case.
- [ ] 4.6 Refactor, and update the `AtLeastOnce` godoc and `notify/docs/email.md` (the exact-resend rule, the new-key fallback and its duplicate risk, the claim limit overshoot on `EmailClaim.Limit`). Re-run.

## 5. N8 — generated identifiers are bounded on every store

- [ ] 5.1 Port the scratch test `TestLongGeneratedIDsPublishOnEveryDialect` (`$V/notify/sqlstore_test.go`) into `notify/sqlstore`'s harness, so it runs on SQLite, PostgreSQL and MySQL. Restate the assertion as: a 100-byte generator is refused at `notify.New` with `ErrConfiguration` naming 64 bytes on every dialect; a 64-byte generator publishes and reads back on every dialect; and a generator that is valid at construction but mints 100 bytes on publish fails the publish with nothing stored. Red: verify that `make notify-store-matrix` fails (construction succeeds, and MySQL returns `Error 1406`).
- [ ] 5.2 Green: add `MaxIDBytes = 64` next to `MaxIdentifierBytes`. Wrap `Service.ids` in a checking generator. Probe it once in `notify.New`. Route `Publish`, successor insertions, the dispatcher owner and batch keys through it. Update the `ConfigurationError` and `WithIDGenerator` godoc. Verify that 5.1 passes on every dialect, and that a memory-store table test in `notify` covers the default (UUIDv7) and the two host generators.
- [ ] 5.3 Refactor and re-run.

## 6. Checks

- [ ] 6.1 Update `notify/docs/email.md` and `docs/schema.md` for the lease fence, repeat bounds, exact resends and `batch_size`, and `notify/docs/notifications.md` for `MaxIDBytes`. Verify that the doc tests (`notify/docs_test.go`) pass.
- [ ] 6.2 Re-run each scratch test against the fixed code through the scratch `go.work` (`cd $V/notify && go test -count=1 -run 'TestEmailLeaseLapseMidPass|TestAtLeastOnce|TestLongGeneratedIDs' .`). Verify the ones whose assertions are unchanged now pass. `TestEmailLeaseLapseMidPass` is superseded by its restated port.
- [ ] 6.3 Run `make lint`, `make test` and `make notify-store-matrix`, and verify all pass. Also run `make split-check` to confirm that `notify/**` still imports nothing outside notify and sqlkit.
