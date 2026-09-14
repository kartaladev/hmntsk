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

	assert.Equal(t, `seeded approvals INV-1 to INV-5 for finance-approvers; alice claimed INV-4; 1h30m0s later

== default: orderings over the Go API
created:             INV-1 INV-2 INV-3 INV-4 INV-5
priority:            INV-3 INV-1 INV-2 INV-5 INV-4
due:                 INV-5 INV-4 INV-2 INV-3 INV-1
urgency:             INV-3 INV-2 INV-1 INV-5 INV-4
priority descending: INV-4 INV-5 INV-2 INV-1 INV-3
urgency, 2 per page: [INV-3 INV-2] [INV-1 INV-5] [INV-4]
an urgency cursor continued under priority: refused (400)
alice's buckets: available=4 mine=1 overdue=1

== default: self-only queries over HTTP
alice GET /v1/tasks?candidate=me&status=READY&orderBy=urgency → 200 INV-3 INV-2 INV-1 INV-5
alice GET /v1/tasks/count?assignee=me → 200 count=1
alice GET /v1/tasks?candidate=bob → 403
carol GET /v1/tasks?group=finance-approvers → 403
alice GET /v1/tasks?candidate=me&order=desc → 400

== override: a supervisor may read the team queue
carol GET /v1/tasks?group=finance-approvers&status=READY&orderBy=urgency → 200 INV-3 INV-2 INV-1 INV-5
carol GET /v1/tasks/count?group=finance-approvers → 200 count=5
alice GET /v1/tasks?group=finance-approvers → 403
alice GET /v1/tasks?candidate=me&status=READY → 200 INV-1 INV-2 INV-3 INV-5
`, out.String())
}
