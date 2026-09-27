Every defect is worked test-first:

1. **Port** the scratch reproduction into the repo next to the code it covers.
2. **Red:** run it and watch it fail for the reason stated.
3. **Green:** make the smallest change that turns it green.
4. **Refactor,** then consider `/simplify` and re-run.

**Scratch reproductions.** They live under `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`, and each file and test to port is named in its task. If the scratchpad is gone, rebuild the case from the scenario in `specs/event-relay/spec.md`, which gives the same arrangement and assertion.

**Test form.**
- Tables with two or more cases follow the `table-test` skill: `assert` closures, a `ctx` modifier where context matters, and `t.Context()`.
- Store-dependent cases go in the shared suites, so that every store runs them:
  - `relaytest` covers memstore, and `sql`, `pgx` and `gorm` on SQLite, PostgreSQL and MySQL;
  - `storetest` covers the store port.
- Containers come only from the existing `storetest` helpers (`use-testcontainers`), and hand-driven clocks come from the suite fixtures, with no sleeps.

**Tooling.**
- Navigate with gopls at `$(go env GOPATH)/bin/gopls`.
- Focused runs:
  - `relaytest` against memstore: `cd relaytest && go test -run 'TestSuiteAgainstTheInMemoryStore/<Group>' -count=1 ./...`
  - one SQL combination: `cd store/sql && go test -run 'TestRelayOnSQLite/<Group>' -count=1 ./...`

## 1. Scaffolding the lease on the port (no behaviour)

- [ ] 1.1 Scaffold the lease on the port, with no behaviour yet.
  - Add `hmntsk.OutboxLease{Owner, Until}`, a `Lease` field on `AttemptRecord`, `Acceptance` and `DeadLetter`, `LeaseRelease`, `ErrLeaseLost` (wrapping `ErrConflict`) and `*LeaseLostError`, with godoc stating the default and that there is no override (design D1).
  - Add a `ReleaseLeases` stub to `OutboxStore` that every store implements as "not yet implemented", so the red tests below compile and fail on behaviour, not on the build.
  - Fill `Lease` from the claimed entry in `relay.settle`.
  - Verify: `go build ./...` in every module of `go.work` passes, and `TestLeaseLostErrorMatchesConflict` (new, in `errors_test.go`) passes.

## 2. R1/S6: settlement fenced by the claiming lease

- [ ] 2.1 Red, store level: port `$V/stores/findings_test.go` `s6OutboxFencing` (run as `TestFindings/<dialect>/S6_outbox_settlement_not_fenced`) into `storetest/outbox.go` under `outboxSettlement`, as the table `settlement is fenced by the claiming lease`.
  - Cases: a stale `RecordAttempt`, a stale `MarkAccepted` and a stale `MarkDeadLettered`. Each asserts that the error matches `ErrLeaseLost` and `ErrConflict`, that relay-b's lease is intact with its attempt count unchanged, and that relay-c claims nothing.
  - Also add a case where a settlement against an expired but unclaimed lease lands, and a case where a zero `Lease` is refused.
  - Verify it fails on memstore (`cd storetest && go test -run 'TestSuiteAgainstTheInMemoryStore/Outbox/settlement' -count=1 ./...`) and on SQL (`make store-matrix`), with messages like "must not release relay-b's live lease".
- [ ] 2.2 Red, relay level: port `$V/relay/r1_fence_test.go` `TestR1StaleSettlementIsNotFenced` into a new `relaytest/fencing.go` as `runFencingCases`, wired into `RunSuite` as `t.Run("Fencing", ...)`.
  - Keep all four cases: the stale partial acceptance, the resurrected dead letter, the regressed attempt count, and the released live lease with relay C.
  - Add assertions that relay A's `Result.Unsettled == 1` and that its error handler saw an error matching `ErrLeaseLost`.
  - Verify it fails on memstore and on `TestRelayOnSQLite/Fencing`.
- [ ] 2.3 Green, SQL: in `store/sqlcore/query.go`, add the `locked_by = ? AND locked_until = ?` fence to the `RecordAttempt`, `MarkAccepted` and `MarkDeadLettered` builders.
  - Make `MarkAccepted` write `published_at` only when it is set (design D2).
  - Update `builder_test.go` and `outbox_test.go` expectations first, watch them fail, then change the builders.
  - Make `settleOutbox` in `store/sql`, `store/pgx` and `store/gorm` return `*LeaseLostError` when zero rows are affected and the row exists.
  - Verify 2.1 is green on `make store-matrix`.
- [ ] 2.4 Green, memstore: fence `RecordAttempt`, `MarkAccepted` and `MarkDeadLettered` in `memstore/outbox.go` on owner and deadline, and normalise `until` in `ClaimDueEvents`. Verify 2.1 is green on memstore.
- [ ] 2.5 Green, engine: in `relayport.go`, refuse a zero `Lease` in `RecordDeliveryAttempt`, `MarkEventAccepted` and `MarkEventDeadLettered` with a `ValidationError`, using a table test in `relayport_test.go` written first. Verify 2.2 is green on memstore and `make relay-matrix`, and that the existing `relaytest` groups stay green.
- [ ] 2.6 Refactor: reword the "recording twice is harmless" godoc on `Acceptance` and the store builders (design D1), then run `/simplify` on the touched files. Verify `make test`.

## 3. Releasing unattempted events

- [ ] 3.1 Red: add `storetest` table `releasing a lease`.
  - Cases:
    - the holder releases; the row becomes unleased with attempts, next attempt and last error unchanged;
    - a superseded lease releases nothing and returns nil;
    - an unknown event ID is a no-op.
  - Verify it fails against the stubs from 1.1.
- [ ] 3.2 Green: implement the `ReleaseLeases` builder in `sqlcore`, `settle` in the three SQL stores and memstore, and `Service.ReleaseEvents`. Verify 3.1 on memstore and `make store-matrix`.

## 4. R2: a pass stays within its lease

- [ ] 4.1 Red: port `$V/relay/r2_overrun_test.go` `TestR2PassRunsPastItsLease` into a new `relaytest/deadline.go` as `runLeaseDeadlineCases`, wired as `t.Run("LeaseDeadline", ...)`.
  - Keep the default lease and batch, and a slow sink that advances the suite clock by `webhook.DefaultTimeout` per delivery.
  - Assert that no event is delivered by both A and B, and that the events A did not reach are unleased after its pass and counted as `Released`.
  - Verify it fails ("21 events were delivered by both A and B").
- [ ] 4.2 Red: port `TestR2SinkContextBoundedByLease` from the same file into the same group.
  - Cases:
    - default: a one-minute lease gives the sink context a deadline no later than the lease deadline;
    - override: with `WithRelayLease(30*time.Minute)` the deadline is up to 30 minutes out.
  - Add a partial-offer case: two sinks, the deadline passes after the first sink accepts. Assert the second sink is not offered, and the event is a partial acceptance due at the settlement instant with no failure recorded for the second sink.
  - Verify the default case fails ("sink context carries no deadline").
- [ ] 4.3 Green: in `relay/relay.go`, check the engine clock against the lease deadline before each event and each sink, and bound each sink context by the remaining lease. Release the events that were not started through `Service.ReleaseEvents`, and add `Result.Released`.
  - Verify 4.1 and 4.2 on memstore and `make relay-matrix`.
  - Verify the `Result` accounting invariant with an assertion added to the existing `relaytest` pass helper.
- [ ] 4.4 Refactor: extract the stop condition shared with section 5 (deadline or cancellation), then run `/simplify`. Verify `make test`.

## 5. R3: a cancelled pass stops cleanly

- [ ] 5.1 Red: port `$V/relay/r3_cancel_test.go` `TestR3CancelledContextMidPass` into a new `relaytest/cancellation.go` as `runCancellationCases`, wired as `t.Run("Cancellation", ...)`.
  - Keep the three cases:
    - nothing is offered after cancellation;
    - e1, accepted before the cancel, is recorded delivered;
    - e2 is not left leased.
  - Add assertions that `Relay.Relay` returns an error matching `context.Canceled`, that `Result.Released == 1`, and that e2's attempt count is unchanged.
  - Verify it fails on memstore, SQLite and PostgreSQL.
- [ ] 5.2 Red: add a settlement-timeout table in `relay/relay_test.go`.
  - Setup: a memstore wrapped so that the settlement blocks until its context is done.
  - Cases:
    - default: a 10-second timeout, asserted through the context deadline the store receives;
    - override: `WithSettleTimeout(2*time.Second)` gives a 2-second deadline, and the event is counted `Unsettled`, with the timeout reported to the error handler.
  - Use a deadline assertion, not a sleep. Verify it fails.
- [ ] 5.3 Green: stop offering when `ctx.Err()` is set. Settle and release on `context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)`, and return `(result, ctx.Err())`. Add `DefaultSettleTimeout` and `WithSettleTimeout`, and update the godoc of `Relay.Relay` and `Run`. Verify 5.1 and 5.2 on memstore and `make relay-matrix`.

## 6. R8 and R9: the schedule

- [ ] 6.1 Red: port `$V/relay/r8_r9_r4_backoff_test.go` `TestR8BackoffMeasuredFromPassStart` into `relaytest/scheduling.go` as the case `a failure late in a pass still waits the full backoff`. Verify it fails ("due again … before it failed").
- [ ] 6.2 Red: port `TestR9NextAttemptOverflowAndCeiling` from the same file into `relaytest/scheduling.go` as table `the schedule is bounded`.
  - Cases:
    - the largest base and ceiling;
    - a one-minute base with the largest ceiling over 30 passes;
    - jitter never exceeding a one-minute ceiling.
  - Run the overflow cases red under amd64 (`GOARCH=amd64 go test -run 'TestSuiteAgainstTheInMemoryStore/Scheduling' -count=1 ./...` in `relaytest`), because on arm64 the float conversion saturates and does not show the defect. The jitter case fails on any architecture.
- [ ] 6.3 Green: read the settlement instant after each fan-out for `nextAttemptAt` and `PublishedAt`. Rewrite `nextAttemptAt` as design D6 describes: cap, draw the jitter window below the ceiling, clamp to the largest duration, then convert. Update the unit tests of `nextAttemptAt` in `relay/relay_test.go` first. Verify 6.1 and 6.2 (arm64 and amd64) and `make relay-matrix`.

## 7. R4 (observation, decided): default retry budget

- [ ] 7.1 Port `$V/relay/r8_r9_r4_backoff_test.go` `TestR4DefaultRetryBudget` into `relaytest/deadletter.go` as table `the retry budget`.
  - Default case: 12 attempts, dead-lettered at 4h39m30s with the draw at its midpoint.
  - Override case: `WithMaxAttempts(3)`, `WithBackoff(1s, 4s)` and `WithJitter(0)` give waits of 1 second and 2 seconds, and a dead letter on attempt 3.
  - Watch the default case fail against `DefaultMaxAttempts = 5` (7m30s).
  - Then set `DefaultMaxAttempts = 12`, with godoc stating the resulting budget, and fix any existing case that assumed 5.
  - Verify on memstore and `make relay-matrix`.

## 8. C9: relay options validated at construction

- [ ] 8.1 Red: port `$V/core/relay_test.go` `TestNewRelay_RejectsMeaninglessOptions` into `relay/relay_test.go`, folding its cases into the existing `TestNewRelayRefusesAConfigurationThatCouldOnlyFailLater` table.
  - Cases from the scratch test:
    - lease 0 and −1 second;
    - batch 0;
    - 0 attempts;
    - backoff (0, 0) and (−1 second, 1 hour);
    - jitter −0.5;
    - `WithSinks(nil)` beside a real sink.
  - Add these cases:
    - a ceiling below the base;
    - jitter 1.5;
    - `WithRelayOwner("")`;
    - `WithJitterSource(nil)`;
    - `WithRelayErrorHandler(nil)`;
    - `WithSettleTimeout(0)`.
  - Keep a control case: defaults, and `WithJitter(0)`, succeed.
  - Verify the new cases fail with "meaningless option must fail at construction".
- [ ] 8.2 Green: options record their errors, and `NewRelay` returns one `*hmntsk.ConfigurationError` naming each bad option. Check `ceiling ≥ base` after all options have been applied. Replace every "ignored" and "raised to it" in the option godoc with the rejection rule and the default it replaces. Verify 8.1 and `go test ./relay/...`.

## 9. Documentation (observations and contract text)

- [ ] 9.1 In `docs/delivery.md`, document:
  - the default retry schedule table (R4);
  - the ceiling as a hard bound with a narrowed jitter window (R9 observation);
  - the lease-sizing rule `lease ≥ batch × sinks × slowest sink timeout`, and that a pass stops at its lease and releases the rest (R2);
  - cancellation behaviour and `WithSettleTimeout` (R3);
  - lease-lost settlements reported through the error handler as `ErrLeaseLost` and counted `Unsettled` (R1).

  Verify `docs_test.go` in the core module passes, and that every option named in the doc exists (the gopls workspace symbol search finds each one).
- [ ] 9.2 Update godoc on `DefaultOutboxLease`, `DefaultOutboxBatch`, `OutboxStore`, `Result` and `Relay.Relay` to match the new contract, then run `go doc ./relay` and review the output.

## 10. Checks

- [ ] 10.1 Run `make lint`, `make test`, `make relay-matrix` and `make store-matrix`, all green. Also run `go test -race -count=20 -run 'TestSuiteAgainstTheInMemoryStore/(Fencing|LeaseDeadline|Cancellation)' ./...` in `relaytest`, to confirm the ported reproductions are deterministic.
