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
== default: the same contract on gin and on fiber
gin   alice GET /v1/tasks?candidate=me → 200 INV-42
gin   alice GET /v1/notifications/count → 200 count=1
gin   bob GET /v1/tasks?candidate=alice → 403 forbidden
gin   alice POST /v1/tasks/<approval>/claim → 200 RESERVED
gin   dave GET /v1/tasks/<approval> → 403 forbidden
gin   carol GET /v1/tasks?group=finance-approvers → 403 forbidden
gin   alice GET /v1/nothing-here → 404 not_found
fiber alice GET /v1/tasks?candidate=me → 200 INV-42
fiber alice GET /v1/notifications/count → 200 count=1
fiber bob GET /v1/tasks?candidate=alice → 403 forbidden
fiber alice POST /v1/tasks/<approval>/claim → 200 RESERVED
fiber dave GET /v1/tasks/<approval> → 403 forbidden
fiber carol GET /v1/tasks?group=finance-approvers → 403 forbidden
fiber alice GET /v1/nothing-here → 404 not_found
identical on gin and fiber: true

== override: one query policy on the contract, served by both
gin   carol GET /v1/tasks?group=finance-approvers → 200 INV-42
gin   alice GET /v1/tasks?group=finance-approvers → 403 forbidden
fiber carol GET /v1/tasks?group=finance-approvers → 200 INV-42
fiber alice GET /v1/tasks?group=finance-approvers → 403 forbidden
identical on gin and fiber: true
`, out.String())
}
