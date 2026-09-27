Every defect task is test-first. Each one starts from its scratch reproduction in `$V = /private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify` and follows the same steps:
1. **Port.** Move the reproduction into the repo next to the code it covers. Use `package hmntsk_test` for the root package, and the repo's existing fixtures instead of the scratch `newFixture`. Tables follow the `table-test` skill: `assert` closures, `t.Context()`.
2. **Red.** Run it with `go test -run '<Name>' -count=1 ./...` in the module and confirm it fails for the reason recorded in `$V/VERIFIED-FINDINGS.md` section B, not a compile error.
3. **Green.** Make the smallest fix that turns it green.
4. **Refactor.** Refactor, consider `/simplify`, and re-run.

Conventions:
- Navigate with gopls at `$(go env GOPATH)/bin/gopls`.
- Doubles come from `use-mockgen`, and real databases from `use-testcontainers`.
- Never run `go mod tidy`.

## 1. Progress shape check never stricter than full validation (C1)

- [ ] 1.1 Port `$V/core/schema_test.go` `TestSaveProgress_AcceptsDraftValidAgainstFullSchema` into `service_progress_test.go` as table rows `not`, `oneOf` and `if-then`. Run it and confirm it fails with `'not' failed`, `'oneOf' failed, subschemas 0, 1 matched` and `/n: minimum: got 3, want 10`.
- [ ] 1.2 Make `relaxSchema` polarity-aware: copy `not` and `if` verbatim, and rewrite `oneOf` to `anyOf` of relaxed branches. Verify 1.1 passes, and that the existing `TestServiceSaveProgressValidationAndRefusals` "wrongly typed field" rows still refuse.
- [ ] 1.3 Add a property-style test: for each existing schema fixture plus the three C1 schemas, every document the full validator accepts is accepted by the shape validator. Verify it passes.
- [ ] 1.4 Refactor, and update the `relaxSchema` godoc to state the invariant (full-valid implies shape-valid) and the polarity rule. Re-run the root tests.

## 2. Default priority when a type names none (C2)

- [ ] 2.1 Port `$V/core/registry_test.go` `TestCreate_TypeWithoutDefaultPriorityGetsPriorityDefault` into `registry_test.go` as a table with two rows:
  - no default, so the task gets `PriorityDefault`, and `Registry.Lookup` reports 5;
  - an explicit `&PriorityHighest`, so the task gets 0.

  Run it and confirm the first row fails with priority 0 against the expected 5. The second row fails to compile until 2.2.
- [ ] 2.2 Change `TypeSpec.DefaultPriority` to `*Priority`:
  - resolve nil to `PriorityDefault` in `Register` before storing;
  - make `Equal` compare effective values;
  - make `Clone` copy the pointer;
  - update `create.go`, `store/sqlcore/types.go` (encode and decode), `transport/core/dto.go`, `examples/internal/invoicing`, `transporttest`, and the test fixtures gopls reports.

  Verify 2.1 passes, `registry_test.go` `TestRegistryRegisterConflicts` gains a row saying nil and `&PriorityDefault` register idempotently, and `go build ./...` passes in every module (`make build`).
- [ ] 2.3 Update the godoc on `TypeSpec.DefaultPriority`, naming the default it replaces and the explicit-zero override. Add a `TypeSpec` JSON row showing that `defaultPriority` is absent for nil and `0` for `&PriorityHighest`. Verify with `go test -run 'TestTypeSpec' ./...`.

## 3. Escalation policy validation (C3)

- [ ] 3.1 Port `$V/core/registry_test.go` `TestRegister_RejectsInvalidEscalationPolicy` into `registry_test.go`. Add rows for "WIDEN with nothing to add" and "notify policy with AddGroups". Port `TestSweep_LowercaseWidenPolicyWidensOrIsRefused` into `sweep_test.go`. Run both and confirm the invalid rows fail with a nil error where `ErrConfiguration` is expected, and that the sweep row fails because `mgr` is not added.
- [ ] 3.2 Add `(*EscalationPolicy).Validate() error` and call it from `Registry.Register`, wrapped in `*ConfigurationError` naming the type. Verify 3.1 passes.
- [ ] 3.3 Add a `Create` row: a lowercase `widen` in `CreateRequest.Escalation` fails with `ErrValidation`, with pointer `/escalation`, and no task is stored. Watch it fail, then validate in `Service.Create` and verify it passes.
- [ ] 3.4 Refactor, and add the godoc on `EscalationPolicy`, `Validate` and `EscalationAction` listing the rules. Re-run the root tests.

## 4. Create overrides validated (C4) and transport overflow (T15)

- [ ] 4.1 Port `$V/core/create_test.go` `TestCreate_RejectsInvalidOverrides` into a new `create_test.go` in the root package. Add the row "explicit zero Deadline means no due date". Run it and confirm:
  - priority 99, priority -1 and the negative deadline fail with a nil error where `ErrValidation` is expected;
  - the zero-deadline row fails with a due date 24h out.
- [ ] 4.2 Make `Service.Create` validate `req.Priority` and a negative `req.Deadline` as `*ValidationError`, and make `resolveDueAt` treat an explicit zero `Deadline` as no deadline. Verify 4.1 passes.
- [ ] 4.3 Port the `T15_*` rows of `$V/transport/request_test.go` `TestCreateRequestHandling` into `transporttest/errors.go` `runErrorCases`, so all three bindings run them:
  - overflowing `deadlineSeconds` 18446744074;
  - `deadlineSeconds` -60;
  - priority -5;
  - priority 11.

  Add the in-range `201` control row. Run `go test -count=1 ./...` in `transport/http` and confirm the overflow row fails with 201 and a due date of about 290ms. Confirm the other rows are already green after 4.2.
- [ ] 4.4 In `transport/core/api.go`, refuse a `deadlineSeconds` beyond `math.MaxInt64/int64(time.Second)` as a `ValidationError` with pointer `/deadlineSeconds`. Verify 4.3 passes on every binding (`make transport-matrix`).
- [ ] 4.5 Add `minimum`/`maximum` to `priority` and `minimum: 0` to `deadlineSeconds` in the OpenAPI generation. Regenerate with `go test ./transport/core -run TestOpenAPIDocument -update` and verify `TestOpenAPIDocument` passes without `-update`.

## 5. Sweep reports illegal escalations (C5)

- [ ] 5.1 Port `$V/core/sweep_test.go` `TestSweep_ReportsIllegalSupersedeOfInProgressTask` into `sweep_test.go`. Add a row where a stale-version conflict stays silent: the task changes between claim and escalate, driven deterministically through an event handler or a second operation after `ClaimOverdue`. Run it and confirm the supersede row fails with an empty handler error list.
- [ ] 5.2 In `Sweep`, skip only `*ConflictError` and report everything else wrapped with the task ID. Verify 5.1 passes, including `errors.Is(err, ErrIllegalTransition)`.

## 6. Escalation cap on direct escalation (C7) and in-progress exemption decision

- [ ] 6.1 Port `$V/core/service_test.go` `TestServiceEscalate_HonoursPolicyLimits` into `sweep_test.go`, next to `TestManualEscalationTakesTheSamePathAsASweep`:
  - keep the `MaxEscalations=1 already reached` row, which is refused with `ErrIllegalTransition`, an unchanged count and no event;
  - invert the `ExemptInProgress` row to the spec decision: the direct escalation is applied and the count increases.

  Run it and confirm the cap row fails with a nil error and a grown count.
- [ ] 6.2 Add an optional `Reason` to `TransitionError`, appended to `Error()` when set. Make `Task.Escalate` refuse at the cap with that reason. Verify 6.1 passes, `TestSweepExclusions` still counts a capped task as exempted, and the `transitions_test.go` table gains the cap row.
- [ ] 6.3 Update the godoc on `EscalationPolicy.ExemptInProgress` (sweep-only, a direct escalation overrides it) and `MaxEscalations` (it caps direct escalation too, with no force path, so use `Delegate`). Update `docs/` if escalation is described there. Verify the root docs test passes.

## 7. Event isolation (C8)

- [ ] 7.1 Port `$V/core/service_test.go` `TestOutbox_StoredEventIsIsolatedFromCallerMutation` into the `storetest` outbox cases as a new `isolation` group, so it runs through `RunSuite` on every store, with two rows:
  - mutating the appended events;
  - mutating an `OutboxEntry` read.

  Also add a root-package row that mutates `Result.Events` over memstore. Run `go test -count=1 ./...` in `storetest` (memstore) and confirm both rows fail with `tampered` and `mallory`.
- [ ] 7.2 Add `Event.Clone()`, deep-copying every reference field, with its own table test. Use it in memstore `Append` and `outboxRow.entry()`. Verify 7.1 passes on memstore, and on every SQL combination with `make store-matrix`.

## 8. Sweeper construction validation (C9, sweeper part)

- [ ] 8.1 Port `$V/core/sweep_test.go` `TestNewSweeper_RejectsMeaninglessOptions` into `sweep_test.go`. Add a consumer-override row, `WithLeaseDuration(10*time.Minute)` accepted, whose lease is observed on a claimed task. Run it and confirm the lease-zero, lease-negative, batch-zero and batch-negative rows fail with a nil error.
- [ ] 8.2 Make `WithLeaseDuration` and `WithSweepBatch` assign unconditionally, and validate in `NewSweeper` with a `*ConfigurationError` naming the option. Update both godocs to name the default replaced. Verify 8.1 passes and `TestNothingStartsOnItsOwn` is unaffected.

## 9. Sweep stops on cancellation (C11)

- [ ] 9.1 Port `$V/core/sweep_test.go` `TestSweep_StopsWhenContextCancelledMidBatch` into `sweep_test.go`. It uses the in-process handler that cancels after the first escalation. Run it with `-count=20` and confirm it fails every time with a nil error.
- [ ] 9.2 Check `ctx.Err()` before each claimed task in `Sweep`, returning the partial result and `ctx.Err()`. Verify 9.1 passes with `-count=20`, and `TestRunStopsWhenTheHostStopsIt` still passes.

## 10. Documentation observation (C10)

- [ ] 10.1 Correct the godoc on `TransitionRecord` (`history.go`), `applyJSONPatch` (`patch.go`) and `SaveProgressRequest.Patch` (`service.go`): patches are applied to the stored draft and are not persisted, and only merged progress is kept. Grep `README.md` and `docs/` for the same claim and fix it. Verify `go test -run 'Docs|Example' ./...` in the root module passes.

## 11. Checks

- [ ] 11.1 From the repository root, run `make lint split-check test test-race` and verify everything passes.
- [ ] 11.2 Run `make store-matrix transport-matrix test-integration` (containers for Postgres and MySQL) and verify everything passes, including the new `storetest` isolation rows and the `transporttest` create-override rows on all bindings.
- [ ] 11.3 Re-run each ported test with `-count=20` (`go test -run 'TestSaveProgress|TestCreate|TestRegister|TestSweep|TestServiceEscalate|TestNewSweeper' -count=20 ./...`) and verify it is stable.
