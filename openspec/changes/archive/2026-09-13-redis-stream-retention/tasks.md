Every behaviour task is test-first: write the case, run it with a focused `go test -run ... -count=1` from `delivery/redis`, and confirm it fails **for the intended reason** — never because the package does not compile. Where a new exported name is needed to make a test compile, add it as an inert stub first (tasks 2.1, 3.1). Table tests use the project's `assert` closure form and `t.Context()`. Containers come only from `RunTestRedis`.

## 1. Test infrastructure

- [x] 1.1 Add a `TestOption` to `RunTestRedis` that applies `CONFIG SET` pairs after the ping (design D8), test-first: a case starting a broker with `stream-node-max-entries 1` asserts `CONFIG GET` returns `1`; seen failing against a no-op option, then green. (After `/simplify` the check lives in `retentionSuite.SetupSuite`, on the broker the retention cases already start, rather than in a test of its own that started an extra container.)
- [x] 1.2 Add a pinned `Redis7Image = "redis:7.4.7-alpine"` constant beside `RedisImage`, with a comment saying it exists to prove what works on a pre-8.2 broker; verified by its use in group 4. (After `/simplify` it is the unexported test constant `redis7Image` in `redis7_test.go`, passed through `WithTestImage`: nothing outside the tests needs an old broker, so it is not package API.)

## 2. Construction and validation

- [x] 2.1 Add inert stubs so the tests compile: `WithMaxLen(int64)`, `WithMaxAge(time.Duration)`, `WithTrimMode(TrimMode)`, `WithClock(hmntsk.Clock)`, the `TrimMode` type and `TrimKeepRef`/`TrimDelRef`/`TrimAcked` constants with their wire values (D4); verify `go build ./...` in `delivery/redis`.
- [x] 2.2 Red: extend the `TestNew` table with rejecting cases — length bound 0 and −1, age bound 0 and −1s, both bounds, a trim mode with no bound, an undefined mode (`"acked"`, `"BOGUS"`) — each asserting `ErrorIs(ErrConfiguration)` and a nil sink; and accepting cases — length bound alone, age bound alone, each mode with a bound, `WithClock(nil)` ignored. Run `go test -run TestNew -count=1 .` and confirm the rejecting cases fail because `New` accepts them.
- [x] 2.3 Green: carry the settings in `config`/`Sink` and validate them in `New`, with `ConfigurationError.Detail` saying how to fix each mistake; `TestNew` passes.

## 3. Trimming against Redis 8.2

- [x] 3.1 Red: add `retention_test.go` with a `retentionSuite` on one broker started with `stream-node-max-entries 1` and a stream per case. Length cases: no bound keeps every entry; a bound of N after N+k publishes leaves exactly the newest N (compare event IDs, oldest first); the stream never drops below N while publishing. Confirm they fail because nothing is trimmed.
- [x] 3.2 Red: age cases with a fixed `hmntsk.Clock` at the current time — seed entries with explicit IDs at clock−2h and clock−30m, set a 1h bound, publish once, and assert the −2h entry is gone and the −30m entry and the new one remain; a second case moves the fixed clock forward and asserts the cutoff follows the clock, not the wall time. Confirm they fail because nothing is trimmed.
- [x] 3.3 Green: set `Approx: true` plus `MaxLen`, or `MinID` computed as `<clock.Now()−maxAge in ms>-0` (D3), on the `XAddArgs` in `publish`; the clock defaults to the core's system clock. Groups 3.1 and 3.2 pass; the existing `TestPublish` and `TestDeliverClassifiesFailures` stay green.
- [x] 3.4 Red: trim-mode cases, each with a bound of 3 on 6 seeded entries plus one publish. `TrimKeepRef` with a group that read 4 unacked: stream holds the newest 3 and the group still lists 4 pending IDs. `TrimDelRef`, same setup: pending count 0. `TrimAcked` with a group that acked 1–2: entries 1–2 gone, 3 onwards kept. `TrimAcked` with a group that never read, after 5 publishes: length 11. `TrimAcked` with no groups: trimmed to the bound. Confirm the mode cases fail because no mode is sent.
- [x] 3.5 Green: set `XAddArgs.Mode` from the configured `TrimMode`, leaving it empty for the zero value; 3.4 passes. Add one assertion that a sink under `TrimAcked` still creates no consumer group, so the producer-only line in `TestCreatesNoConsumerMachinery` holds with retention on.

## 4. Behaviour on a pre-8.2 broker

- [x] 4.1 Red then green: a suite on `Redis7Image` asserting that a sink with no retention delivers, and that a length bound with no mode delivers and trims. Verify these tests would catch a regression: temporarily default the mode to `TrimKeepRef` when unset, confirm both cases fail with the 7.4 error, then revert.
- [x] 4.2 In the same suite, for each `TrimMode`: `Deliver` returns `OutcomeRetryable`, the error matches `ErrPublish`, and its message contains `ERR Invalid stream ID specified as stream command argument`. Keep that text in a test-package constant shared with 5.1, so the docs and the real broker are checked against the same string. Run with `go test -run 'TestRetention' -count=1 .`.

## 5. Operational documentation

- [x] 5.1 Red: add `delivery/redis/docs_test.go`, mirroring `delivery/webhook/docs_test.go`, asserting that `docs/delivery.md` names each `TrimMode` wire value, the minimum version `8.2`, the shared Redis 7 error-text constant, `stream-node-max-entries`, and each option name. Confirm it fails because the section does not exist.
- [x] 5.2 Green: add a Redis stream section to `docs/delivery.md` covering the message contract's retention options and every operational constraint in design D7, with the measured figures (overshoot up to one node; at most 100 × `stream-node-max-entries` trimmed per publish, 10,000 at the default), a version-check snippet an operator can run before setting a mode, and the stale-group remedy. 5.1 passes, and `delivery/webhook`'s docs test is still green.
- [x] 5.3 Write godoc that states each constraint where it is configured: `WithTrimMode` names Redis 8.2+ and the stall on older servers; `TrimKeepRef`/`TrimDelRef` name unread-entry loss; `TrimAcked` names the stale-group hazard; `WithMaxAge` names clock skew; `WithMaxLen`/`WithMaxAge` say "at least" and point to `docs/delivery.md`. Add a retention paragraph to the package doc, and mention optional retention in README's Redis rows. Verify with `go doc -all .` in `delivery/redis` and `GOTOOLCHAIN=go1.26.8 make lint`.

## 6. Verification

- [x] 6.1 Run `/simplify` on the touched `delivery/redis` code, then re-run the module's tests.
- [x] 6.2 `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln` is green across the workspace, and `openspec validate redis-stream-retention --strict` passes.
- [x] 6.3 On the PR, the `delivery/redis` CI job pulls both pinned images and passes; confirm in the job log that the Redis 7 suite ran rather than being skipped. (PR #3: all 24 checks green; `delivery delivery/redis` passed as `ok … 10.487s`. The job runs `go test -count=1 -timeout 30m ./...` without `-v`, so it prints no per-test lines. The suite is proven to have run because there is no `-run` or `-short` filter, no skip anywhere in the module, and a Redis 7 container that failed to start would have failed `SetupSuite`.)
