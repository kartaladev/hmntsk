Every behaviour task is test-first. Write the case, run it with a focused `GOTOOLCHAIN=go1.26.8 go test -run ... -count=1 .` from `delivery/nats`, and confirm it fails **for the intended reason**, never because the package does not compile. Where a test needs an exported name that does not exist yet, add it first as an inert stub (task 3.1). Where a test is written after the code, invert the implementation temporarily and confirm the test notices.

Table tests use the project's `assert` closure form and `t.Context()`. Containers come only from `RunTestNATS`. Never run `go mod tidy` or `make tidy` in this module: it imports `relay`, which no published tag contains (see `docs/releasing.md`).

## 1. Module and workspace wiring

- [ ] 1.1 Create `delivery/nats/go.mod` (`module github.com/kartaladev/hmntsk/delivery/nats`, `go 1.26.0`) with the same header comment as `delivery/redis/go.mod`, add `./delivery/nats` to `go.work`, and add `github.com/nats-io/nats.go@v1.53.1`, `github.com/testcontainers/testcontainers-go@v0.44.0`, `.../modules/nats@v0.44.0` and `github.com/stretchr/testify` with `go get`. Verify `GOTOOLCHAIN=go1.26.8 go build ./...` in the module succeeds under `go.work`, with a `doc.go` package clause as the only file.
- [ ] 1.2 Red: add `"delivery/nats"` to the hardcoded module list in the root `docs_test.go`, and confirm the release-order test fails because the Makefile and `docs/releasing.md` do not name it.
- [ ] 1.3 Green: name `delivery/nats` everywhere the workspace lists modules:
  - Makefile `MODULES`, and `RELEASE_ORDER` after `delivery/redis`;
  - `docs/releasing.md`: a tag-table row, the numbered release-order block (`12. delivery/nats  depends on core  (+ relaytest, for tests)`, transports renumbered 13–15), and the list of modules importing `relay` in the `make tidy` warning;
  - README: the module table, and the sinks table;
  - the CI `delivery` matrix in `.github/workflows/ci.yml`.

  Verify the root docs test passes and `make release-order` prints the new order.

## 2. Test infrastructure

- [ ] 2.1 Add `RunTestNATS(t, opts ...TestOption) *nats.Conn` to `delivery/nats/testutils.go` (design D10):
  - pinned `NATSImage = "nats:2.12.7-alpine"`, with the command overridden to `-js`;
  - termination registered with `t.Cleanup` immediately, using a fresh context;
  - options for image, startup timeout and extra server arguments (needed for `--max_payload` in 4.5).

  Test-first, in the JetStream suite's `SetupSuite` (5.1): `jetstream.New(conn).AccountInfo` succeeds. Confirm it fails when the helper temporarily starts the server without `-js`.
- [ ] 2.2 Add a switchable TCP proxy test helper for outage cases, modelled on `delivery/redis`'s `brokerProxy`, which is in another module's test package and cannot be imported. Verify it through 4.4, whose closed-proxy case must fail if the proxy forwards while closed.

## 3. Construction and the shared contract

- [ ] 3.1 Add inert stubs so tests compile, and verify `go build ./...`:
  - `Option`, `JetStreamOption`, `WithName`, `WithSubjectPrefix`, `WithTimeout`, `WithExpectStream`;
  - `NewSink(*nats.Conn, ...Option)` and `NewJetStreamSink(jetstream.JetStream, ...JetStreamOption)`;
  - `DefaultName = "nats"`, `DefaultJetStreamName = "jetstream"`, `DefaultSubjectPrefix = "hmntsk.events"`, `DefaultTimeout = 5 * time.Second`;
  - `Schema = "hmntsk.nats.event.v1"`, and the `Header*` constants with the webhook sink's values plus `Hmntsk-Attempt` and `Hmntsk-Schema` (design D4);
  - `ErrConfiguration`, `ErrInvalidEvent`, `ErrPublish` and their error types.

  Shared options must be accepted by both constructors, and JetStream-only options must not compile with `NewSink` (D2).
- [ ] 3.2 Red: add a `TestNew` table covering both constructors.
  - Rejecting cases, each asserting `ErrorIs(ErrConfiguration)` and a nil sink: nil connection, nil JetStream context, empty name, zero and negative timeout, and the prefixes `""`, `"a..b"`, `".a"`, `"a."`, `"a.*"`, `"a.>"`, `"a b"` and `"a\tb"`.
  - Accepting cases: defaults for each constructor (names `nats` and `jetstream`, which differ), a custom name, and the prefix `acme.tasks`.

  Confirm the rejecting cases fail because the stubs accept everything.
- [ ] 3.3 Green: validate in both constructors, with `ConfigurationError.Detail` saying how to fix each mistake. `TestNew` passes.

## 4. Plain-subject sink against a real server

- [ ] 4.1 Red: add a `plainSuite` on one `RunTestNATS` server. A subscriber on `hmntsk.events.>` receives the message published by `NewSink`. Cases:
  - a full event: subject `hmntsk.events.task.completed`, every header with its documented value, and a body that unmarshals to the recorded event;
  - no correlation data: no correlation headers;
  - a task type containing a space and a `*`: subject unaffected;
  - a task type containing `\n`: the header holds a space, the body the original;
  - prefix `acme.tasks`: subject `acme.tasks.task.claimed`.

  Confirm these fail because the stub publishes nothing.
- [ ] 4.2 Red: with no subscriber on the subject, `Deliver` returns `OutcomeDelivered`. Confirm it fails against the stub's unclassified outcome.
- [ ] 4.3 Green: render subject, headers and body, then `PublishMsg` followed by `FlushWithContext` under the sink's timeout (D5). 4.1 and 4.2 pass.
- [ ] 4.4 Red then green: outage cases through the proxy (2.2).
  - With the proxy closed, `Deliver` returns `OutcomeRetryable` matching `ErrPublish` within the timeout, and is not delivered.
  - After the proxy opens, the same event is delivered.
  - A cancelled context is retryable.

  Verify by inversion: temporarily skip the flush and confirm the closed-proxy case now wrongly reports delivered.
- [ ] 4.5 Red then green: permanent failures.
  - An event with no identifier returns `OutcomePermanent` matching `ErrInvalidEvent`, and nothing is published.
  - With a server started with a small `--max_payload`, an oversized event returns `OutcomePermanent`, and nothing is published.

  Then implement the classification table in design D8 for the plain sink.

## 5. JetStream sink against a real server

- [ ] 5.1 Red: add a `jetStreamSuite` whose `SetupSuite` asserts JetStream is available (2.1). Each case creates its own stream as the host would, capturing a unique prefix.

  Case: `NewJetStreamSink` publishes an event, `Deliver` is delivered, and the stream holds one message whose `Nats-Msg-Id` equals the event ID and whose headers and body match 4.1's contract.

  Confirm it fails against the stub.
- [ ] 5.2 Red: the same event delivered twice within the stream's duplicate window. Both attempts are delivered, and the stream holds one message.
- [ ] 5.3 Red: with no stream capturing the subject:
  - `Deliver` returns `OutcomeRetryable` matching `ErrPublish` whose cause is `jetstream.ErrNoStreamResponse`;
  - the server's stream list is unchanged;
  - the attempt returns in less than `jetstream.DefaultPubRetryWait` (250 ms), which proves a single publication.
- [ ] 5.4 Red: with `WithExpectStream("expected")` and the subject captured by a different stream, `Deliver` is retryable and neither stream stores the message.
- [ ] 5.5 Green: `PublishMsg` with `WithRetryAttempts(0)`, `WithMsgID(event.ID)` and the optional `WithExpectStream`; a duplicate acknowledgement counts as delivered; classification per D8 (D6, D7). 5.1–5.4 pass.

  Verify by inversion:
  - remove `WithRetryAttempts(0)` and confirm 5.3's timing assertion fails;
  - use the delivery ID as the message ID and confirm 5.2 fails.
- [ ] 5.6 In the same suite, the permanent cases from 4.5 hold for the JetStream sink: no identifier, and oversized against a small `--max_payload` server. Seen failing by temporarily classifying both as retryable.

## 6. Documentation

- [ ] 6.1 Red: add `delivery/nats/docs_test.go`, modelled on `delivery/redis/docs_test.go`, scoped to a `# Publishing to NATS` section of `docs/delivery.md`. It asserts the section names:
  - every `Header*` value, `Schema` and `DefaultSubjectPrefix`;
  - both default sink names;
  - both constructors and every option, taken from the functions;
  - the text of `jetstream.ErrNoStreamResponse`.

  Confirm it fails because the section does not exist.
- [ ] 6.2 Green: write the section. It covers:
  - the mode table from design D1;
  - that plain-subject delivery loses an event with no subscriber;
  - the subject layout and why the task type is not in it;
  - headers as routing hints and the body as authoritative, including CR/LF sanitising;
  - that the JetStream sink never creates streams, and that a forgotten stream retries until dead-lettered, naming the error;
  - the duplicate window and consumer de-duplication;
  - the sink-name trap when switching modes;
  - `WithExpectStream`.

  6.1 passes, and the webhook and Redis docs tests stay green.
- [ ] 6.3 Write the package doc and godoc stating each mode's guarantee where it is chosen: `NewSink` names the lost-without-subscriber case, `NewJetStreamSink` names the never-creates-streams rule. Point both to `docs/delivery.md`. Verify with `go doc -all .` in `delivery/nats` and `GOTOOLCHAIN=go1.26.8 make lint`.

## 7. Verification

- [ ] 7.1 Run `/simplify` on the touched `delivery/nats` code, then re-run the module's tests with `-race`.
- [ ] 7.2 `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln` is green across all 15 modules. `openspec validate nats-delivery --strict` passes.
- [ ] 7.3 On the PR, the new `delivery delivery/nats` CI job runs and passes alongside the existing 24 checks.
