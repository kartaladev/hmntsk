## 1. Preparation

- [x] 1.1 Run `/golang-how-to`, then load `table-test` and `golang-testing`. Confirm `/Users/zakyalvan/go/bin/gopls version` works. Verify with a green baseline: `GOTOOLCHAIN=go1.26.8 go test -count=1 ./...` passes in the core module. Never run `make tidy` or `go mod tidy`.

## 2. Snapshot in the transition (core)

- [x] 2.1 **Red.** Extend `TestTransitionsHappyPaths` (`transitions_test.go`) with assert-closure cases for the snapshot:
  - pooled activation carries the pool and the creator, with no previous holder;
  - claim has no previous holder;
  - release names the releaser;
  - delegation names the delegator;
  - start, complete, suspend and a cancel while held carry no previous holder;
  - the pool snapshot includes exclusions;
  - widening escalation carries the widened pool.

  Give the fixture a `CreatedBy` and an excluded actor. Run `GOTOOLCHAIN=go1.26.8 go test -run 'TestTransitionsHappyPaths' -count=1 .`. It must fail to compile on the missing `Event.Candidates`, `Event.PreviousAssignee` and `Event.CreatedBy` fields, and for no other reason.
- [x] 2.2 **Green.** Add `Candidates CandidatePool json:"candidates,omitzero"`, `PreviousAssignee string json:"previousAssignee,omitempty"` and `CreatedBy string json:"createdBy,omitempty"` to `Event` (`event.go`), each with a doc comment. Run 2.1 again: it compiles and fails on the assertions, which proves the cases test the snapshot rather than the struct.
- [x] 2.3 **Green.** Fill the three fields in `Task.record()` after `mutate`:
  - `Candidates` is a clone of `next.Candidates`, with empty slices normalised to nil;
  - `PreviousAssignee` is `t.Assignee` when it is non-empty and differs from `next.Assignee`;
  - `CreatedBy` is `next.CreatedBy`.

  Verify: 2.1 passes. Temporarily invert the previous-holder condition and confirm the release and delegation cases fail, then restore it.
- [x] 2.4 **Red, then green.** Add a table case to the event aliasing tests (next to `TestEveryEventCarriesCorrelation` in `event_test.go`) proving that mutating the returned task's `Candidates` slices does not change the event's snapshot. Verify it fails when the clone in 2.3 is replaced by a plain assignment, then passes with the clone.
- [x] 2.5 **Red, then green.** Add an engine-level case in `service_dispatch_test.go` proving that a dispatched claim event keeps its snapshot after the task is later delegated, which is the redelivery scenario. Verify it passes, and fails if the snapshot is read from the stored task instead.

## 3. Stores round-trip the snapshot

- [x] 3.1 **Red.** Add a conformance case to `storetest/outbox.go`: an event with a pool (users, groups, exclusions), a previous holder and a creator is appended, claimed, and read back through `OutboxEntry` with `assert.Equal` on all three fields. Add the fields to the event fixture in `storetest/fixtures.go`. Verify it fails on the store where normalisation is missing: with 2.3's normalisation temporarily removed and a non-nil empty slice, SQL stores decode nil.
- [x] 3.2 **Green.** Verify the case passes on memstore and on every SQL combination: `GOTOOLCHAIN=go1.26.8 make store-matrix`, or the store modules' integration tests with Docker running.

## 4. Sink bodies

- [x] 4.1 **Red.** In `delivery/webhook/payload_test.go`, add a case asserting that a delegation event's rendered body carries `event.candidates`, `event.previousAssignee` and `event.createdBy`. Keep the file's byte-level assertions, not `JSONEq`. Verify it fails because `PayloadEvent` lacks the fields.
- [x] 4.2 **Green.** Add `Candidates`, `PreviousAssignee` and `CreatedBy` to `webhook.PayloadEvent` with the same JSON names and omit rules, map them in `payload.go`, and update the sink's doc comments. Verify: 4.1 passes, and `delivery/webhook/sink_test.go` still passes.
- [x] 4.3 **Red, then green.** In `delivery/redis/publish_test.go`'s full-event case and `delivery/nats/sink_publish_test.go`'s "a full event" case, set the snapshot on the test event (`delivery/redis/deliver_test.go` and `delivery/nats/helpers_test.go` fixtures), and assert the decoded body carries the pool, previous holder and creator. Also assert that the Redis flat field set and the NATS header set are unchanged.
  - Verify the new assertions fail when the fixture omits the snapshot, then pass.
  - Run with Docker: `GOTOOLCHAIN=go1.26.8 go test -count=1 ./delivery/redis/... ./delivery/nats/...` from each module.
- [x] 4.4 Confirm `relaytest` needs no behaviour change: its fixture events (`relaytest/fixtures.go`) carry no audience, and relay behaviour does not read it. Verify with `GOTOOLCHAIN=go1.26.8 make relay-matrix`. If a relay case asserts whole events, extend the fixture instead of weakening the case.

## 5. Documentation

- [x] 5.1 Update `docs/delivery.md`:
  - add `candidates`, `previousAssignee` and `createdBy` to the webhook body example;
  - state that the Redis `event` field and the NATS body carry them, and that headers and flat fields do not;
  - note size growth next to the NATS `max_payload` row, and that webhook receivers now see pool identifiers.

  Verify: `docs_test.go` in the affected modules passes.
- [x] 5.2 Update the `Event` godoc and the `EventTypeDelegated` and `EventTypeReleased` comments where they describe what a consumer can learn. Verify with `GOTOOLCHAIN=go1.26.8 go doc github.com/kartaladev/hmntsk Event`, which shows the three fields documented.

## 6. Refactor and verify

- [x] 6.1 Run `/simplify` on the touched files (`event.go`, `transitions.go`, `delivery/webhook/payload.go`, and the tests). Re-run the focused tests from sections 2 to 4, which must stay green.
- [x] 6.2 Run gopls diagnostics on every touched package, which must be clean. Run the full check: `GOTOOLCHAIN=go1.26.8 make lint test test-race test-integration vuln` with Docker running, which must pass.
- [x] 6.3 Run `openspec validate event-audience-snapshot`, which must be valid. Tick every task above, and confirm each scenario in `specs/task-events/spec.md` maps to a test from sections 2 to 4.
