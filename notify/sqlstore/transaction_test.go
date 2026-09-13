package sqlstore_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/sqlstore/internal/harness"
	"github.com/kartaladev/hmntsk/sqlkit"
	"github.com/kartaladev/hmntsk/sqlkit/sqlkittest"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
)

// TestNotificationWritesDoNotJoinTheCallersTransaction pins the stated limit
// that notification writes run in transactions of their own.
//
// It runs on PostgreSQL rather than SQLite: SQLite has one writer, so a caller
// holding a write transaction would block the store's own transaction until the
// busy timeout, which says nothing about joining.
func TestNotificationWritesDoNotJoinTheCallersTransaction(t *testing.T) {
	t.Parallel()

	db := openSQL(t, "postgres", sqlkittest.RunTestPostgres(t))
	executor := stdsqlExecutor(t, db, sqlkit.PostgreSQL)
	store := harness.NewStore(t, executor)

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	callerCtx := stdsqlexec.ContextWithTx(t.Context(), tx)
	require.True(t, executor.InTransaction(callerCtx), "the caller's transaction is on the context the store is given")

	created := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)

	_, err = store.Insert(callerCtx, "task-1", []notify.Insertion{{Notification: notify.Notification{
		ID: "n-1", Recipient: "alice", SourceID: "event-1", Subject: "task-1", Kind: "offer",
		State: notify.StateActive, CreatedAt: created,
	}}})
	require.NoError(t, err)

	require.NoError(t, tx.Rollback(), "the caller rolls its own transaction back")

	got, err := store.Get(t.Context(), "alice", "n-1")
	require.NoError(t, err, "the notification survives the caller's rollback")
	assert.Equal(t, notify.StateActive, got.State)
}
