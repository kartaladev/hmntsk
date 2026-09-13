package notify_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

func TestUUIDv7Generator(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		assert func(t *testing.T, generator *notify.UUIDv7Generator)
	}

	cases := []testCase{
		{
			name: "identifiers are version 7 UUIDs",
			assert: func(t *testing.T, generator *notify.UUIDv7Generator) {
				id, err := generator.NewID()
				require.NoError(t, err)
				require.Len(t, id, 36)
				assert.Equal(t, byte('7'), id[14], "version nibble")
				assert.Contains(t, "89ab", string(id[19]), "variant bits")
			},
		},
		{
			name: "identifiers minted in a row sort in the order they were minted",
			assert: func(t *testing.T, generator *notify.UUIDv7Generator) {
				previous := ""

				for range 10_000 {
					id, err := generator.NewID()
					require.NoError(t, err)
					require.Greater(t, id, previous)

					previous = id
				}
			},
		},
		{
			name: "identifiers minted concurrently are unique",
			assert: func(t *testing.T, generator *notify.UUIDv7Generator) {
				var (
					mu   sync.Mutex
					seen = make(map[string]struct{})
					wg   sync.WaitGroup
				)

				for range 8 {
					wg.Go(func() {
						for range 1_000 {
							id, err := generator.NewID()
							assert.NoError(t, err)

							mu.Lock()
							seen[id] = struct{}{}
							mu.Unlock()
						}
					})
				}

				wg.Wait()
				assert.Len(t, seen, 8_000)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, notify.NewUUIDv7Generator())
		})
	}
}

func TestClocks(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)

	type testCase struct {
		name   string
		clock  notify.Clock
		assert func(t *testing.T, now time.Time)
	}

	cases := []testCase{
		{
			name:  "the system clock reads the current time",
			clock: notify.SystemClock{},
			assert: func(t *testing.T, now time.Time) {
				assert.WithinDuration(t, time.Now(), now, time.Second)
			},
		},
		{
			name:  "a clock function reads whatever it returns",
			clock: notify.ClockFunc(func() time.Time { return fixed }),
			assert: func(t *testing.T, now time.Time) {
				assert.Equal(t, fixed, now)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.clock.Now())
		})
	}
}
