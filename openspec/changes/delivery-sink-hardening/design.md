# Design

## Context

For motivation, see proposal.md under Why. Each defect has a failing reproduction in the scratch module `verify/relay`, stored in the session scratchpad. The scratchpad sits outside the repo and is not preserved, so every task below ports the test into the repo before anything else is changed.

| Finding | Scratch test | Failure observed |
| --- | --- | --- |
| R5 | `r5_redis_test.go` › `TestR5RedisOnePublicationPerAttempt` | One `Deliver` appended 2 stream entries under the same `deliveryId`. A proxy severs the connection after the `XADD` reply, and the go-redis default of `MaxRetries` 3 re-sends. The control case with `MaxRetries: -1` passes. |
| R7 | `r7_r11_webhook_test.go` › `TestR7SignatureCoversDedupHeader` | `Verify` returns nil for a delivery whose `Hmntsk-Event-Id` or `Hmntsk-Delivery-Id` header was replaced. |
| R11 | `r7_r11_webhook_test.go` › `TestR11CallbackQuerySecretRedacted` | `last_error` reads `webhook: webhook: post to http://127.0.0.1:9/hook?token=s3cr3t-token: Post "http://…?token=s3cr3t-token": dial tcp …`. The error-handler text leaks the token in the same way. |
| R6 | none (observation) | The webhook sink builds its own `http.Transport` and exposes no TLS setting. |

Current state that shapes the fixes:

- `redis.New` accepts any `goredis.UniversalClient` and documents that "retry policy [is] configured where it is built". go-redis normalises `Options.MaxRetries` at construction: 0 becomes 3, and -1 becomes 0. `(*Client).Options()`, `(*Ring).Options()` and `(*ClusterClient).Options()` expose the normalised values. `NewFailoverClient` returns a `*Client`. A `*ClusterClient` retries network errors inside its `MaxRedirects` loop, which also follows `MOVED`/`ASK`. Its per-node `MaxRetries` defaults to disabled.
- The NATS JetStream sink already turns off client retries with `jetstream.WithRetryAttempts(0)`, so the Redis sink is the outlier.
- The webhook body already carries `deliveryId`, `event.id`, `event.type`, `event.taskId`, `event.taskType` and `correlation`. `setHeader` omits a header whose value it cannot carry.
- `classifyError` wraps the `*url.Error` from `http.Client.Do`, and that error prints the full URL. `url.URL.Redacted()` masks only the userinfo password. `AddressError.Address` holds the raw address.
- The relay formats a part of `last_error` as `sink + ": " + cause.Error()` (relay/relay.go:580). Every sink error begins with its package prefix (`webhook:`, `redis:`), so under the default names the prefix appears twice.

## Goals / Non-Goals

**Goals:**

- Each defect's reproduction lives in the repo, and it fails before the fix and passes after.
- Every new behaviour has a safe default and a documented override (`library-design.md`).

**Non-Goals:**

- Idempotent `XADD`, whether through Lua, `SET NX` guards or explicit entry IDs.
- Changing the `v1` signed material.
- Any change to the SSRF policy or the redirect refusal. Both were verified sound.

## Decisions

### D1. R5: refuse a retrying client at construction; the opt-in is `WithClientRetries()`

**Default.** `New` checks the concrete client type and refuses the ones it can prove would retry:

- `*goredis.Client`, which includes failover clients: refused when `Options().MaxRetries > 0`.
- `*goredis.Ring`: refused when `Options().MaxRetries > 0`.
- `*goredis.ClusterClient`: refused when `Options().MaxRetries > 0`, which covers per-node retries. The cluster's `MaxRedirects` loop is **not** inspected. It also follows `MOVED`/`ASK`, and turning it off breaks cluster routing. This limit is stated on `New` and in the docs, following rule 4.
- Any other `UniversalClient`: accepted. The godoc on `New` states that the host must build it without retries.

The refusal is a `*ConfigurationError` whose detail names the fix: build the client with `MaxRetries: -1`. `redis.New` already reports its wiring mistakes as `ErrConfiguration`, so this follows rule 6.

**Override.** `WithClientRetries()` skips the check. Its godoc states the cost: one attempt may append the event more than once under one `deliveryId`, and the `attempt` field no longer counts publications. At-least-once delivery and de-duplication on `eventId` still hold.

**Alternatives considered.**

- Build a retry-free client from the host's `Options()`. That creates a second pool the sink would have to close, and the package promises it "neither dials nor closes".
- A go-redis hook. Hooks wrap the retrying `process`, so a hook cannot see a retry.
- Idempotent publishing with a Lua script and a guard key. That adds keys, breaks under cluster slot rules unless hash-tagged, and is out of proportion to the problem.
- Document the requirement only. That silently accepts a configuration that breaks the relay's attempt budget, which rules 4 and 6 forbid.

**Compatibility.** A host that passes a client built with go-redis defaults now gets a construction error. That is free before the first tag and is recorded here (rule 7).

### D2. R7: the verifier checks identity headers against the signed body; `v1` material is unchanged

**Default.** After the signature and freshness checks pass, `Verify` decodes only the identity fields of the body: `deliveryId`, `event.id`, `event.type`, `event.taskId`, `event.taskType` and `correlation.*`. Every identity header that is present must equal the matching field. A mismatch returns an error that matches `ErrSignatureMismatch`. The scratch test already asserts that sentinel, so no new sentinel is needed. The error text names the header. An absent header is not a mismatch, because the sink omits values a header cannot legally carry. A body that is not JSON, once its signature has verified, is also a mismatch. The engine never sends one.

The docs change as well. The headers table no longer calls `Hmntsk-Event-Id` "the de-duplication key". "De-duplicating" says to discard repeats on `event.id` from a verified body, or on the header only after `Verify`. A hand-rolled receiver in another language keeps the simple `timestamp.body` HMAC and de-duplicates on the body.

**Override.** None. Turning the check off could only accept a delivery the engine never sent. A receiver that wants only the HMAC can call `Sign` and compare the result itself, which is the documented hand-rolled path. The check is not configurable for that reason.

**Alternatives considered.**

- Sign a canonical header set, as a `v2` scheme or by redefining `v1`. That would also cover the headers for hand-rolled receivers, but every receiver in every language would have to canonicalise headers exactly. The sink also omits headers conditionally, so the signed set would vary from one delivery to the next. The body already holds every identity value, so signing it is enough if receivers are pointed at it.
- Change the docs only. That leaves the provided `Verifier` accepting forged headers, and the finding is confirmed against the `Verifier`.

### D3. R11: one redaction function, used by every error that names an address

**Default.** A package-level redaction renders `scheme://host[:port]/path`. It drops userinfo, query and fragment completely and adds no placeholder. Every error that carries an address uses it: `StatusError.Address`, the transport failure, both URLs in the redirect refusal, and `AddressError`. `classifyError` stops wrapping the `*url.Error`'s text. It builds a new `TransportError{Address, Cause}`, and `Cause` is the `*url.Error`'s inner `Err`, so `errors.Is` on `ErrDestinationRefused`, `ErrRedirect`, `context.DeadlineExceeded` and `net.OpError` still works. For an address that will not parse, `AddressError` records only the reason, taken from `url.Error.Err`, and never the raw text. `AddressError.Address` is set to the redacted form, or left empty when the address does not parse.

**Override.** `WithAddressRedactor(func(*url.URL) string)` replaces the default. One use is hiding path secrets, as in Slack-style webhook URLs. The default keeps the path because it matters for diagnosis, and this trade-off is written on the option. A nil redactor keeps the default, matching the nil handling of `WithDestinationPolicy` and `WithClock`.

**Alternatives considered.**

- Use `URL.Redacted()`. It leaves the query in place, which is the finding.
- Mask query values but keep the keys. Key names can themselves be sensitive, and they help little with diagnosis.

### D4. The relay names each sink once

**Default.** When the relay records a failure for sink `name`, it writes the message unchanged if the message already begins with `name + ": "`. Otherwise it writes `name + ": " + message`. The error-handler wrapper follows the same rule. `OutboxEntry.LastError`'s godoc changes to say this. The change is in the relay rather than in each sink, because the prefix was doubled for every sink and not only the webhook sink.

**Override.** `WithName` on either sink. A renamed sink no longer matches its own package prefix, so its name is written in full, and the name remains what identifies the sink.

**Alternative considered.** Drop the package prefix from sink errors. That breaks the convention every error in both packages follows, and it makes a sink's error unclear when it is logged outside the relay.

### D5. R6: `WithTLSConfig(*tls.Config)` on the webhook sink (observation addressed)

Rule 2 requires every default to be replaceable without forking. Today the webhook sink's TLS behaviour is fixed, so a private CA or mTLS receiver is out of reach. The sink deliberately refuses a caller-supplied `http.Client` (its godoc on `New`), so this change adds the smallest override that keeps the dialler.

**Default.** `nil`, which means Go's default client TLS: system roots and Go's minimum version.

**Override.** The host's config is cloned at construction and set as `Transport.TLSClientConfig`. The dialler, the policy, `Proxy: nil` and the redirect refusal do not change.

**Limit (rule 4).** `InsecureSkipVerify: true` without `VerifyPeerCertificate` or `VerifyConnection` is a `ConfigurationError`. With either callback set, the config is accepted, because the host has replaced verification rather than removed it. That covers certificate pinning.

**Alternative considered.** Accept an `http.RoundTripper`. That reopens the policy bypass that `New` rules out.

## Risks / Trade-offs

- [Existing Redis wiring breaks at startup] → The error text names the exact fix (`MaxRetries: -1`), and `docs/delivery.md` shows it in the Redis example. Before the first tag no host is affected.
- [Cluster clients can still re-send an `XADD` on a network error through `MaxRedirects`] → The limit is stated, not hidden. Consumers already de-duplicate on `eventId`.
- [The verifier now parses JSON] → Only after the HMAC and freshness checks pass, so an unauthenticated body is never parsed. The decode reads only the identity fields.
- [The default redaction keeps paths, which may hold secrets] → The limit is documented, and `WithAddressRedactor` is the override.
- [The relay change affects NATS sinks too] → Intended. The NATS error texts begin `nats:`, and they are deduplicated the same way under their default names.

## Migration Plan

No schema change and nothing to migrate. Rollback is a revert. The recorded compatibility decisions (rule 7) are:

1. The Redis sink refuses retrying clients by default.
2. The verifier is stricter.
3. The `last_error` text is shorter.

## Assumptions and Open Questions

- Assumption (the author was not asked): R7 is fixed in the verifier and the docs rather than by signing headers, because the body already carries every identity value and the scheme stays easy to reimplement.
- Assumption: R6 is in scope as an option rather than recorded as "no override", because rule 2 applies and the change is small.
- Assumption: dropping the doubled prefix is done in the relay, and so applies to every sink.
- Open question: whether a `*ClusterClient`'s network-error retry through `MaxRedirects` actually re-sends `XADD` in practice. No cluster testcontainer helper exists. The limit is documented either way, so the answer changes neither the specs nor the tasks.
