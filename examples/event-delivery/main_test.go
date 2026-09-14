package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, run(t.Context(), &out))

	assert.Equal(t, `
== default: an in-process event handler
handler: task.created INV-42
handler: task.claimed INV-42
handler: task.started INV-42
handler: task.completed INV-42 {"approved":true,"reason":"within-budget"}

== override: the relay and a signed webhook, default destination policy
approval for INV-43 created with a callback to the receiver on 127.0.0.1
relay pass: claimed 1, delivered 0, retried 0, dead-lettered 1 (webhook: permanent 1)
the receiver got nothing; the recorded error names loopback: true

== override: the same webhook, allowed to reach loopback
approval for INV-44 created, claimed, started and completed
relay pass: claimed 4, delivered 4, retried 0, dead-lettered 0 (webhook: delivered 4)
receiver verified: task.created, task.claimed, task.started, task.completed
receiver saw correlation INV-44 and reference parameters {"invoice":"INV-44"}
a tampered body fails verification: true

== override: a receiver that is down, retried with backoff
approval for INV-45 created; its receiver answers 503, then 204
relay pass: claimed 1, delivered 0, retried 1, dead-lettered 0 (webhook: retryable 1)
outbox: attempts 1, next attempt in 1m0s, last error names 503: true
relay pass: claimed 0, delivered 0, retried 0, dead-lettered 0 (webhook: nothing offered)
1m0s later
relay pass: claimed 1, delivered 1, retried 0, dead-lettered 0 (webhook: delivered 1)
the receiver answered INV-45 with: 503, 204
approvals for INV-46 (receiver answers 429) and INV-47 (receiver answers 410) created
relay pass: claimed 2, delivered 0, retried 1, dead-lettered 1 (webhook: retryable 1, permanent 1)

== override: two sinks, each accepting on its own
approval for INV-48 created; the ledger sink fails its first attempt
relay pass: claimed 1, delivered 0, retried 1, dead-lettered 0 (ledger: retryable 1; webhook: delivered 1)
accepted so far: webhook
the error handler saw the ledger's failure: true
1m0s later
relay pass: claimed 1, delivered 1, retried 0, dead-lettered 0 (ledger: delivered 1; webhook: skipped 1)
the webhook receiver got INV-48 once: true

== override: what an event says about its audience
approval for INV-49 created, then claimed and released by alice
relay pass: claimed 3, delivered 3, retried 0, dead-lettered 0 (webhook: delivered 3)
task.created: created by billing-service, candidates [finance-approvers]
task.claimed: created by billing-service, assignee alice, candidates [finance-approvers]
task.released: created by billing-service, candidates [finance-approvers], previous assignee alice
`, out.String())
}
