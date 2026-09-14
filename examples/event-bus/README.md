# event-bus

Task events published to message brokers for internal consumers: a Redis
stream, plain NATS subjects and JetStream. The relay drives every sink, so an
event stays in the engine's outbox until each broker has taken it.

## What it shows

- **Redis stream, default** (`delivery/redis`): every event is appended to
  `hmntsk.events` and nothing is ever trimmed. The entries are read back with
  `XRANGE`: event type, invoice and the `hmntsk.event.v1` schema.
- **NATS subjects, default** (`delivery/nats` `NewSink`):
  - every event goes to `hmntsk.events.<event type>`, with routing headers such
    as `Hmntsk-Event-Type`;
  - "delivered" means the server received the message, not that anyone read it;
  - an event published while nobody subscribes is delivered and lost.
- **JetStream, default** (`NewJetStreamSink`):
  - "delivered" means a stream stored the message;
  - the sink never creates streams, so until the host creates one every attempt
    is retried;
  - each message carries the event ID as its `Nats-Msg-Id`, so a second copy
    inside the stream's duplicate window is discarded.
- **Overrides, all in one relay:**
  - `WithStream` and `WithMaxLen` bound a Redis stream; approximate trimming
    keeps at least the bound;
  - `WithTrimMode(TrimAcked)` keeps whatever a consumer group has not
    acknowledged;
  - `WithSubjectPrefix` sets the NATS prefix, and a subscriber filters on the
    subject;
  - `WithExpectStream` makes the JetStream sink refuse to publish when the
    stream capturing the subject is not the one named, until the host corrects
    it;
  - several sinks accept independently: a retry skips the sinks that already
    took an event.

## Context

These examples share a made-up invoice-approval business.

- **Tasks:** each section takes one `invoice.approve` task through create,
  claim, start and complete, which records four events.
- **People:** alice and bob are finance approvers, carol is a finance manager,
  dave is an auditor.
- **Engine:** in memory, with a fixed clock. The relay retries after exactly one
  minute with jitter off, so the output is the same on every run.

## What it leaves out

- **Webhooks:** signed delivery to a callback address is in `event-delivery`.
- **Consuming:** consumer groups, acknowledgements and de-duplicating on
  `Hmntsk-Event-Id` belong to your consumers. hmntsk only produces.
- **Broker operations:** replicas, persistence and authentication.

## Services needed

A Redis 8.2+ server and a NATS server with JetStream enabled:

```sh
docker run --rm -p 6379:6379 redis:8.2.9-alpine
docker run --rm -p 4222:4222 nats:2.12.7-alpine -js
```

**Warning: use scratch servers.** On every run the scenario resets what it
owns:
- it deletes the Redis keys `hmntsk.events`, `acme.invoice-events` and
  `acme.audited-events`;
- it deletes the JetStream streams `HMNTSK_EVENTS` and `ACME_INVOICES`.

**Trimming is approximate.** Redis removes only whole stream nodes, which hold
`stream-node-max-entries` entries each (100 by default). Against a Redis with
the default setting, the bounded streams keep all the handful of events this
scenario publishes, so the program prints more entries than the bound. The test
starts its Redis with `stream-node-max-entries` set to 1, which is what makes
the trimming in its expected output exact. The scenario never changes that
setting on your server.

## Run it

From the `examples/` directory:

```sh
HMNTSK_REDIS_ADDR=127.0.0.1:6379 HMNTSK_NATS_URL=nats://127.0.0.1:4222 go run ./event-bus
```

Without either setting, the program stops and prints the `docker run` command
to start that server.

The test starts both servers in containers itself, so it needs Docker but not
the commands above:

```sh
go test ./event-bus
```

## Read next

- [Delivering events](../../docs/delivery.md), in particular
  [Bounding the Redis stream](../../docs/delivery.md#bounding-the-redis-stream)
  and [Publishing to NATS](../../docs/delivery.md#publishing-to-nats)
- The package docs: `go doc github.com/kartaladev/hmntsk/delivery/redis` and
  `go doc github.com/kartaladev/hmntsk/delivery/nats`
