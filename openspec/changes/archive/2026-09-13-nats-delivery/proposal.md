## Why

The relay can deliver to a webhook or a Redis Stream, and nothing else. The event-delivery change built the sink interface expecting a NATS sink next, but none exists, so a host whose internal consumers already use NATS has to write and maintain its own. hmntsk is a library, and those hosts do not all use NATS the same way: some consume through JetStream streams, others subscribe to plain subjects with no stream at all. A sink that supported only one would push the other half back to writing their own.

## What Changes

- **A new `delivery/nats` module** with two relay sinks, one per way a host uses NATS:
  - `NewSink(*nats.Conn)`, named `nats`, publishes to **plain NATS subjects**. "Delivered" means the server has received the message. It does **not** mean any subscriber got it: with no subscriber present, the event counts as delivered and is lost. That is a property of plain NATS, and the sink states it rather than hiding it.
  - `NewJetStreamSink(jetstream.JetStream)`, named `jetstream`, publishes to **JetStream**. "Delivered" means a stream stored the message. Each publish carries the event identifier as the message ID, so the broker discards a redelivery inside its duplicate window. The sink never creates or reconfigures a stream; with no stream bound to the subject, the publish fails and is retried.
- **One message contract for both sinks.**
  - **Subject:** a configurable prefix followed by the event type, so the default is `hmntsk.events.task.completed`, and consumers filter on the server with wildcards.
  - **Headers:** the webhook sink's `Hmntsk-*` names, plus the attempt number and a schema version.
  - **Body:** the whole event as JSON.
- **Construction rejects wiring mistakes:** a missing connection, an empty name, a non-positive timeout, and a subject prefix NATS would refuse.
- **BREAKING (spec only):** the five retention requirements move, word for word, out of `bus-delivery` into a new `redis-delivery` capability. They describe Redis trim modes and consumer groups, which a NATS sink cannot meet. `bus-delivery` becomes the contract every bus sink shares. No code or behaviour changes.
- **Documentation:** a NATS section in `docs/delivery.md` states what each mode does and does not promise, and a drift test keeps it honest.
- **Workspace:** `delivery/nats` becomes the 15th module, in the workspace, the release order, the docs and CI.

## Capabilities

### New Capabilities

- `nats-delivery`: publication of task events to NATS, in plain-subject and JetStream modes. Covers subject and message layout, what delivered means in each mode, broker-side de-duplication, how failures are classified, and construction-time validation.
- `redis-delivery`: requirements specific to the Redis Stream sink. It starts with the retention requirements moved unchanged out of `bus-delivery`.

### Modified Capabilities

- `bus-delivery`: the five Redis-specific retention requirements are removed, having moved to `redis-delivery`. The remaining requirements are the contract shared by every bus sink.

## Impact

- **Code:** a new `delivery/nats` module depending on core and `github.com/nats-io/nats.go`, with `relaytest` and `testcontainers-go/modules/nats` for tests. No change to core, the relay, the outbox, or the webhook and Redis sinks.
- **Dependencies:** `github.com/nats-io/nats.go` v1.53.1 and `github.com/testcontainers/testcontainers-go/modules/nats` v0.44.0, matching the repo's testcontainers pin. Both enter only through the new module.
- **Workspace wiring:** `go.work`, Makefile `MODULES` and `RELEASE_ORDER`, `docs/releasing.md` (tag table and release-order block), README, the root `docs_test.go` module list, and the CI delivery matrix.
- **Operations:** a host choosing plain subjects accepts that an event with no subscriber is lost once the server has received it. A host choosing JetStream must create its stream, since the sink never does.
- **Release:** like the other satellites, `delivery/nats` imports `relay`, which no published tag contains yet. `make tidy` stays unusable until the core is tagged.
