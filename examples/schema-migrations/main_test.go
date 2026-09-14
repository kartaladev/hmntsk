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
== default: the published schema, applied by the host's migration step
tables: tasks, task_candidates, task_history, task_outbox, task_types
applied the published statements; schema verified

== default: nested scopes join one transaction
inside the scope, the engine sees a transaction: true
two creates in one scope committed together: tasks for INV-51: 2
a failure inside the scope rolled back everything: tasks for INV-52: 0

== override: a table prefix, for a database that already uses those names
tables: acme_tasks, acme_task_candidates, acme_task_history, acme_task_outbox, acme_task_types
applied and verified; a task created on the prefixed tables: tasks for INV-53: 1

== override: verification reports every discrepancy
dropped index acme_tasks_urgency_idx
schema mismatch: true; issues: 1, on table acme_tasks
`, out.String())
}
