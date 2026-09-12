package storetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// runConcurrencyCases covers the conditional update that is the engine's only
// concurrency mechanism.
//
// Optimistic compare-and-swap is used rather than row locking because one
// supported dialect has no row-level locking at all: SELECT ... FOR UPDATE is
// absent there, not weaker. These cases therefore assert the outcome — exactly
// one winner — and never the mechanism.
func runConcurrencyCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a second claim against the same version loses", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := Seed(t, h, NewTask(seq.next()))

		claim := func(actor string) error {
			return h.Store.Do(t.Context(), func(ctx context.Context) error {
				claimed := task.Clone()
				claimed.Version = task.Version + 1
				claimed.Status = hmntsk.StatusReserved
				claimed.Assignee = actor

				return h.Store.Update(ctx, claimed, task.Version)
			})
		}

		require.NoError(t, claim(Assignee))

		err := claim(OtherActor)
		require.ErrorIs(t, err, hmntsk.ErrConflict)

		var conflict *hmntsk.ConflictError

		require.ErrorAs(t, err, &conflict)
		assert.Equal(t, task.Version+1, conflict.Current,
			"the loser must be told the version it has to re-read")

		stored, err := h.Store.Get(t.Context(), task.ID)
		require.NoError(t, err)
		assert.Equal(t, Assignee, stored.Assignee, "the first writer keeps the task")
	})

	t.Run("racing claims produce exactly one winner", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := Seed(t, h, NewTask(seq.next()))

		var (
			mu      sync.Mutex
			winners []string
			losers  []error
			wg      sync.WaitGroup
		)

		for _, actor := range []string{Assignee, OtherActor, "carol", "dave"} {
			wg.Go(func() {
				err := h.Store.Do(t.Context(), func(ctx context.Context) error {
					current, getErr := h.Store.Get(ctx, task.ID)
					if getErr != nil {
						return getErr
					}

					if current.Status != hmntsk.StatusReady {
						return &hmntsk.ConflictError{
							TaskID: task.ID, Expected: task.Version, Current: current.Version,
						}
					}

					claimed := current.Clone()
					claimed.Version = current.Version + 1
					claimed.Status = hmntsk.StatusReserved
					claimed.Assignee = actor

					return h.Store.Update(ctx, claimed, current.Version)
				})

				mu.Lock()
				defer mu.Unlock()

				if err != nil {
					losers = append(losers, err)

					return
				}

				winners = append(winners, actor)
			})
		}

		wg.Wait()

		require.Len(t, winners, 1, "exactly one actor may win the claim")
		assert.Len(t, losers, 3)

		for _, err := range losers {
			assert.ErrorIs(t, err, hmntsk.ErrConflict)
		}

		stored, err := h.Store.Get(t.Context(), task.ID)
		require.NoError(t, err)
		assert.Equal(t, winners[0], stored.Assignee)
		assert.Equal(t, task.Version+1, stored.Version)
	})

	t.Run("concurrent sweeps lease each overdue task once", func(t *testing.T) {
		h := factory(t)

		var seq ids

		past := Reference.Add(-time.Hour)

		wanted := make(map[hmntsk.TaskID]bool, 6)

		for range 6 {
			id := seq.next()
			wanted[id] = true

			Seed(t, h, NewTask(id, func(task *hmntsk.Task) { task.DueAt = &past }))
		}

		var (
			mu      sync.Mutex
			claimed []hmntsk.Task
			wg      sync.WaitGroup
		)

		for _, owner := range []string{"sweeper-1", "sweeper-2", "sweeper-3"} {
			wg.Go(func() {
				var batch []hmntsk.Task

				err := h.Store.Do(t.Context(), func(ctx context.Context) error {
					var claimErr error

					batch, claimErr = h.Store.ClaimOverdue(ctx, hmntsk.LeaseRequest{
						Now: Reference, Owner: owner, Duration: time.Minute, Limit: 10,
					})

					return claimErr
				})

				mu.Lock()
				defer mu.Unlock()

				if err == nil {
					claimed = append(claimed, batch...)

					return
				}

				// A conflict between sweeps is acceptable; losing a task is not.
				assert.ErrorIs(t, err, hmntsk.ErrConflict)
			})
		}

		wg.Wait()

		seen := make(map[hmntsk.TaskID]int, len(wanted))
		for _, task := range claimed {
			seen[task.ID]++
		}

		for id := range wanted {
			assert.LessOrEqualf(t, seen[id], 1, "task %s was claimed by more than one sweeper", id)
		}
	})
}
