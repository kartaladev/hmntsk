package tasknotify

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
)

// TestNewDefaults pins the defaults a projector built with no options applies.
// They are not exported as getters, so they are read from inside the package.
func TestNewDefaults(t *testing.T) {
	t.Parallel()

	engine, err := hmntsk.New(memstore.New())
	require.NoError(t, err)

	notifier, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	projector, err := New(engine, notifier)
	require.NoError(t, err)

	assert.Equal(t, DefaultSinkName, projector.name)
	assert.Equal(t, DefaultPublishBatch, projector.batch)
	assert.Equal(t, 500, projector.batch)
	assert.Equal(t, DefaultTaskLinkTemplate, projector.taskLink)
	assert.Equal(t, "/v1/tasks/{task.id}", projector.taskLink)
	assert.ElementsMatch(t, []hmntsk.Status{
		hmntsk.StatusCompleted, hmntsk.StatusFailed, hmntsk.StatusError, hmntsk.StatusExited, hmntsk.StatusObsolete,
	}, slices.Collect(maps.Keys(projector.closing)), "every final status closes a task's notifications by default")
}
