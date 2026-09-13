package sqlstore_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/sqlkit"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
)

// executorOnly hides every method but the Executor contract, standing in for an
// executor that cannot run schema statements.
type executorOnly struct{ sqlkit.Executor }

func TestNew(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", "file::memory:")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	executor, err := stdsqlexec.New(db, sqlkit.SQLite)
	require.NoError(t, err)

	type testCase struct {
		name     string
		executor sqlkit.Executor
		opts     []sqlstore.Option
		assert   func(t *testing.T, store *sqlstore.Store, err error)
	}

	cases := []testCase{
		{
			name:     "an executor makes a store in the executor's dialect with unprefixed tables",
			executor: executor,
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				require.NoError(t, err)
				assert.Equal(t, sqlkit.SQLite, store.Dialect())
				assert.Equal(t, []string{sqlstore.NotificationsTable, sqlstore.WatermarksTable}, store.Tables())
			},
		},
		{
			name:     "a table prefix applies to every table",
			executor: executor,
			opts:     []sqlstore.Option{sqlstore.WithTablePrefix("app_")},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"app_" + sqlstore.NotificationsTable, "app_" + sqlstore.WatermarksTable}, store.Tables())
			},
		},
		{
			name:     "a nil executor is a configuration error",
			executor: nil,
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				require.ErrorIs(t, err, notify.ErrConfiguration)
				assert.Nil(t, store)
			},
		},
		{
			name:     "an executor that cannot run schema statements is a configuration error",
			executor: executorOnly{executor},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				require.ErrorIs(t, err, notify.ErrConfiguration)
				assert.Nil(t, store)
			},
		},
		{
			name:     "a prefix that is not a plain identifier is a configuration error",
			executor: executor,
			opts:     []sqlstore.Option{sqlstore.WithTablePrefix(`app"; DROP`)},
			assert: func(t *testing.T, store *sqlstore.Store, err error) {
				require.ErrorIs(t, err, notify.ErrConfiguration)
				assert.Nil(t, store)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store, err := sqlstore.New(tc.executor, tc.opts...)
			tc.assert(t, store, err)
		})
	}
}
