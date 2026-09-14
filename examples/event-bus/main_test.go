package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmntsknats "github.com/kartaladev/hmntsk/delivery/nats"
	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
)

// TestRun runs the scenario against real brokers: a Redis and a NATS server
// with JetStream, each started in a container by the owning module's helper.
func TestRun(t *testing.T) {
	t.Parallel()

	b := brokers{
		// The scenario trims exactly, so a default Redis prints the same lengths
		// as any other.
		redis: hmntskredis.RunTestRedis(t),
		nats:  hmntsknats.RunTestNATS(t),
	}

	var out bytes.Buffer

	require.NoError(t, run(t.Context(), &out, b))

	assert.Equal(t, `
== default: a Redis stream, with no bound
approval for INV-42 created, claimed, started and completed
relay pass: claimed 4, delivered 4, retried 0 (redis: delivered 4)
stream hmntsk.events holds 4 entries:
  task.created INV-42 schema hmntsk.event.v1
  task.claimed INV-42 schema hmntsk.event.v1
  task.started INV-42 schema hmntsk.event.v1
  task.completed INV-42 schema hmntsk.event.v1

== default: NATS subjects, where delivered means the server received it
approval for INV-43 created, claimed, started and completed
relay pass: claimed 4, delivered 4, retried 0 (nats: delivered 4)
a subscriber on hmntsk.events.> received:
  hmntsk.events.task.created (event type task.created, invoice INV-43, schema hmntsk.nats.event.v1)
  hmntsk.events.task.claimed (event type task.claimed, invoice INV-43, schema hmntsk.nats.event.v1)
  hmntsk.events.task.started (event type task.started, invoice INV-43, schema hmntsk.nats.event.v1)
  hmntsk.events.task.completed (event type task.completed, invoice INV-43, schema hmntsk.nats.event.v1)
approval for INV-44 created while nobody subscribed
relay pass: claimed 1, delivered 1, retried 0 (nats: delivered 1)
a subscriber that arrives afterwards receives: nothing

== default: JetStream, where delivered means a stream stored it
approval for INV-45 created, claimed, started and completed; no stream captures hmntsk.events.> yet
relay pass: claimed 4, delivered 0, retried 4 (jetstream: retryable 4)
the host creates stream HMNTSK_EVENTS for hmntsk.events.>, discarding duplicates for 2m0s
1m0s later
relay pass: claimed 4, delivered 4, retried 0 (jetstream: delivered 4)
stream HMNTSK_EVENTS stores 4 messages
approval for INV-46 created, claimed, started and completed, published by two JetStream sinks
relay pass: claimed 4, delivered 4, retried 0 (jetstream: delivered 4; jetstream-copy: delivered 4)
stream HMNTSK_EVENTS stores 8 messages: the second sink's copies were discarded as duplicates

== override: bounded Redis streams, subject prefixes, an expected stream, and four sinks in one relay
approval for INV-47 created, claimed, started and completed
relay pass: claimed 4, delivered 0, retried 4 (jetstream: retryable 4; nats: delivered 4; redis: delivered 4; redis-audited: delivered 4)
redis stream acme.invoice-events, at most about 2 entries: holds 2
redis stream acme.audited-events, at most about 2 entries, trimming only what groups acknowledged: holds 4
a subscriber on acme.live.task.completed received: acme.live.task.completed (invoice INV-47)
the JetStream sink expected stream ACME_ELSEWHERE, but ACME_INVOICES captures acme.invoices.>, so it was refused
the host corrects the expected stream to ACME_INVOICES; 1m0s later
relay pass: claimed 4, delivered 4, retried 0 (jetstream: delivered 4; nats: skipped 4; redis: skipped 4; redis-audited: skipped 4)
stream ACME_INVOICES stores 4 messages
`, out.String())
}
