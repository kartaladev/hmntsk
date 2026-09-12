package storetest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// runRollbackCases covers what must not survive a scope that failed.
func runRollbackCases(t *testing.T, factory Factory) {
	t.Helper()

	type testCase struct {
		name string
		// act runs the failing scope. It returns the error Do produced, or
		// recovers a panic and reports it.
		act    func(t *testing.T, h Harness, id hmntsk.TaskID) (panicked any, err error)
		assert func(t *testing.T, panicked any, err error)
	}

	cases := []testCase{
		{
			name: "an error rolls the scope back",
			act: func(t *testing.T, h Harness, id hmntsk.TaskID) (any, error) {
				return nil, h.Store.Do(t.Context(), func(ctx context.Context) error {
					if err := h.Store.Create(ctx, NewTask(id)); err != nil {
						return err
					}

					return errCaseFailure
				})
			},
			assert: func(t *testing.T, panicked any, err error) {
				require.ErrorIs(t, err, errCaseFailure)
				assert.Nil(t, panicked)
			},
		},
		{
			name: "a panic rolls the scope back and is re-raised",
			act: func(t *testing.T, h Harness, id hmntsk.TaskID) (panicked any, err error) {
				defer func() { panicked = recover() }()

				err = h.Store.Do(t.Context(), func(ctx context.Context) error {
					if createErr := h.Store.Create(ctx, NewTask(id)); createErr != nil {
						return createErr
					}

					panic("storetest: injected panic")
				})

				return panicked, err
			},
			assert: func(t *testing.T, panicked any, _ error) {
				assert.Equal(t, "storetest: injected panic", panicked,
					"a panic must reach the caller unchanged, not become an error")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := factory(t)

			var seq ids

			id := seq.next()

			before, err := h.Events(t.Context())
			require.NoError(t, err)

			panicked, actErr := tc.act(t, h, id)
			tc.assert(t, panicked, actErr)

			_, getErr := h.Store.Get(t.Context(), id)
			assert.ErrorIs(t, getErr, hmntsk.ErrNotFound, "no partial change may be durable")

			records, err := h.Store.History(t.Context(), id)
			require.NoError(t, err)
			assert.Empty(t, records, "a rolled-back scope leaves no history")

			after, err := h.Events(t.Context())
			require.NoError(t, err)
			assert.Len(t, after, len(before), "a rolled-back scope leaves no events")
		})
	}

	t.Run("events written in a failed scope are not durable", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := NewTask(seq.next())

		before, err := h.Events(t.Context())
		require.NoError(t, err)

		err = h.Store.Do(t.Context(), func(ctx context.Context) error {
			if createErr := h.Store.Create(ctx, task); createErr != nil {
				return createErr
			}

			if appendErr := h.Store.Append(ctx, []hmntsk.Event{
				NewEvent(task, hmntsk.EventTypeCreated, "event-rolled-back"),
			}); appendErr != nil {
				return appendErr
			}

			if historyErr := h.Store.AppendHistory(ctx,
				NewRecord(task, hmntsk.OpCreate, hmntsk.StatusCreated, hmntsk.StatusReady),
			); historyErr != nil {
				return historyErr
			}

			return errCaseFailure
		})
		require.ErrorIs(t, err, errCaseFailure)

		after, err := h.Events(t.Context())
		require.NoError(t, err)
		assert.Len(t, after, len(before),
			"nothing a consumer could act on may survive a rollback")
	})

	t.Run("a rolled-back update leaves the stored task byte for byte unchanged", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := Seed(t, h, NewTask(seq.next()))

		err := h.Store.Do(t.Context(), func(ctx context.Context) error {
			claimed := task.Clone()
			claimed.Version = task.Version + 1
			claimed.Status = hmntsk.StatusReserved
			claimed.Assignee = Assignee

			if updateErr := h.Store.Update(ctx, claimed, task.Version); updateErr != nil {
				return updateErr
			}

			return errCaseFailure
		})
		require.ErrorIs(t, err, errCaseFailure)

		stored, err := h.Store.Get(t.Context(), task.ID)
		require.NoError(t, err)
		assert.Equal(t, hmntsk.StatusReady, stored.Status,
			"a rolled-back claim must not appear to have happened")
		assert.Empty(t, stored.Assignee)
		assert.Equal(t, task.Version, stored.Version)
	})
}
