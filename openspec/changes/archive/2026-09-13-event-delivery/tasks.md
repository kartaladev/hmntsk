> Every task below follows `.claude/rules/golang-tdd.md`: write the failing test,
> run it and watch it fail for the intended reason, make it pass, then refactor
> (consider `/simplify`). "Verify" in a task means the test was red first.

## 1. Outbox schema and storage

- [x] 1.1 Extend the outbox row type with attempts, next-attempt time, last error, lease fields and per-sink acceptance; verify a red round-trip test on the in-memory store goes green
- [x] 1.2 Add the six columns to the PostgreSQL, MySQL and SQLite DDL, with identifier collation pinned as the existing tables are; verify the DDL applies cleanly on all three engines
- [x] 1.3 Extend `VerifySchema` to require the new columns; verify it names each missing column before the change and passes after
- [x] 1.4 Add `sqlcore` statements to claim due events by lease, record an attempt outcome, mark per-sink acceptance and mark dead-lettered; verify generated SQL and argument order per dialect
- [x] 1.5 Replace the single published-at read with a due-events read honouring next-attempt time, lease state and dead-letter state; verify a not-yet-due event is excluded and an expired lease is included

## 2. Relay conformance suite (written before the relay)

- [x] 2.1 Create the `relaytest` module exporting `RunSuite(t, factory)`; verify it fails against a deliberately empty relay implementation
- [x] 2.2 Add claiming cases: two concurrent passes attempt each event once, an expired lease is reclaimed, claiming holds with no row-level locking; verify each fails against an unleased implementation
- [x] 2.3 Add scheduling cases: oldest first, delay grows between attempts, an event is not claimed before it is due, jitter keeps two failures from sharing a next-attempt time
- [x] 2.4 Add dead-letter cases: attempts exhausted, a permanent failure skipping remaining attempts, a dead letter still readable with attempt count and last error
- [x] 2.5 Add per-sink cases: one sink failing does not redeliver to the sink that succeeded, and an event is delivered only when every sink has accepted it
- [x] 2.6 Add resilience cases: one event failing does not abandon the pass, and the failure reaches the host's error handler

## 3. The relay

- [x] 3.1 Define the `Sink` interface and the delivery outcome type; verify a sink returning an unclassified outcome is a compile-time error, not a runtime default
- [x] 3.2 Implement lease-based claiming of due events; verify the claiming cases from 2.2 turn green
- [x] 3.3 Implement exponential backoff with jitter and a ceiling; verify the scheduling cases from 2.3 turn green
- [x] 3.4 Implement dead-lettering on exhausted attempts and on permanent failure; verify the cases from 2.4 turn green
- [x] 3.5 Implement per-sink fan-out and acceptance accounting; verify the cases from 2.5 turn green
- [x] 3.6 Implement the pass result (claimed, delivered, retried, dead-lettered per sink) and the error handler; verify the cases from 2.6 turn green
- [x] 3.7 Implement the host-driven runner with no implicit startup; verify a constructed relay starts no goroutine, timer or polling until the host runs it
- [x] 3.8 Run `relaytest` against all seven driver × dialect combinations; verify identical results on each
- [x] 3.9 Refactor the relay with the suite green — consider `/simplify` — and verify the suite still passes

## 4. Webhook sink

- [x] 4.1 Create the `delivery/webhook` module with a sink that skips tasks carrying no callback address; verify no HTTP request is made and the sink reports success
- [x] 4.2 Implement the request body and headers carrying event identifier, delivery identifier and correlation data; verify a receiver can correlate and distinguish a repeat
- [x] 4.3 Implement verbatim reference-parameter echo; verify parameters with names the engine does not know arrive unchanged
- [x] 4.4 Implement HMAC signing over timestamp and body; verify a recomputed signature matches, a tampered body does not, and the timestamp is present for a freshness check
- [x] 4.5 Implement response-status classification (2xx delivered; 408 and 429 retryable; other 4xx permanent; 5xx and transport errors retryable); verify each class with a table test against a test server
- [x] 4.6 Implement the per-attempt timeout; verify an unresponsive receiver is abandoned at the timeout and classified retryable

## 5. SSRF protection

- [x] 5.1 Define the destination policy port with a default-deny implementation rejecting loopback, link-local, private, unique-local and unspecified addresses; verify each range with a table test
- [x] 5.2 Enforce the policy on the resolved address at dial time rather than on the URL string; verify a hostname resolving to an internal address is refused even though its text looks public
- [x] 5.3 Disable redirect following by default; verify a redirect toward an internal address does not result in a request to it
- [x] 5.4 Classify a policy rejection as permanent; verify such an event is dead-lettered rather than retried
- [x] 5.5 Allow a host-supplied policy to permit a chosen internal destination; verify delivery proceeds under it

## 6. Bus sink

- [x] 6.1 Create the `delivery/redis` module publishing each event with `XADD`; verify against a Redis testcontainer that the message is readable from the stream
- [x] 6.2 Publish every event regardless of callback address; verify a task with no callback address still reaches the bus and that both sinks receive an event once when both are configured
- [x] 6.3 Include event identifier, task identifier, task type, event type, occurrence time and correlation data as message fields; verify a consumer can route without a database lookup
- [x] 6.4 Classify broker unavailability, timeout and write rejection as retryable; verify a backlog survives an outage spanning several passes and publishes on the next success
- [x] 6.5 Assert the sink creates no consumer group and tracks no consumer position; verify by inspecting the broker after a pass
- [x] 6.6 Implement the per-attempt publish timeout; verify an unacknowledged write is abandoned and classified retryable

## 7. Wiring, documentation and CI

- [x] 7.1 Add relay and sink construction to the public surface with wiring-time validation of sink names and configuration; verify a duplicate sink name fails at construction rather than at the first pass
- [x] 7.2 Write package documentation for every exported type in the relay and both sinks; verify the linter's doc-comment checks pass
- [x] 7.3 Document the delivery contract a receiver implements — headers, body shape, signature verification, de-duplication, and the status codes that mean what; verify the documented example verifies a real signed delivery as an example test
- [x] 7.4 Document the SSRF default and how to override it, stating plainly what the default rejects; verify the documented policy matches the implementation by test
- [x] 7.5 Update the schema and release documentation for the new columns and two new modules, keeping the release order test passing; verify `make release-order` and its test agree
- [x] 7.6 Add CI jobs for the relay suite across all seven store combinations, the webhook sink and the Redis sink; verify the matrix runs green end to end
- [x] 7.7 Run `make lint`, `make test-integration`, `make test-race` and `make vuln` on the pinned Go 1.26 toolchain; verify all pass
