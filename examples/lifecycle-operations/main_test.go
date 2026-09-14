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
== default: every lifecycle operation, on a task the type offers to a group
approval for INV-1 created: READY, offered to finance-approvers
alice claims it: RESERVED, held by alice
bob releases it: refused, not authorised
alice releases it: READY
alice claims it again: RESERVED, held by alice
alice delegates it to carol: refused, not authorised
alice delegates it to bob: RESERVED, held by bob
bob completes it before starting: refused, illegal transition from RESERVED
bob suspends it: SUSPENDED, will resume to RESERVED
alice claims it while suspended: refused, illegal transition from SUSPENDED
bob resumes it: RESERVED, held by bob
bob saves progress with a stale version: refused, conflict (expected version 6, current version 7)
bob saves progress: IN_PROGRESS, the first save started it, events 0
bob fails it: FAILED, reason "supplier unreachable"
approval for INV-2 cancelled: EXITED, reason "duplicate invoice"
cancelling it again: refused, illegal transition from EXITED

== override: a candidate pool chosen for each task
pool of finance-managers, carol only: RESERVED for carol at creation
pool of a group with no members: ERROR, reason "no actor is eligible for this task"
pool of finance-approvers excluding bob: RESERVED for alice at creation
bob may claim it: false
`, out.String())
}
