package storetest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// runCancellationCases covers what happens when the caller goes away mid-scope.
func runCancellationCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a context cancelled inside the scope rolls it back", func(t *testing.T) {
		h := factory(t)

		var seq ids

		id := seq.next()

		ctx, cancel := context.WithCancel(t.Context())

		err := h.Store.Do(ctx, func(ctx context.Context) error {
			if createErr := h.Store.Create(ctx, NewTask(id)); createErr != nil {
				return createErr
			}

			// The client hangs up while the work is still in flight.
			cancel()

			return nil
		})
		require.Error(t, err, "a scope whose context died must not be committed")
		assert.ErrorIs(t, err, context.Canceled)

		_, getErr := h.Store.Get(t.Context(), id)
		assert.ErrorIs(t, getErr, hmntsk.ErrNotFound)

		events, err := h.Events(t.Context())
		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("a context cancelled before the scope starts commits nothing", func(t *testing.T) {
		h := factory(t)

		var seq ids

		id := seq.next()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := h.Store.Do(ctx, func(ctx context.Context) error {
			return h.Store.Create(ctx, NewTask(id))
		})
		require.Error(t, err)

		_, getErr := h.Store.Get(t.Context(), id)
		assert.ErrorIs(t, getErr, hmntsk.ErrNotFound)
	})
}
