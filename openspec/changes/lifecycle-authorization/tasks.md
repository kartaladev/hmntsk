Every defect task is test-first. Each one starts from its scratch reproduction in `$V = /private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify` and follows the same steps:

1. **Port.** Move the reproduction into the repo next to the code it covers.
   - Engine tests go in `package hmntsk_test`, beside `transitions_test.go` or `service_test.go`, and use the repo's fixtures instead of the scratch `newFixture`.
   - HTTP tests go in the shared `transporttest` suite, so that `transport/http`, `transport/gin` and `transport/fiber` all run them.
   - Tables follow the `table-test` skill: `assert` closures and `t.Context()`.
2. **Red.** Run it with `go test -run '<Name>' -count=1 ./...` in the module. Confirm it fails for the reason recorded in `$V/VERIFIED-FINDINGS.md` section A, not because of a compile error. Where a new API name is needed first, add a stub that compiles and does nothing, so the failure is the behavioural one.
3. **Green.** Make the smallest change that turns it green.
4. **Refactor.** Refactor, consider `/simplify`, and re-run.

Conventions:
- Navigate with gopls at `$(go env GOPATH)/bin/gopls`.
- Test doubles come from `use-mockgen`.
- Never run `go mod tidy`.

## 1. Engine: an actor is required, and checked before the task is read (E1 no-actor rows, E3)

- [ ] 1.1 Port the `TestService_ActorChecks` rows `E1 cancel with no actor`, `E1 escalate with no actor` and `E3 stale version with no actor is refused as unauthorised, not as a conflict` from `$V/core/service_test.go` into `service_test.go`, as table `TestServiceRequiresActor`. Add rows:
  - claim of an unknown task with no actor, where the error is `ErrUnauthorized` and not `ErrNotFound`;
  - `Create` with no actor, where the error is `ErrUnauthorized` and no task is stored.

  Run the table and confirm the cancel and escalate rows fail with a nil error, and the E3 row fails with a `*ConflictError` carrying `Current`.
- [ ] 1.2 In `mutate`, refuse an empty `req.Actor` with `*AuthorizationError` right after the `TaskID` check and before `store.Get`. Do the same at the top of `Service.Create`. Verify 1.1 passes, and that `transitions_test.go` and the existing `service_test.go` still pass. The "Illegal transition is refused" cases supply an actor, so they remain conflicts.
- [ ] 1.3 Add a row asserting that a fault raised during creation records an empty actor while `Task.CreatedBy` is the creator. Then narrow the godoc on `TransitionRecord.Actor` (`history.go`) and `Event.Actor` (`event.go`) to say that an empty actor appears only on system faults. Verify with `go test -run 'TestServiceRequiresActor' ./...`.

## 2. Engine: the `LifecycleAuthorizer` port and `OwnershipRules` (E1 stranger row, E2)

- [ ] 2.1 Port the `TestService_ActorChecks` rows `E1 cancel by a stranger (not creator, not assignee)`, `E2 suspend unheld READY task by non-candidate` and `E2 resume unheld task by non-candidate` from `$V/core/service_test.go` into `service_test.go`, as table `TestServiceOwnershipRules`. Add rows:
  - the creator cancels (success);
  - the assignee who is not the creator cancels, and is refused;
  - a group-member candidate suspends a pooled task (success);
  - the non-candidate creator suspends a pooled task (success);
  - a candidate who is not the holder suspends a reserved task, and is refused;
  - a `COMPLETED` task cancelled by a stranger is a conflict, not an authorisation error;
  - a failing resolver on an unheld suspend gives `ErrGroupResolution`.

  Use a `mockgen` resolver per `use-mockgen`. Run the table and confirm the three ported rows fail with a nil error.
- [ ] 2.2 Add `LifecycleCheck`, `NewLifecycleCheck`, `LifecycleCheck.Eligible` (lazy), `LifecycleAuthorizer`, `LifecycleAuthorizerFunc` and `OwnershipRules` in a new `lifecycle_authorizer.go`, with unit table `TestOwnershipRules`. It must show:
  - the creator check runs before `Eligible`;
  - `Eligible` is never called for cancel or escalate;
  - a no-actor check is refused.

  Watch it fail against a stub, then implement it and verify it passes.
- [ ] 2.3 Add `WithLifecycleAuthorizer`, with `OwnershipRules` as the default in `New`, and write `TestNewRefusesANilLifecycleAuthorizer` first. In `Service.Cancel`, `Suspend`, `Resume` and `Escalate`, call the policy after the pure transition succeeds. Wrap refusals so they match `ErrUnauthorized`, and pass `ErrGroupResolution` through. Remove `requireAssigneeIfHeld` from `Task.Suspend` and `Task.Resume`. Update the `Authorize` godoc. Verify 2.1, 2.2 and the constructor test pass, along with the root package suite.
- [ ] 2.4 Add table `TestServiceLifecycleAuthorizerOverride`:
  - an "admin" policy cancels a task it did not create;
  - a "no pooled suspension" policy refuses the creator, with the policy's reason;
  - a permit-all policy still refuses a no-actor cancel (the invariant from Decision 1);
  - a permit-all policy still refuses a non-assignee complete (the stated limit).

  Watch the override rows fail before wiring, if wired in a separate step, and verify them all green.

## 3. Engine: system escalations bypass the port

- [ ] 3.1 Write `TestSweepBypassesLifecycleAuthorizer` in `sweep_test.go`. With a policy that refuses every escalation, a sweep escalates an overdue task, and history and the event carry `Sweeper.Owner()` as actor. A direct `Service.Escalate` on another task is refused. Run it and confirm the sweep row fails once 2.3 routes `Escalate` through the policy.
- [ ] 3.2 Add the unexported `Service.escalate(ctx, req, swept bool)`. `Service.Escalate` calls it with `false` and `Sweeper.Sweep` calls it with `true`. Verify 3.1 and the existing `sweep_test.go` pass. Coordinate with `engine-input-validation` (C7 cap in `Task.Escalate`) if it has landed.

## 4. transportcore: anonymous refusal before lookup (T1 anonymous rows, T4, T5 anonymous part)

- [ ] 4.1 Port `$V/transport/authz_test.go` `TestLifecycleAuthorization` rows `T1 anonymous DELETE cancels nothing`, `T1 anonymous escalate is refused` and `T4 anonymous stale-version call does not reveal the task exists`, and `TestAnonymousCreate`, into a new `transporttest/authz.go` as `runLifecycleAuthorizationCases`. Wire it into `RunSuite` as `t.Run("LifecycleAuthorization", ...)`.
  - The scratch `createPooled` fixture sends `candidates` and `callback`. Create it through an API built with `WithCreateAuthorizer(transportcore.AllowAll)`, or register the pooled assignment as the type default.
  - Run `make transport-matrix`. Confirm the rows fail on net/http, gin and fiber with `200`/`201`/`409` where `403` is expected.
- [ ] 4.2 In `operation()` and `createTask`, refuse `req.Actor == ""` with `refuse(...)` before `decode`. Verify 4.1 passes on all three bindings, and that an anonymous malformed body is also `403`.

## 5. transportcore: `OperationAuthorizer`, escalation withheld (T1 escalate, T1/T2 stranger rows over HTTP)

- [ ] 5.1 Port the `TestLifecycleAuthorization` rows `T1 non-owner authenticated actor cannot cancel`, `T2 non-candidate cannot suspend a pooled task` and `T2 non-candidate cannot resume a task it suspended` into `runLifecycleAuthorizationCases`. Add rows:
  - an authenticated escalation is `403` under the default policy;
  - the creator cancels (`200`, `EXITED`).

  Run `make transport-matrix`. Confirm the ported rows now pass through the engine rules from group 2, and that the authenticated-escalation row fails with `200`. If group 2 has not landed, confirm all of them fail.
- [ ] 5.2 Write unit tests `TestWithholdEscalation`, `TestOperationAuthorizerFuncPassesItsArgumentsThrough`, `TestNewRefusesANilOperationAuthorizer`, and an `AllowAll` row. Then add `OperationCall`, `OperationAuthorizer`, `OperationAuthorizerFunc`, `WithholdEscalation`, `WithOperationAuthorizer` and `AllowAllPolicy.AuthorizeOperation`, and call the policy in `operation()` after the anonymous check. Verify the unit tests and 5.1 pass.
- [ ] 5.3 Add `operationHostPolicy` cases to the suite:
  - "ops" may escalate (`200`, pool widened);
  - a policy refusing every cancel refuses the creator, with its reason;
  - a policy error that is also a validation error is still `403`;
  - `AllowAll` permits escalation.

  Verify across all three bindings.

## 6. transportcore: `CreateAuthorizer`, plain creates by default (T5)

- [ ] 6.1 Extend the ported `TestAnonymousCreate` into table `runCreateAuthorizationCases`. Rows, all sent by `Owner`:
  - a plain create is `201` with `createdBy` `owner`;
  - `callback` is `403` naming `callback`;
  - `candidates` plus `escalation` is `403` naming both;
  - `id` is `403`;
  - a host policy for `billing-service` permits all overrides (`201`).

  Run `make transport-matrix` and confirm the refusal rows fail with `201`.
- [ ] 6.2 Write `TestPlainCreatesOnly`, `TestTaskCreateOverrides`, `TestNewRefusesANilCreateAuthorizer` and an `AllowAll` row. Then add `TaskCreate`, `TaskCreate.Overrides`, `CreateAuthorizer`, `CreateAuthorizerFunc`, `PlainCreatesOnly`, `WithCreateAuthorizer` and `AllowAllPolicy.AuthorizeCreate`, and call the policy in `createTask` after `decode`. If `transport-binding-parity` has landed, call it after the id-shape check. Verify the unit tests and 6.1 pass.

## 7. transportcore: responses honour the read policy (T3)

- [ ] 7.1 Port `$V/transport/authz_test.go` `TestLifecycleResponseHonoursReadPolicy` into the suite as `runLifecycleResponseCases`. Its original rows now pass by refusal, so keep them as regression rows. Add rows:
  - with an operation policy permitting "ops" to escalate, the body has no `SECRET-INPUT` or `SECRET-REF` and carries `id`, `status` and `version`;
  - with a read policy permitting "ops", the body is the full task;
  - a candidate's claim returns the full task.

  Run `make transport-matrix` and confirm the "ops" row fails because it leaks the secrets.
- [ ] 7.2 Add `TaskReceipt{ID, Status, Version}` to `dto.go`. Change `respond` to take the acting user, build a `TaskRead` for `result.Task`, and render the receipt when the read policy refuses or eligibility fails. Apply this to `createTask` too. Verify 7.1 on all three bindings. Reconcile with `post-commit-dispatch`'s `respond` rewrite if it has landed.

## 8. OpenAPI document

- [ ] 8.1 Update the `openapi_test.go` expectations first:
  - every lifecycle route and `createTask` declares `403`;
  - their success responses are `oneOf` task or `TaskReceipt`;
  - the `403` description mentions the operation and create policies.

  Watch it fail. Then update `openapi.go` and regenerate `transport/core/openapi.json` with `go test ./transport/core -run TestOpenAPIDocument -update`, run inside the module. Verify it passes without `-update`.

## 9. Existing suites, examples and docs

- [ ] 9.1 Run the existing `transporttest` groups (`Lifecycle`, `Errors`, `Payloads`, `Inbox`, `Read`, `TaskTypes`, `Host`) under `make transport-matrix`. Move the cases that escalate over HTTP (`lifecycle.go:62`) or create with `id`, `candidates` or `callback` (`lifecycle.go:119`, `:128`) onto an API built with `WithOperationAuthorizer(transportcore.AllowAll)` and `WithCreateAuthorizer(transportcore.AllowAll)`. Verify every group is green on all three bindings.
- [ ] 9.2 Audit `examples/` for creates or lifecycle calls without an actor, cancels by a non-creator, HTTP escalations, and HTTP creates with overrides. Use gopls references to `Service.Create`, `Cancel`, `Suspend`, `Resume` and `Escalate`, and grep the React client under `examples/contextual-ui`. Fix any that are found. `examples/escalation` (`"operations-desk"`) and `examples/lifecycle-operations` (the creator cancels) are expected to stay unchanged. Verify with `cd examples && go test ./...`, including the golden-output tests.
- [ ] 9.3 In `docs/inbox.md`, add "Who may change a task" beside "Who may read a task". It covers:
  - the actor invariant;
  - `OwnershipRules` and `WithLifecycleAuthorizer`, with an example that permits assignees to cancel and delegates everything else to `OwnershipRules`;
  - the sweeper bypass;
  - `WithholdEscalation`, `PlainCreatesOnly` and `AllowAll`;
  - receipts;
  - the stated limit on existence and version leaking to authenticated actors.

  Add entries to "Unreleased breaking changes". Update the README's HTTP authorization and lifecycle sections. Verify the docs tests pass where they exist.
- [ ] 9.4 Add godoc to every new exported name. Each option and default must name what it replaces, and each port must say what the library uses when none is supplied. Verify that `make lint` reports no documentation findings.

## 10. Checks

- [ ] 10.1 Run `/simplify` on the touched code, then re-run the focused tests from groups 1–7 and verify they stay green.
- [ ] 10.2 Run `make lint`, `make test`, `make test-race` and `make transport-matrix`, plus `cd examples && go test ./...`. Verify they all pass with no new findings. Re-run the original scratch reproductions in `$V/core` and `$V/transport` and verify they now pass.
