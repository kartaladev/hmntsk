// Package webhook delivers hmntsk events to the callback address a caller
// supplied when the task was created.
//
// It is a [relay.Sink]: the relay decides what to deliver, when to retry and
// when to give up, and this package decides only how one attempt is made and
// what its result means. A host constructs one [Sink] and hands it to the
// relay.
//
// # The delivery
//
// Each attempt is a POST whose body is a [Payload] and whose headers carry
// enough for a receiver to route and de-duplicate without reading the body:
// the event identifier, which is stable across redeliveries; a delivery
// identifier, which is fresh for every attempt; the event and task types; and
// the correlation data the host supplied at creation. Two deliveries carrying
// one event identifier and two delivery identifiers are the same event twice,
// which is the receiver's cue to discard the second.
//
// The caller's reference parameters are echoed into the body exactly as they
// were supplied — same names, same order, same digits, same whitespace. The
// engine treats them as opaque bytes, and so does this package: see
// [Payload.MarshalJSON] for why that rules out re-encoding them.
//
// # Verifying a delivery
//
// Every delivery is signed. [Sign] covers the timestamp and the body together,
// and both are sent, so a receiver can prove the delivery came from the engine
// it shares a secret with and reject one captured and replayed later. [Verifier]
// is that check, written here so a receiver need not reimplement it — the two
// ways to get it wrong, comparing signatures with == and skipping the freshness
// window, are both easy to make and quiet when made.
//
// # Delivering to an address a caller chose
//
// A callback address is supplied by whoever created the task, which makes this
// the one component that will POST wherever it is pointed, from inside the
// host's network. Three things stand between that and an SSRF:
//
//   - A [DestinationPolicy] is consulted at dial time, against the address the
//     hostname actually resolved to. Checking the URL's text instead would be
//     defeated by a name that resolves to a public address when inspected and a
//     private one when connected to.
//   - [DefaultPolicy] refuses loopback, link-local — where the cloud metadata
//     service lives — private ranges, IPv6 unique-local, multicast and the
//     other ranges that are not routable on the public internet, in whichever
//     notation they arrive in. A host that has a legitimate internal receiver
//     supplies its own policy, which is the supported way in; see
//     [WithDestinationPolicy] and [AllowLoopback].
//   - Redirects are not followed, and no proxy is consulted. Both would send the
//     request to an address the policy never saw.
//
// A refusal is permanent: the relay dead-letters the event rather than spending
// its attempts asking a question that has already been answered.
//
// # What a response means
//
// A 2xx is delivered. A 408 or a 429 is retryable, because the receiver is
// asking for the same request again, later. Any other 4xx is permanent: the
// request was wrong and the next attempt would send the same one. Everything
// else — 5xx, a timeout, a transport error — is retryable.
package webhook
