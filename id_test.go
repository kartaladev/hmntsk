package hmntsk_test

import (
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

func TestUUIDv7GeneratorNewTaskIDSortsInCreationOrder(t *testing.T) {
	t.Parallel()

	const count = 500

	gen := hmntsk.NewUUIDv7Generator()

	ids := make([]string, 0, count)

	for range count {
		id, err := gen.NewTaskID()
		require.NoError(t, err)
		require.False(t, id.IsZero())

		ids = append(ids, id.String())
	}

	assert.True(t, slices.IsSorted(ids),
		"identifiers must sort lexically in creation order so keyset pagination is stable")

	assert.Len(t, slices.Compact(slices.Clone(ids)), count, "identifiers must be unique")

	for _, id := range ids {
		require.Len(t, id, 36)
		assert.Equalf(t, byte('7'), id[14], "%s must carry UUID version 7", id)
		assert.Containsf(t, "89ab", string(id[19]), "%s must carry the RFC 9562 variant bits", id)
	}
}

func TestUUIDv7GeneratorIsMonotonicUnderConcurrency(t *testing.T) {
	t.Parallel()

	const (
		workers  = 8
		perGroup = 200
	)

	gen := hmntsk.NewUUIDv7Generator()

	var (
		mu  sync.Mutex
		all []string
		wg  sync.WaitGroup
	)

	for range workers {
		wg.Go(func() {
			local := make([]string, 0, perGroup)

			for range perGroup {
				id, err := gen.NewTaskID()
				if err != nil {
					t.Error(err)

					return
				}

				local = append(local, id.String())
			}

			mu.Lock()
			defer mu.Unlock()

			all = append(all, local...)
		})
	}

	wg.Wait()

	slices.Sort(all)
	assert.Len(t, slices.Compact(all), workers*perGroup,
		"concurrent generation must not repeat an identifier")
}

func TestTaskIDIsZero(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		id     hmntsk.TaskID
		assert func(t *testing.T, zero bool)
	}

	cases := []testCase{
		{
			name:   "the empty identifier is zero",
			id:     hmntsk.TaskID(""),
			assert: func(t *testing.T, zero bool) { assert.True(t, zero) },
		},
		{
			name:   "a generated identifier is not zero",
			id:     hmntsk.TaskID("019243af-9f1c-7000-8000-0123456789ab"),
			assert: func(t *testing.T, zero bool) { assert.False(t, zero) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.id.IsZero())
		})
	}
}
