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
== default: publish once per source, then read
published: created 2, duplicates 0
published the same sources again: created 0, duplicates 2
alice: comment ACTIVE "Bob commented on INV-42"
alice active: 1
alice marked read: 1
bob marked all read: 1
alice active: 0, bob active: 0

== override: a coalescing draft
reminder for alice: created 1, coalesced 0
another reminder for alice, coalescing: created 0, coalesced 1

== override: closing a subject with a successor, and its watermark
closed comments on invoice/INV-42 at version 2, sparing bob: closed 1 [alice], successors 1
alice: decision ACTIVE "INV-42 was approved", reminder ACTIVE "INV-42 is due tomorrow", comment CLOSED approved
bob: comment READ "Alice commented on INV-42"
a late comment at version 1 for alice: created 0, suppressed 1
a comment at version 3 for alice: created 1, suppressed 0
`, out.String())
}
