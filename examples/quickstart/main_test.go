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

	assert.Equal(t, `registered: invoice.review, invoice.approve
created invoice.approve for invoice INV-42: READY
candidates: alice, bob
alice claimed it: RESERVED
alice started it: IN_PROGRESS
alice completed it: COMPLETED
output: {"approved":true,"reason":"within-budget"}
`, out.String())
}
