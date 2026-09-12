package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// errCaseFailure is the failure the rollback cases inject.
var errCaseFailure = errors.New("storetest: injected failure")

// runTransactionCases covers who begins, who commits, and what nesting means.
func runTransactionCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("engine-led scope commits before returning", func(t *testing.T) {
		h := factory(t)

		var seq ids

		id := seq.next()

		require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
			return h.Store.Create(ctx, NewTask(id))
		}))

		stored, err := h.Store.Get(t.Context(), id)
		require.NoError(t, err, "a scope the engine began must be durable when Do returns")
		assert.Equal(t, id, stored.ID)
	})

	t.Run("a host-led scope is not committed by the engine", func(t *testing.T) {
		h := factory(t)

		var seq ids

		id := seq.next()

		scoped, done := h.HostTx(t.Context())

		require.NoError(t, h.Store.Do(scoped, func(ctx context.Context) error {
			assert.True(t, h.Store.InTransaction(ctx),
				"the engine must recognise that it joined rather than began")

			return h.Store.Create(ctx, NewTask(id))
		}))

		_, err := h.Store.Get(t.Context(), id)
		require.ErrorIs(t, err, hmntsk.ErrNotFound,
			"the engine must not have committed a transaction it did not begin")

		require.NoError(t, done(true))

		stored, err := h.Store.Get(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, id, stored.ID)
	})

	t.Run("a host-led scope the host discards leaves nothing", func(t *testing.T) {
		h := factory(t)

		var seq ids

		id := seq.next()

		scoped, done := h.HostTx(t.Context())

		require.NoError(t, h.Store.Do(scoped, func(ctx context.Context) error {
			return h.Store.Create(ctx, NewTask(id))
		}))

		require.NoError(t, done(false))

		_, err := h.Store.Get(t.Context(), id)
		assert.ErrorIs(t, err, hmntsk.ErrNotFound)
	})

	t.Run("a nested scope joins rather than nests", func(t *testing.T) {
		h := factory(t)

		var seq ids

		outer := seq.next()
		inner := seq.next()

		require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
			if err := h.Store.Create(ctx, NewTask(outer)); err != nil {
				return err
			}

			return h.Store.Do(ctx, func(ctx context.Context) error {
				// The inner scope must see the outer scope's uncommitted write,
				// which it can only do if there is one transaction and not two.
				if _, err := h.Store.Get(ctx, outer); err != nil {
					return err
				}

				return h.Store.Create(ctx, NewTask(inner))
			})
		}))

		for _, id := range []hmntsk.TaskID{outer, inner} {
			_, err := h.Store.Get(t.Context(), id)
			assert.NoErrorf(t, err, "both writes must commit together, %s did not", id)
		}
	})

	t.Run("an inner failure aborts the whole scope", func(t *testing.T) {
		h := factory(t)

		var seq ids

		outer := seq.next()
		inner := seq.next()

		err := h.Store.Do(t.Context(), func(ctx context.Context) error {
			if err := h.Store.Create(ctx, NewTask(outer)); err != nil {
				return err
			}

			return h.Store.Do(ctx, func(ctx context.Context) error {
				if err := h.Store.Create(ctx, NewTask(inner)); err != nil {
					return err
				}

				return errCaseFailure
			})
		})
		require.ErrorIs(t, err, errCaseFailure)

		for _, id := range []hmntsk.TaskID{outer, inner} {
			_, getErr := h.Store.Get(t.Context(), id)
			assert.ErrorIsf(t, getErr, hmntsk.ErrNotFound,
				"a savepoint would have let %s survive its sibling's failure", id)
		}
	})

	t.Run("a nested scope takes no savepoint", func(t *testing.T) {
		h := factory(t)

		var seq ids

		outer := seq.next()
		inner := seq.next()

		err := h.Store.Do(t.Context(), func(ctx context.Context) error {
			if err := h.Store.Create(ctx, NewTask(outer)); err != nil {
				return err
			}

			innerErr := h.Store.Do(ctx, func(ctx context.Context) error {
				if err := h.Store.Create(ctx, NewTask(inner)); err != nil {
					return err
				}

				return errCaseFailure
			})
			require.ErrorIs(t, innerErr, errCaseFailure)

			// This is the whole difference between joining and nesting. A
			// savepoint would have been released by the inner scope's failure,
			// taking its write with it and leaving this one alive; joining
			// leaves both writes exactly where they were, in one transaction
			// that is still entirely undecided.
			_, getErr := h.Store.Get(ctx, inner)
			assert.NoError(t, getErr,
				"a failed inner scope must not have discarded its own write behind a savepoint")

			return innerErr
		})
		require.ErrorIs(t, err, errCaseFailure)

		for _, id := range []hmntsk.TaskID{outer, inner} {
			_, getErr := h.Store.Get(t.Context(), id)
			assert.ErrorIsf(t, getErr, hmntsk.ErrNotFound, "%s must not be durable", id)
		}
	})

	t.Run("the repository and the transactor share one connection", func(t *testing.T) {
		h := factory(t)

		var seq ids

		id := seq.next()

		require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
			if err := h.Store.Create(ctx, NewTask(id)); err != nil {
				return err
			}

			// A repository on a different connection could not see this write,
			// because it is not committed yet.
			_, err := h.Store.Get(ctx, id)

			return err
		}))
	})

	t.Run("a state change and its events commit together", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := NewTask(seq.next())

		require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
			if err := h.Store.Create(ctx, task); err != nil {
				return err
			}

			return h.Store.Append(ctx, []hmntsk.Event{
				NewEvent(task, hmntsk.EventTypeCreated, "event-1"),
			})
		}))

		stored, err := h.Store.Get(t.Context(), task.ID)
		require.NoError(t, err)
		assert.Equal(t, task.ID, stored.ID)

		events, err := h.Events(t.Context())
		require.NoError(t, err)
		require.Len(t, events, 1, "the event must be durable in the same commit")
		assert.Equal(t, hmntsk.EventTypeCreated, events[0].Type)
		assert.Equal(t, task.ID, events[0].TaskID)
		assert.Equal(t, "p-1", events[0].Correlation.OwnerRef)
	})

	t.Run("the sink declares itself transactional", func(t *testing.T) {
		h := factory(t)

		assert.True(t, h.Store.Transactional(),
			"a sink that cannot join the transaction is refused at construction, "+
				"so an adapter claiming otherwise cannot be wired at all")
	})
}
