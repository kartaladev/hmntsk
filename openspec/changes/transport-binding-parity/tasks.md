Every defect starts from its scratch reproduction, `V=/private/tmp/claude-501/-Users-zakyalvan-Documents-RND-hmntsk/e93557f3-b6ef-4e0a-8f39-5740b374b0f5/scratchpad/verify`, `$V/transport/*_test.go`. Work through each one in three steps:

1. **Red:** port the reproduction into the repo. Put it in the shared `transporttest` suite wherever the behaviour is part of the contract, so the net/http, gin, Fiber-`Mount` and Fiber-`App` harnesses all run it. Run it with `go test -run '<Name>' -count=1 ./...` in the binding modules, and watch it fail on the named binding for the reason stated.
2. **Green:** make the smallest fix that turns it green.
3. **Refactor:** consider `/simplify`, and re-run.

Conventions:
- Tables with two or more cases follow the `table-test` skill: the `assert` closure, and `t.Context()`.
- Navigate with gopls at `$(go env GOPATH)/bin/gopls`.
- Never run `go mod tidy`.

## 1. transportcore: base path validation (T13)

- [ ] 1.1 **Red.** Port `TestBasePathWithoutLeadingSlash` (`$V/transport/wiring_test.go`) into a `transport/core` table test, `TestNewRefusesInvalidBasePath`. Cases:
  - `v2`, `""`, `/`, `/a//b`, `/a/../b`, `/{x}` and `/a b` → `hmntsk.ConfigurationError`;
  - `/api/tasks-v2/` → accepted, with `BasePath()` equal to `/api/tasks-v2`.
  
  Add the suite case "a valid custom base path is served" to `transporttest`, as a wrapper around the harness that uses `/api/tasks-v2`. Verify `v2` is accepted today, so the test fails. That is the net/http failure the scratch test showed.
- [ ] 1.2 **Green.** `WithBasePath` records the raw value, and `New` validates it per design decision 7. Verify 1.1 passes, and `make transport-matrix` runs the new suite case green on every harness.
- [ ] 1.3 **Refactor.** Update the `WithBasePath` godoc to name the default and the rule. Verify with `go test ./transport/core/...`.

## 2. transportcore: strict body decoding (T14a, T14b)

- [ ] 2.1 **Red.** Port the two T14 rows of `TestCreateRequestHandling` (`$V/transport/request_test.go`) into a new `transporttest` table, `runRequestBodyCases`, called from `RunSuite` as `"RequestBodies"`. Rows:
  - case-folded `"Type"` → `400`;
  - duplicate `"type"` → `400`;
  - unknown `createdBy` → `400` with pointer `/createdBy`;
  - `input` holding `Amount`, `amount` and an unschema'd member → `201` with the payload unchanged.
  
  Run it and verify the first three rows fail on all three bindings, with `201`.
- [ ] 2.2 **Green.** Implement the reflective token walk and `WithUnknownFields` per design decision 8, in a new `transport/core/decode.go`. Use it from `decode` for both `CreateTaskRequest` and `OperationRequest`. Verify 2.1 passes on every harness.
- [ ] 2.3 Add unit table `TestDecodeStrict` in `transport/core`. Rows:
  - nested case-fold in `escalation` (`/escalation/AddUsers`);
  - a duplicate in `correlation`;
  - `extra` and `referenceParameters` members never inspected;
  - `UnknownFieldsIgnore` accepts `createdBy` but still refuses `Type`;
  - an out-of-range mode is a `ConfigurationError`.
  
  Verify it passes.
- [ ] 2.4 **Consumer override in the suite.** Add a row in which the API is built with `WithUnknownFields(UnknownFieldsIgnore)` via `NewAPI`, and `{"type":"note","createdBy":"ceo"}` → `201` with `createdBy` equal to the acting user. Verify it passes.
- [ ] 2.5 **Refactor.** Use `/simplify`. Add `BenchmarkDecodeCreate` (1 KiB and 1 MiB bodies) to record the cost of the second pass. Verify with `go test -bench DecodeCreate -run ^$ ./transport/core/`.

## 3. transportcore: masked 500 and the internal error handler (T7)

- [ ] 3.1 **Red.** Port `TestGroupResolutionFailureBody` (`$V/transport/errors_test.go`) into `transporttest` as a case of `runErrorCases`, using `newAPI` with a directory whose error names `10.20.0.7` and `password rejected`. Cover the inbox query and the count. Verify it fails on all three bindings because the body contains the cause.
- [ ] 3.2 **Red.** In `transport/core/errors_test.go`, change the group-resolution row to also assert the fixed message `"group membership could not be resolved"`. Verify it fails.
- [ ] 3.3 **Green.** Mask every `CodeInternal` in `errorDetail`, add `WithInternalErrorHandler` (default: `slog.Default().ErrorContext`), and make `fail` an `*API` method that reports every `500`, the encode failure included. A nil handler is a `ConfigurationError` from `New`. Verify 3.1 and 3.2 pass.
- [ ] 3.4 **Default and override.** Add table `TestInternalErrorHandler` in `transport/core`. Rows:
  - the default logs through a swapped `slog.Default()` (not parallel; restore in `t.Cleanup`);
  - a host handler receives an error for which `errors.As` finds `*hmntsk.GroupResolutionError` and its cause;
  - a nil handler is a `ConfigurationError`.
  
  Verify it passes.
- [ ] 3.5 **Refactor.** Update the `StatusFor` and `errorDetail` godoc and the OpenAPI `500` description. Verify `go test ./transport/core/...`, the OpenAPI golden test included.

## 4. transportcore and binders: client-supplied ids (T10)

- [ ] 4.1 **Red.** Port `TestClientSuppliedIDIsAddressable` (`$V/transport/routing_test.go`) into `transporttest` as a table, `runTaskIDCases`, under `"TaskIDs"`. Rows:
  - `a b` → `400`;
  - `x/y` → `400`;
  - `count` → `400` with pointer `/id`;
  - `.` and `..` → `400`;
  - 129 characters → `400`;
  - `order-42_v1.a~b` → `201`, then `GET` → `200`.
  
  Verify `count` fails on net/http and gin, and `a b`/`x/y`/`count` fail on Fiber, as the scratch run showed.
- [ ] 4.2 **Green.** Add `TaskIDPolicy`, `DefaultTaskIDPolicy` and `WithTaskIDPolicy`, and the fixed rule computed from `Routes()`, per design decision 4. Verify 4.1 passes on every harness.
- [ ] 4.3 **Red, Fiber unescape.** Add a suite row where the API uses `WithTaskIDPolicy` admitting spaces, and `a b` → `201`, then `GET /tasks/a%20b` → `200`. Verify it fails on both Fiber harnesses with `404 "task a%20b not found"`.
- [ ] 4.4 **Green.** In `fibertransport.handle`, use `url.PathUnescape` on each param. A malformed escape answers the contract `404`. Verify 4.3 passes on every harness.
- [ ] 4.5 Add unit table `TestTaskIDPolicy` in `transport/core`:
  - `DefaultTaskIDPolicy` accepts and refuses at the boundaries;
  - a permissive host policy still cannot admit `count`, `/` or `..`;
  - a nil policy is a `ConfigurationError`.
  
  Verify it passes.
- [ ] 4.6 **Refactor.** Add the id pattern to the OpenAPI `CreateTaskRequest.id` schema, and godoc the stated limit for ids created through the Go API. Verify the OpenAPI test passes.

## 5. Content-Encoding (T9)

- [ ] 5.1 **Red.** Port the `T9 gzip` row of `TestCreateRequestHandling` into `runRequestBodyCases`: gzip body plus `Content-Encoding: gzip` → `400`. Extend `transporttest.Client` so a case can send raw bytes and extra headers. Verify it fails on both Fiber harnesses with `201`.
- [ ] 5.2 **Green.**
  - Add `Request.ContentEncoding`. The core's `decode` refuses anything other than empty or `identity`.
  - Fill `ContentEncoding` in all three binders.
  - Switch Fiber to `c.Request().Body()`.
  
  Verify 5.1 passes on every harness.
- [ ] 5.3 **Override.** Add a `transport/http` test in which host middleware gunzips the body and deletes the header before `Handler` → `201`. Verify it passes.
- [ ] 5.4 **Refactor.** Update the `Request` godoc and the binder package docs (the host decodes content codings). Verify `go vet` is clean in each module.

## 6. Body limit and 413 (T8)

- [ ] 6.1 **Red.** Port the three T8 rows of `TestCreateRequestHandling` into `runRequestBodyCases`:
  - 9 MiB → `413` with a JSON body;
  - 9 MiB with a completing tail → `413`;
  - 5 MiB → `201`.
  
  Verify gin fails the first two (`201`/`400`), and Fiber-`Mount` fails the 5 MiB row with a plain-text `413`.
- [ ] 6.2 **Green, core.** Add `DefaultMaxBodyBytes`, `StatusPayloadTooLarge` and `PayloadTooLargeResponse()`. net/http writes it instead of the inline literal. Verify net/http still passes 6.1.
- [ ] 6.3 **Green, gin.** Use `http.MaxBytesReader`, and answer `*http.MaxBytesError` with `413`. Verify gin passes 6.1.
- [ ] 6.4 **Green, Fiber.**
  - Add `MaxBodyBytes`, `WithMaxBodyBytes`, and the binder-side length check.
  - Add `ErrorHandler(next)` mapping 404/405/413.
  - `App()` sets `BodyLimit` and uses `ErrorHandler`.
  - The Fiber-`Mount` harness sets `BodyLimit` and wraps with `ErrorHandler`, as the godoc tells hosts to.
  
  Verify both Fiber harnesses pass 6.1.
- [ ] 6.5 **Wiring mistakes.** Add table `TestNonPositiveBodyLimitIsConfigurationError` in each binder module, for `0` and `-1` on `Mount` and the convenience constructor. Verify it fails first, since the value is ignored today, then make options record the bad value and constructors refuse it. Verify it passes.
- [ ] 6.6 **Override.** Add a per-binder test in which `WithMaxBodyBytes(16<<20)` accepts a valid 9 MiB body → `201` (Fiber also raises `BodyLimit`). Verify it passes on all three.
- [ ] 6.7 **Refactor.** Add the `413` response to the OpenAPI document. Godoc the Fiber stated limit (host `BodyLimit` applies first). Verify the OpenAPI test passes.

## 7. Routing parity (T11) and net/http Mount (T12)

- [ ] 7.1 **Red.** Port `TestUnservedPathVariants` (`$V/transport/routing_test.go`) into `runErrorCases` as a table with four rows: upper-case, trailing slash, double slash, `HEAD`. Each expects a JSON `404` (no body check for `HEAD`), no redirect, and a client that does not follow redirects. Add a gin `Engine()` harness beside the existing gin `Mount` harness. Verify it fails:
  - net/http on `//` (`307`) and `HEAD` (`200`);
  - gin-`Mount` on the trailing slash (`301`);
  - both Fiber harnesses on case, trailing slash and `HEAD` (`200`).
- [ ] 7.2 **Green, adapters.** Add the method check in all three adapters, and the Fiber path check, per design decision 5. Verify `HEAD` and the Fiber rows pass, the Fiber-`Mount` harness included.
- [ ] 7.3 **Green, constructors.**
  - `Engine()` sets `RedirectTrailingSlash`/`RedirectFixedPath` to false, and the gin-`Mount` harness sets the same, as documented.
  - `App()` sets `CaseSensitive` and `StrictRouting`.
  - Add `httptransport.CanonicalPaths`, and use it in `Handler()`.
  
  Verify 7.1 passes on every harness.
- [ ] 7.4 **Red.** Port `TestMountBesideHostRoutes` (`$V/transport/wiring_test.go`) into `transport/http/handler_test.go` (net/http only). Add rows:
  - `{base}/nothing` → contract JSON `404`;
  - the host pre-registers `/v1/` → `Mount` returns a configuration error, with no panic.
  
  Verify it panics today.
- [ ] 7.5 **Green.** `Mount` registers `{base}/` instead of `/`, and recovers registration panics into a configuration error. Add `NotFoundHandler()`. `Handler()` keeps `/`. Verify 7.4 and the whole suite pass.
- [ ] 7.6 **Refactor.** Godoc on each `Mount`: the router settings that keep parity (gin redirects; net/http `CanonicalPaths`; Fiber `BodyLimit`/`ErrorHandler`), and the partial-registration limit of net/http `Mount`. Verify with `/simplify`, then re-run `make transport-matrix`.

## 8. Checks

- [ ] 8.1 Re-run every scratch reproduction against the fixed tree (`cd $V/transport && GOWORK=off go test -count=1 -run 'TestGroupResolutionFailureBody|TestCreateRequestHandling/.*/T(8|9|14)|TestClientSuppliedIDIsAddressable|TestUnservedPathVariants|TestMountBesideHostRoutes|TestBasePathWithoutLeadingSlash' .`). Verify that no failure remains other than the T15 rows (another change) and the scratch harness's own gin/Fiber router defaults, which the ported harnesses replace as documented.
- [ ] 8.2 From the repository root, run `make lint`, `make test` and `make transport-matrix`, and verify all pass.
