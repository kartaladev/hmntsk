// Package relay delivers the events the engine recorded durably.
//
// The engine writes an event inside the transaction that produced it and stops
// there. This package is what gets it out again: it claims due events by
// time-bounded lease so that relays in several application instances do not
// deliver the same event twice, fans each one out to every configured sink,
// retries a failure with a growing delay, and dead-letters an event that cannot
// be delivered rather than retrying it forever.
//
// Nothing here starts on its own. Constructing a relay starts no goroutine, no
// timer and no polling; the host drives it, exactly as it drives the escalation
// sweep.
//
// A sink is how an event reaches one destination, and lives in its own module
// so that the core stays free of HTTP clients and broker libraries — see
// delivery/webhook and delivery/redis.
package relay
