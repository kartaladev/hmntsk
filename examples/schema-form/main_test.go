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
== default: a form built from the served schemas
alice GET /v1/task-types/invoice.approve → 200
form title: Approve invoice
form fields: approved (boolean; required), note (string), reason (string: within-budget, over-budget, duplicate, other; required)
alice POST /v1/tasks/<approval>/claim → 200 RESERVED
alice POST /v1/tasks/<approval>/start → 200 IN_PROGRESS
alice POST /v1/tasks/<approval>/progress {"patch":[{"op":"add","path":"/approved","value":true}]} → 200 progress {"approved":true}
alice POST /v1/tasks/<approval>/complete {"output":{"approved":true}} → 400 validation_failed
alice POST /v1/tasks/<approval>/complete {"output":{"approved":true,"reason":"within-budget"}} → 200 COMPLETED

== override: the typed facade, schemas derived from Go types
registered invoice.approve.typed with schemas derived from Go types
derived input requires [amount invoiceId supplier], output requires [approved reason]
created from {InvoiceID:INV-43 Supplier:Acme Paper Amount:1299}
typed handler: INV-43 completed, approved=false reason=duplicate
completed with {Approved:false Reason:duplicate Note:}
read back typed: approved=false reason=duplicate, amount=1299
`, out.String())
}
