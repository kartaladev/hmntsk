## MODIFIED Requirements

### Requirement: Deliveries carry correlation identifiers

Every delivery SHALL carry a stable identifier for the event and a distinct identifier for this delivery attempt, so a receiver can correlate a notification to the work that caused it and discard a repeat without keeping its own correlation store. Both identifiers SHALL be carried in the signed body. When the same identifiers are also sent as request headers, the headers SHALL be documented as a routing convenience. The documented de-duplication key SHALL be the event identifier in a verified delivery, and a header value SHALL be relied on only after the delivery has been verified.

#### Scenario: A receiver de-duplicates a repeat

- **WHEN** the same event is delivered twice
- **THEN** both carry the same event identifier and different delivery identifiers, in the signed body and in the headers alike

#### Scenario: Correlation data accompanies the notification

- **WHEN** a delivery arrives for a task created with correlation data
- **THEN** the owner type, owner reference and activity key are readable from the delivery

#### Scenario: The documented de-duplication key is covered by the signature

- **WHEN** a receiver follows the documented guidance for discarding repeats
- **THEN** the value it de-duplicates on is one the signature covers, either directly or because the verifier has checked it against the signed body

### Requirement: Deliveries are signed

The system SHALL sign every delivery with a keyed hash over the request body and a timestamp, and SHALL include both the signature and the timestamp with the request, so a receiver can verify the delivery originated from this engine and reject a replayed one. The verifier the system provides SHALL also reject a delivery whose identity headers disagree with the signed body. The identity headers are the event identifier, the delivery identifier, the event type, the task identifier, the task type and the correlation values. An identity header that is absent SHALL NOT cause a rejection, because the sink omits a value that a header cannot legally carry.

#### Scenario: A receiver verifies a delivery

- **WHEN** a delivery arrives and the receiver recomputes the signature with the shared key
- **THEN** the computed signature matches the one sent

#### Scenario: A tampered body fails verification

- **WHEN** the body of a delivery is altered in transit
- **THEN** the signature no longer matches what the receiver computes

#### Scenario: A replayed delivery is detectable

- **WHEN** a delivery is captured and re-sent later
- **THEN** its timestamp is unchanged, so a receiver enforcing a freshness window can reject it

#### Scenario: A forged event identifier header is rejected

- **WHEN** a captured delivery is re-sent inside the freshness window with its event identifier header changed, and the body and signature are left untouched
- **THEN** the provided verifier rejects it as a signature mismatch

#### Scenario: A forged delivery identifier header is rejected

- **WHEN** a captured delivery is re-sent inside the freshness window with its delivery identifier header changed
- **THEN** the provided verifier rejects it as a signature mismatch

#### Scenario: An omitted header is not a mismatch

- **WHEN** a delivery arrives without a correlation header because its value could not be carried in a header
- **THEN** the provided verifier accepts it, and the value is still readable from the signed body

## ADDED Requirements

### Requirement: Recorded failures do not reveal secrets from the callback address

A callback address may carry credentials in its userinfo, query string or fragment. So the text of every error the webhook sink reports SHALL show the address with its userinfo, query and fragment removed. That text is recorded as the outbox entry's last error and passed to the relay's error handler. This SHALL hold for policy refusals, transport failures, timeouts, non-success statuses, refused redirects (both the original and the redirect target) and unusable addresses. An address that cannot be parsed SHALL NOT appear in the error text at all. A host SHALL be able to replace the default redaction with its own.

#### Scenario: A query-string token is not recorded when the policy refuses the destination

- **WHEN** delivery to a callback address carrying `?token=<secret>` is refused by the default destination policy
- **THEN** neither the recorded last error nor the error passed to the relay's error handler contains the secret

#### Scenario: A query-string token is not recorded when the connection fails

- **WHEN** delivery to a callback address carrying `?token=<secret>` fails because the connection is refused
- **THEN** neither the recorded last error nor the error passed to the relay's error handler contains the secret

#### Scenario: A query-string token is not recorded when the receiver fails

- **WHEN** the receiver at a callback address carrying `?token=<secret>` answers 500
- **THEN** neither the recorded last error nor the error passed to the relay's error handler contains the secret

#### Scenario: Userinfo and fragment are removed

- **WHEN** delivery fails to a callback address carrying a user, a password and a fragment
- **THEN** the error text names the scheme, host and path, and none of the user, password or fragment

#### Scenario: A host supplies its own redaction

- **WHEN** a host constructs the sink with its own address redaction, for example one that also hides the path, and a delivery fails
- **THEN** the error text shows the address exactly as the host's redaction rendered it

### Requirement: A host can configure TLS for deliveries

The webhook sink SHALL verify the receiver's certificate against the system's trusted roots by default. A host SHALL be able to supply its own TLS configuration, including trusted roots, a client certificate for mutual TLS and a minimum protocol version, without replacing the sink's HTTP client or weakening the destination policy. A configuration that disables certificate verification without supplying a verification callback of its own SHALL be rejected at construction.

#### Scenario: Default TLS trusts only the system roots

- **WHEN** a sink built without a TLS configuration delivers to an HTTPS receiver whose certificate is signed by a private authority
- **THEN** the attempt fails and is retryable, and no request body reaches the receiver

#### Scenario: A host trusts a private certificate authority

- **WHEN** a host supplies a TLS configuration whose trusted roots include the receiver's private authority
- **THEN** delivery to that receiver succeeds

#### Scenario: A host presents a client certificate

- **WHEN** the receiver requires a client certificate and the host's TLS configuration supplies one
- **THEN** delivery succeeds, and the receiver observes that certificate

#### Scenario: The destination policy still applies with a custom TLS configuration

- **WHEN** a host supplies a TLS configuration and a callback address resolves to an address the default policy refuses
- **THEN** no connection is made and the failure is permanent

#### Scenario: Disabling verification is refused at construction

- **WHEN** a host supplies a TLS configuration that skips certificate verification and supplies no verification callback of its own
- **THEN** the sink is not constructed, and the error reports a configuration mistake
