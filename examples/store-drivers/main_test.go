package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/storetest"
)

// TestRun starts a real PostgreSQL and a real MySQL in containers, as every
// store module's tests do, and runs the scenario against both. It needs Docker.
func TestRun(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, run(t.Context(), &out, databases{
		Postgres: storetest.RunTestPostgres(t),
		MySQL:    storetest.RunTestMySQL(t),
	}))

	assert.Equal(t, `
== default: the same wiring on every driver and database
database/sql on PostgreSQL: schema verified; INV-42 READY, RESERVED, IN_PROGRESS, COMPLETED; tasks for INV-42: 1
pgx on PostgreSQL: schema verified; INV-42 READY, RESERVED, IN_PROGRESS, COMPLETED; tasks for INV-42: 1
database/sql on MySQL: schema verified; INV-42 READY, RESERVED, IN_PROGRESS, COMPLETED; tasks for INV-42: 1
GORM on PostgreSQL: schema verified; INV-42 READY, RESERVED, IN_PROGRESS, COMPLETED; tasks for INV-42: 1
GORM on MySQL: schema verified; INV-42 READY, RESERVED, IN_PROGRESS, COMPLETED; tasks for INV-42: 1
identical on every driver: true

== override: the host's own transaction, in each driver's transaction type
database/sql on PostgreSQL: committed INV-43 (invoice rows 1, tasks 1, dispatch waited for commit: true, then task.created); rolled back INV-44 (invoice rows 0, tasks 0)
pgx on PostgreSQL: committed INV-43 (invoice rows 1, tasks 1, dispatch waited for commit: true, then task.created); rolled back INV-44 (invoice rows 0, tasks 0)
database/sql on MySQL: committed INV-43 (invoice rows 1, tasks 1, dispatch waited for commit: true, then task.created); rolled back INV-44 (invoice rows 0, tasks 0)
GORM on PostgreSQL: committed INV-43 (invoice rows 1, tasks 1, dispatch waited for commit: true, then task.created); rolled back INV-44 (invoice rows 0, tasks 0)
GORM on MySQL: committed INV-43 (invoice rows 1, tasks 1, dispatch waited for commit: true, then task.created); rolled back INV-44 (invoice rows 0, tasks 0)
identical on every driver: true
`, out.String())
}
