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

	assert.Equal(t, `SQLite schema applied and verified

== default: the engine commits its own transaction
invoice INV-41 saved by the host
event: task.created for invoice INV-41
review task created: READY

== override: the host's transaction, committed
invoice INV-42 and its review task written in one transaction
event dispatch waits for the host's commit: true
committed
event: task.created for invoice INV-42
invoice INV-42 exists: true, tasks for it: 1

== override: the host's transaction, rolled back
invoice INV-43 and its review task written in one transaction
rolled back
invoice INV-43 exists: false, tasks for it: 0
`, out.String())
}
