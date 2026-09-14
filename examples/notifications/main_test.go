package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestRun(t *testing.T) {
	// Not parallel: goleak checks that the hub, the streams and the servers are
	// all gone when run returns, which other tests' goroutines would muddle.
	defer goleak.VerifyNone(t)

	var out bytes.Buffer

	require.NoError(t, run(t.Context(), &out))

	assert.Equal(t, `
== default: task events become notifications
bob's stream: connected
approval for INV-42 created; relay pass: delivered 1
bob's stream: unread-changed (created)
alice GET /v1/notifications → 200 offer ACTIVE "Task available: invoice.approve"
alice's links: context /invoices/INV-42/approve?task=<approval-42>, task /v1/tasks/<approval-42>
alice GET /v1/notifications/count → 200 count=1
bob claimed it; relay pass: delivered 1
alice GET /v1/notifications → 200 taken ACTIVE "Task taken by bob: invoice.approve", offer CLOSED taken
bob GET /v1/notifications/count → 200 count=0
alice POST /v1/notifications/<taken-42>/read → 200
alice GET /v1/notifications/count → 200 count=0

== override: SQLite, the host's links and titles, a supervisor's stream, retention and email
notification schema, email table included, applied and verified on SQLite
approval for INV-43 created; relay pass: delivered 1
alice GET /v1/notifications → 200 offer ACTIVE "Approve INV-43 from Acme Paper (1299)"
alice's links: context /invoices/INV-43/approve?task=<approval-43>, task /app/tasks/<approval-43>
carol GET /v1/notifications/stream?recipient=bob → 403 under the default policy
carol GET /v1/notifications/stream?recipient=bob → 200 under a supervisor policy
6m0s later, email pass: messages 2, notifications sent 2
mail to alice@example.test: "1 invoice task waiting" Approve INV-43 from Acme Paper (1299)
mail to bob@example.test: "1 invoice task waiting" Approve INV-43 from Acme Paper (1299)
bob read his offer; approval for INV-44 created; relay pass: delivered 1
prune, RetainActive, at most 1 per recipient: inactive deleted 1, active evicted 0
alice GET /v1/notifications/count → 200 count=2

== override: releasing and delegating, under the default rules
approval for INV-50: alice claimed it, then released it
bob: offer ACTIVE "Task available: invoice.approve", taken CLOSED released, offer CLOSED taken
bob claimed it and delegated it to alice; alice delegated it back to bob
alice: assigned CLOSED reassigned, offer CLOSED taken
bob: assigned ACTIVE "Task assigned to you: invoice.approve", offer CLOSED taken, taken CLOSED released, offer CLOSED taken

== override: a widening escalation offers the task to the newly eligible only
approval for INV-51 created; alice and bob were offered it
24h1m0s later, the sweeper widened it to finance-managers
open offers: alice 1, bob 1, carol 1

== override: host rules, and which statuses close notifications
approval for INV-52 completed by alice
billing-service: done ACTIVE "INV-52 is done"
approval for INV-53 started by alice, then failed
bob: taken ACTIVE "Task taken by alice: invoice.approve"

== override: email for offers only, and a recipient with no address
6m0s later, email pass: messages 1, sent 1, skipped for no address 1, skipped by filter 1
mail to alice@example.test: "1 invoice task waiting" Task available: invoice.approve

== override: retention by age, and the default strategy under a count bound
alice read her INV-56 offer; INV-57 was offered too
91 days later, prune with the defaults: inactive deleted for age 1, active evicted 0
prune, at most 1 per recipient, default strategy: inactive deleted 0, active evicted 1 [bob]
`, out.String())
}
