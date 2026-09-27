# Proposal

## Why

An audit of the delivery sinks found three defects, each reproduced by a failing test against the public surface. They are in the scratch module `verify/relay`, and `design.md` names each one. The Redis sink can append one event several times in a single relay attempt. The webhook `Verifier` accepts a delivery whose documented de-duplication header was forged. A secret in a callback URL's query string is written to `last_error` and passed to the relay's error handler. None of these modules has been tagged, so the signature contract and the error text can still change without a compatibility cost. After the first tag they cannot.

## What Changes

**Redis sink: one publication per attempt (R5)**

- `redis.New` inspects the client's retry settings where go-redis exposes them (`*Client`, which includes failover clients, `*Ring` and `*ClusterClient`). It refuses a client that would re-send a failed `XADD` inside one attempt, and reports this as an `ErrConfiguration` naming `MaxRetries: -1`. **BREAKING for wiring**: a client built with go-redis defaults (`MaxRetries` 3) no longer constructs a sink.
- A new option, `WithClientRetries()`, lets a host accept the extra publications on purpose. The requirement and the option are documented on `New` and in `docs/delivery.md`.

**Webhook: headers cannot contradict the signed body (R7)**

- `Verifier.Verify` rejects a delivery when any identity header (`Hmntsk-Event-Id`, `Hmntsk-Delivery-Id`, event type, task id, task type, correlation) disagrees with the value in the signed body. The error matches `ErrSignatureMismatch`. The `v1` signed material stays timestamp + body.
- `docs/delivery.md` and the package doc change to say that a receiver de-duplicates on the **verified** event id. The headers are a routing convenience, and they are trusted only after `Verify` or a check against the body.

**Webhook: callback URLs are redacted in errors (R11)**

- Every error the sink produces shows the callback address with its userinfo, query and fragment removed. That covers transport, status, redirect, policy and address errors, and the text reaches both `last_error` and the relay's error handler. The `*url.Error` that repeated the full URL (`Post "…?token=…"`) no longer reaches the error text.
- A new option, `WithAddressRedactor(func(*url.URL) string)`, replaces the default redaction. A host can use it to hide path secrets as well, or to keep more detail.

**Relay: a sink's name appears once (R11)**

- The relay no longer writes `webhook: webhook: …` or `redis: redis: …`. When a sink's message already begins with `<sink>: `, the relay does not add the prefix again. This applies to both `last_error` and the error-handler text.

**Webhook: TLS configuration (R6, observation)**

- A new option, `WithTLSConfig(*tls.Config)`, supplies a private CA, a client certificate for mTLS or a minimum TLS version. The sink keeps its own policy-enforcing dialler. A config that turns off certificate verification and supplies no verification callback of its own is refused at construction.

### Non-goals

- Redirect and SSRF behaviour. The audit verified it as sound, and it stays unchanged.
- Changing the `v1` signed material, or adding a signature scheme with a new version.
- Consumer-side de-duplication helpers.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `webhook-delivery`: the verifier checks identity headers against the signed body, and de-duplication is specified on the verified body. Recorded failures no longer reveal secrets from the callback address. A host can configure TLS.
- `redis-delivery`: one delivery attempt publishes at most once, and a client that would retry internally is refused at construction unless the host opts in.
- `event-relay`: the recorded failure names each refusing sink once.

## Impact

- **Code**: `delivery/redis` (`New`, a new option, errors), `delivery/webhook` (`Verifier.Verify`, error construction, redaction, two new options), and `relay` (how failures are formatted).
- **Docs**: `docs/delivery.md` (the headers table, "De-duplicating", the Redis client requirements, TLS) and the package docs for both sinks.
- **Compatibility**: none of these modules is tagged yet. The Redis wiring change and the verifier's stricter check are recorded here as deliberate changes to defaults, as `library-design.md` rule 7 requires.
- **Tests**: each defect's scratch reproduction is ported into the repo as its red step. The Redis tests use `RunTestRedis`.
