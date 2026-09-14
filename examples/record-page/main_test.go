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

	assert.Equal(t, `seeded INV-42 (review completed by bob, approval waiting) and INV-43 (approval waiting)

== default: every task for one invoice, over the Go API
INV-42: invoice.review COMPLETED bob, invoice.approve READY
INV-43: invoice.approve READY

== default: participants-only reads over HTTP
alice GET /v1/tasks?ownerType=invoice&ownerRef=INV-42 → 403
alice GET /v1/tasks?candidate=me&status=READY&ownerType=invoice&ownerRef=INV-42 → 200 invoice.approve READY
alice GET /v1/tasks/<approval-42> → 200 invoice.approve READY
billing-service GET /v1/tasks/<approval-42> → 200 invoice.approve READY
dave GET /v1/tasks/<approval-42> → 403
dave GET /v1/tasks/<approval-42>/history → 403
nobody GET /v1/tasks/<approval-42> → 403
alice GET /v1/tasks/no-such-task → 404

== override: auditors may read any task
dave GET /v1/tasks/<approval-42> → 200 invoice.approve READY
dave GET /v1/tasks/<approval-42>/history → 200
carol GET /v1/tasks/<approval-42> → 403
nobody GET /v1/tasks/<approval-42> → 403
`, out.String())
}
