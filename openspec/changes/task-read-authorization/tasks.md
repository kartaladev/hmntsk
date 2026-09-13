Every task is test-first: write the failing test, run it with `GOTOOLCHAIN=go1.26.8 go test -run '<TestName>' -count=1 ./...` in the module, watch it fail for the intended reason, then make the smallest change to turn it green. Tables with two or more cases follow the `table-test` skill: `assert` closures, a `ctx` modifier where context matters, and `t.Context()`. Navigate with gopls (`/Users/zakyalvan/go/bin/gopls`). Never run `make tidy` or `go mod tidy`.

## 1. Engine: eligibility as a Service method

- [ ] 1.1 In the core module, write table test `TestServiceEligible`, then implement `Service.Eligible(ctx, task, actor)` delegating to `IsEligible` with the service's resolver. Cases: candidate user, member of a candidate group, excluded member (false, and the directory is not consulted), outsider, empty actor, and a resolver error surfacing as `ErrGroupResolution`. Use a `mockgen` resolver per `use-mockgen`. Verify the test passes and was seen failing first.
- [ ] 1.2 Write a test that `Service.Eligible` with a group pool and no resolver configured returns an error matching `ErrGroupResolution`, then make it pass. Verify with the focused run.

## 2. transportcore: read authorization types

- [ ] 2.1 Write `TestTaskReadEligibleIsLazy`, proving the bound eligibility function runs only when `TaskRead.Eligible` is called. Then add `TaskRead{Actor, Task}` with the unexported bound check and the `Eligible(ctx)` method. Verify the test passes.
- [ ] 2.2 Write table test `TestParticipantsOnly`, then implement `TaskReadAuthorizer`, `TaskReadAuthorizerFunc` and `ParticipantsOnly`. Cases:
  - the holder is permitted without resolving eligibility;
  - the creator is permitted without resolving eligibility;
  - an eligible candidate is permitted;
  - an ineligible outsider is refused with an error matching `hmntsk.ErrUnauthorized`;
  - no actor is refused;
  - an eligibility error matching `ErrGroupResolution` is returned as itself.

  Verify the test passes.
- [ ] 2.3 Write `TestTaskReadAuthorizerFuncPassesItsArgumentsThrough`, then make it pass.
- [ ] 2.4 Extend `TestAllowAll` so `AllowAll` permits every read as well as every query. Then change `AllowAll` to a value of exported type `AllowAllPolicy` implementing both interfaces. Verify existing `TestAllowAll` and `TestSelfOnly` cases still pass.
- [ ] 2.5 Write `TestNewRefusesANilTaskReadAuthorizer` (a `ConfigurationError` naming `AllowAll`), then add `WithTaskReadAuthorizer` and the nil check in `New`, with `ParticipantsOnly` as the default. Verify `TestNewRefusesANilQueryAuthorizer` still passes.

## 3. transportcore: guarded handlers and refusal mapping

- [ ] 3.1 Write error-mapping cases in `errors_test.go`: a policy refusal wrapping a validation error is `403` with `CodeForbidden`; a policy error matching `ErrGroupResolution` is `500`. Then generalise `queryRefusedError` into `policyRefusedError`, applying the group-resolution exception on the read path. Verify the new and existing `TestErrorMapping` cases pass.
- [ ] 3.2 Guard `getTask`. Write unit tests for the order "no actor is 403 before the store is read; unknown task with an actor is 404; refused is 403; permitted is 200", using a policy spy that records whether it was asked, then implement it. Verify the tests pass.
- [ ] 3.3 Guard `getHistory` with the same order, reusing the task it already reads and reading history only after the policy permits. Test that a refused history read never calls `Service.History`, observed through a store spy or by asserting no records are returned. Verify.

## 4. OpenAPI document

- [ ] 4.1 Update `openapi_test.go` expectations first: `getTask` and `getTaskHistory` declare `403`, `404` and `500`, and the `403` description mentions the read policy. Watch it fail, then update `responseShapes` and `statusDescriptions` in `openapi.go` and regenerate `transport/core/openapi.json` with `GOTOOLCHAIN=go1.26.8 go test ./transport/core -run TestOpenAPIDocument -update` (run inside the `transport/core` module). Verify `TestOpenAPIDocument` passes without `-update` and the diff of `openapi.json` contains only these changes.

## 5. Shared behavioural suite (http, gin, fiber)

- [ ] 5.1 Add `runReadAuthorizationCases` to `transporttest`, a table with the default policy. Cases:
  - holder 200;
  - eligible group member on a pooled task 200;
  - creator (`Owner`) 200;
  - outsider (`Carol`) 403 with no task in the body;
  - outsider history 403 with no records;
  - excluded member 403;
  - anonymous read of an existing and of an unknown task, both 403;
  - unknown task with an actor 404.

  Wire it into `RunSuite` as `t.Run("ReadAuthorization", ...)`. Verify the new cases pass through `transport/http`, `transport/gin` and `transport/fiber` via `GOTOOLCHAIN=go1.26.8 make transport-matrix`.
- [ ] 5.2 Add `readHostPolicy` cases, a table over `WithTaskReadAuthorizer`:
  - an auditor policy permits any task;
  - a policy refusing closed tasks refuses the holder of a completed task, with the policy's reason in the message;
  - a policy error that is also a validation error is still 403;
  - `AllowAll` permits `Carol`.

  Verify across all three bindings.
- [ ] 5.3 Add a directory-failure case: a resolver that errors makes an outsider's read of a pooled task answer 500, not 403. If `NewAPI` cannot inject a failing resolver, add an exported suite option to do so. Verify across all three bindings.
- [ ] 5.4 Re-run the whole existing suite (`Lifecycle`, `Errors`, `Payloads`, `Inbox`, `TaskTypes`, `Host`) and verify every earlier single-task read still answers 200 under the new default.

## 6. Documentation

- [ ] 6.1 In `docs/inbox.md`, add "Who may read a task":
  - the default `ParticipantsOnly`;
  - the order of checks (anonymous 403, unknown 404, refused 403, directory failure 500);
  - overriding with `WithTaskReadAuthorizer`, with an example composing `ParticipantsOnly`;
  - `AllowAll`;
  - the stated limit about existence leaking to authenticated actors.

  Rewrite the "Limit, stated" paragraph under "Who may query", and add an entry to "Unreleased breaking changes". Verify the docs tests (`docs_test.go` / `inbox_guide_test.go`, where they exist) pass.
- [ ] 6.2 Add godoc to every new exported name: `Service.Eligible`, `TaskRead`, `TaskRead.Eligible`, `TaskReadAuthorizer`, `TaskReadAuthorizerFunc`, `ParticipantsOnly`, `AllowAllPolicy` and `WithTaskReadAuthorizer`. Each option and default names what it replaces. Update the README where it describes HTTP authorization. Verify `GOTOOLCHAIN=go1.26.8 make lint` reports no documentation findings.

## 7. Verification and refactor

- [ ] 7.1 Run `/simplify` on the touched code, then re-run the focused tests and verify they stay green.
- [ ] 7.2 Run the full check `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln` and verify it passes with no new findings.
