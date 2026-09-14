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
== default: the type's hmntsk.route, expanded for each task
template: /invoices/{correlation.ownerRef}/{correlation.activityKey}?task={task.id}
form key: invoice-approve-form
INV-42: /invoices/INV-42/approve?task=<approval-42>
INV 7/B, raw: /invoices/INV 7/B/approve?task=<approval-7b>
INV 7/B, escaped by the host: /invoices/INV%207%2FB/approve?task=<approval-7b>
unknown placeholders are kept: /{tenant}/invoices/INV-42

== override: host metadata keys and a host link resolver
acme.icon: stamp
INV-42 while READY: /invoices/INV-42/approve?task=<approval-42>
INV-42 once COMPLETED: /acme/invoices/INV-42
`, out.String())
}
