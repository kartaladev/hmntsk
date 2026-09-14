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
== default: the type's escalation policy
approval for INV-42 created, due 2026-09-15 09:00 UTC
carol may claim it: false
24h1m0s later
sweep: claimed 1, escalated 1, exempted 0
event: task.escalated
candidates now: groups [finance-approvers finance-managers], users []
carol may claim it: true

== override: a per-task policy, and a sweeper for approvals only
approval for INV-43 created with its own policy: widen to user carol
review for INV-43 created, due 2026-09-16 09:00 UTC
48h1m0s later
sweep: claimed 1, escalated 1, exempted 0
approval candidates now: groups [finance-approvers], users [carol]
review escalations: 0

== override: escalating directly, without the sweeper
approval for INV-44 created, due 2026-09-15 09:00 UTC, not yet overdue
an operator escalates it: task.escalated
candidates now: groups [finance-approvers finance-managers], users []

== override: a policy that exempts work already started
approval for INV-45 started by alice
24h1m0s later
sweep: claimed 1, escalated 0, exempted 1
escalations: 0, status: IN_PROGRESS

== override: a cap on how often one task escalates
approvals for INV-46 (the type's policy) and INV-47 (at most once) created
24h1m0s later
sweep: claimed 2, escalated 2, exempted 0
6m0s later, both leases have expired and both tasks are still overdue
sweep: claimed 2, escalated 1, exempted 1
escalations: INV-46 2, INV-47 1

== override: a policy that supersedes an overdue task
approval for INV-48 created with a policy that supersedes it
24h1m0s later
sweep: claimed 1, escalated 1, exempted 0
event: task.obsoleted
status now: OBSOLETE
`, out.String())
}
