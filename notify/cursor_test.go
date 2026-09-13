package notify_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

func TestCursor(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 14, 8, 30, 0, 123456000, time.UTC)
	position := notify.CursorPosition{CreatedAt: at, ID: "0199-id"}

	base := notify.ListQuery{
		Recipient: "alice",
		States:    []notify.State{notify.StateActive, notify.StateRead},
		Kinds:     []string{"offer", "taken"},
		Subject:   "task-1",
	}

	type testCase struct {
		name   string
		decode func(cursor string) notify.ListQuery
		assert func(t *testing.T, got notify.CursorPosition, ok bool, err error)
	}

	cases := []testCase{
		{
			name: "a cursor round-trips under the query that produced it",
			decode: func(cursor string) notify.ListQuery {
				q := base
				q.Cursor = cursor

				return q
			},
			assert: func(t *testing.T, got notify.CursorPosition, ok bool, err error) {
				require.NoError(t, err)
				assert.True(t, ok)
				assert.True(t, at.Equal(got.CreatedAt), "created at %s, got %s", at, got.CreatedAt)
				assert.Equal(t, "0199-id", got.ID)
			},
		},
		{
			name: "filters listed in another order are the same query",
			decode: func(cursor string) notify.ListQuery {
				q := base
				q.States = []notify.State{notify.StateRead, notify.StateActive}
				q.Kinds = []string{"taken", "offer"}
				q.Cursor = cursor

				return q
			},
			assert: func(t *testing.T, got notify.CursorPosition, ok bool, err error) {
				require.NoError(t, err)
				assert.True(t, ok)
				assert.Equal(t, "0199-id", got.ID)
			},
		},
		{
			name: "no cursor means the first page",
			decode: func(string) notify.ListQuery {
				return base
			},
			assert: func(t *testing.T, _ notify.CursorPosition, ok bool, err error) {
				require.NoError(t, err)
				assert.False(t, ok)
			},
		},
		{
			name: "a cursor under another recipient is refused",
			decode: func(cursor string) notify.ListQuery {
				q := base
				q.Recipient = "bob"
				q.Cursor = cursor

				return q
			},
			assert: func(t *testing.T, _ notify.CursorPosition, _ bool, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name: "a cursor under other states is refused",
			decode: func(cursor string) notify.ListQuery {
				q := base
				q.States = []notify.State{notify.StateActive}
				q.Cursor = cursor

				return q
			},
			assert: func(t *testing.T, _ notify.CursorPosition, _ bool, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name: "a cursor under another subject is refused",
			decode: func(cursor string) notify.ListQuery {
				q := base
				q.Subject = "task-2"
				q.Cursor = cursor

				return q
			},
			assert: func(t *testing.T, _ notify.CursorPosition, _ bool, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name: "a cursor that is not one of ours is refused",
			decode: func(string) notify.ListQuery {
				q := base
				q.Cursor = "not-a-cursor"

				return q
			},
			assert: func(t *testing.T, _ notify.CursorPosition, _ bool, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cursor := notify.EncodeCursor(base, position)
			got, ok, err := notify.DecodeCursor(tc.decode(cursor))
			tc.assert(t, got, ok, err)
		})
	}
}

func TestListQueryValidate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		query  notify.ListQuery
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name:  "a recipient alone is a valid query",
			query: notify.ListQuery{Recipient: "alice"},
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name:  "a recipient is required",
			query: notify.ListQuery{},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name:  "the page size cap is allowed",
			query: notify.ListQuery{Recipient: "alice", Limit: notify.MaxListLimit},
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name:  "a page size above the cap is refused",
			query: notify.ListQuery{Recipient: "alice", Limit: notify.MaxListLimit + 1},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name:  "a negative page size is refused",
			query: notify.ListQuery{Recipient: "alice", Limit: -1},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name:  "an unknown state is refused",
			query: notify.ListQuery{Recipient: "alice", States: []notify.State{"UNREAD"}},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
		{
			name:  "a malformed cursor is refused",
			query: notify.ListQuery{Recipient: "alice", Cursor: "%%%"},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, notify.ErrValidation)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.query.Validate())
		})
	}
}

func TestListQueryEffectiveLimit(t *testing.T) {
	t.Parallel()

	assert.Equal(t, notify.DefaultListLimit, notify.ListQuery{}.EffectiveLimit())
	assert.Equal(t, 7, notify.ListQuery{Limit: 7}.EffectiveLimit())
}
