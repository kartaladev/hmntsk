# Tasks

> Every task follows `.claude/rules/golang-tdd.md` and `.claude/rules/error-reproducible.md`. Each defect starts by porting its scratch reproduction into the repo, next to the code it covers, in `table-test` form with the `assert` closure and `t.Context()`. The ported test is run and must fail for the reason the finding states (red). Then the smallest fix makes it pass (green), and a refactor follows (consider `/simplify`). Redis tests use the module's `RunTestRedis` testcontainers helper, never a fake. The scratch module is `verify/relay` in the session scratchpad.

## 1. R5: the Redis sink publishes at most once per attempt

- [ ] 1.1 Port `r5_redis_test.go` › `TestR5RedisOnePublicationPerAttempt` into `delivery/redis/deliver_test.go` as `TestSinkPublishesAtMostOncePerAttempt`. Bring the `dropFirstXAddReply` proxy with it, as a test helper in that file, and keep both cases: the go-redis defaults, and the control with `MaxRetries: -1`. Run `go test -run TestSinkPublishesAtMostOncePerAttempt -count=1 ./delivery/redis/...` and confirm the defaults case fails with "one Deliver attempt appended 2 stream entries" (red). Confirm the control case passes.
- [ ] 1.2 Change the defaults case to assert that `New` fails with `ErrConfiguration` and that the detail names `MaxRetries: -1`. Add cases for a `*Ring` and a `*ClusterClient` whose `MaxRetries` is positive (construction refused), and for a failover `*Client` (refused). Add a case for a non-go-redis `UniversalClient` wrapper, which is accepted. Run them and see the refusal cases fail (red).
- [ ] 1.3 Add a `WithClientRetries()` case: a retrying client constructs, and the stream may hold more than one entry per attempt. Keep the control case asserting exactly one entry after the severed reply. Temporarily invert the control's client to the defaults and confirm the test notices.
- [ ] 1.4 Implement the client inspection in `New` and the `WithClientRetries()` option (design D1). The option's godoc names the default it replaces, and the godoc on `New` states the cluster `MaxRedirects` limit and the requirement for clients the sink cannot inspect. Verify that 1.1–1.3 are green and that the existing `delivery/redis` tests still pass.
- [ ] 1.5 Update every existing test and example in `delivery/redis` that builds a client with defaults to use `MaxRetries: -1`. Verify with `go test -count=1 ./delivery/redis/...`.
- [ ] 1.6 Refactor with the tests green (consider `/simplify`), then re-run `go test -count=1 ./delivery/redis/...`.

## 2. R7: identity headers must agree with the signed body

- [ ] 2.1 Port `r7_r11_webhook_test.go` › `TestR7SignatureCoversDedupHeader` into `delivery/webhook/signature_test.go`, as new cases of `TestVerifierVerify` or as `TestVerifierRejectsForgedIdentityHeaders`. Keep the untouched, forged `Hmntsk-Event-Id` and forged `Hmntsk-Delivery-Id` cases. Run `go test -run 'TestVerifier' -count=1 ./delivery/webhook/...` and confirm that the forged cases fail with "Expected error with \"webhook: signature does not match\" in chain but got nil" (red).
- [ ] 2.2 Add cases for forged event-type, task-id, task-type and correlation headers (each rejected), an absent correlation header (accepted), and a correctly signed non-JSON body (rejected as a mismatch). Run them and see the rejection cases fail (red).
- [ ] 2.3 Implement the post-HMAC identity check in `Verifier.Verify` (design D2), with an error that matches `ErrSignatureMismatch` and names the header. Verify that 2.1–2.2 are green and that `TestSign` and the existing `TestVerifierVerify` cases still pass.
- [ ] 2.4 Update the docs. In `docs/delivery.md`, fix the headers table row and "De-duplicating" so they say to de-duplicate on the verified `event.id`. In `delivery/webhook/doc.go`, change the de-duplication paragraph the same way, and update the godoc on `Verify` to list the identity check. Verify with `go test -count=1 ./delivery/webhook/...`, which runs `docs_test.go`, and by checking that no remaining text calls the header alone the de-duplication key.
- [ ] 2.5 Refactor with the tests green (consider `/simplify`), then re-run the webhook tests.

## 3. R11: callback URLs are redacted in every error

- [ ] 3.1 Port `r7_r11_webhook_test.go` › `TestR11CallbackQuerySecretRedacted` into `delivery/webhook/sink_test.go` as `TestSinkDeliverRedactsCallbackAddress`. Keep the three cases: refused by the default policy, connection refused, and a 500 response. Assert that neither `Outcome.Err.Error()` nor the relay's `last_error` and error-handler text contains the token. For the relay-level assertions, drive a real relay over the in-memory store, as the scratch fixture does. Run `go test -run TestSinkDeliverRedactsCallbackAddress -count=1 ./delivery/webhook/...` and confirm each case fails with "should not contain \"s3cr3t-token\"" (red).
- [ ] 3.2 Add cases for userinfo and a fragment, both URLs of a refused redirect (with a token in the `Location`), an unparseable address that carries a token, and a timeout. Add an override case: `WithAddressRedactor` that hides the path, where the error text equals the redactor's output. Run them and see them fail (red).
- [ ] 3.3 Implement the default redaction, `TransportError`, the `AddressError` change and `WithAddressRedactor` (design D3). Keep `errors.Is` working for `ErrDestinationRefused`, `ErrRedirect` and `context.DeadlineExceeded`, and check that with the existing `TestSinkDeliver*` tests. Verify that 3.1–3.2 are green.
- [ ] 3.4 Refactor with the tests green (consider `/simplify`), then re-run the webhook tests.

## 4. The relay names each sink once

- [ ] 4.1 In `relay/relay_test.go`, add `TestRelayRecordsEachSinkNameOnce` with a stub sink, as a table. Case 1: sink `webhook`, message `webhook: boom`, expecting a `last_error` of `webhook: boom`. Case 2: sink `partner-hook` with the same message, expecting `partner-hook: webhook: boom`. Case 3: the same prefix rule for the error-handler text. This reproduces the doubled `webhook: webhook:` seen in the R11 scratch output. Run `go test -run TestRelayRecordsEachSinkNameOnce -count=1 ./relay/...` and confirm case 1 fails with `webhook: webhook: boom` (red).
- [ ] 4.2 Implement the rule in the relay's failure formatting and error-handler wrapper (design D4), and update the godoc on `OutboxEntry.LastError`. Verify that 4.1 is green, and that the relay conformance suite (`relaytest`) still passes across every store combination.
- [ ] 4.3 Refactor with the tests green (consider `/simplify`), then re-run the relay tests.

## 5. R6: TLS configuration for the webhook sink

- [ ] 5.1 Add `TestSinkDeliverTLS` to `delivery/webhook/sink_test.go`, as a table using `httptest.NewTLSServer` or `NewUnstartedServer` with client auth. The cases are:
  - The default config fails against a private-CA receiver, and the failure is retryable.
  - `WithTLSConfig` with that CA in `RootCAs` delivers.
  - With a client certificate, the receiver observes the certificate.
  - A custom TLS config with the default policy still refuses a loopback destination permanently.

  Add `WithTLSConfig` cases to `TestNew`: `InsecureSkipVerify` without a callback is refused with `ErrConfiguration`, and `InsecureSkipVerify` with `VerifyPeerCertificate` is accepted. Run them and see them fail, since they do not compile until the option exists. Once a stub option compiles, see them fail on behaviour (red).
- [ ] 5.2 Implement `WithTLSConfig` (design D5): clone the config into the transport, and validate it in `New`. The godoc names the default. Document TLS in `docs/delivery.md`. Verify that 5.1 is green.
- [ ] 5.3 Refactor with the tests green (consider `/simplify`), then re-run the webhook tests.

## 6. Checks

- [ ] 6.1 Run `go test -race -count=1` over every changed module (`./relay/...`, `./delivery/redis/...` and `./delivery/webhook/...`, from the workspace root), plus the NATS sink module for the relay prefix change. Confirm all pass.
- [ ] 6.2 Run the R5 case with `-count=20` and confirm it is deterministic.
- [ ] 6.3 Run `golangci-lint run` over the changed modules and confirm there are no new findings. Run gopls diagnostics on the changed files and confirm they are clean.
- [ ] 6.4 Run `openspec validate delivery-sink-hardening --strict` and confirm it passes.
