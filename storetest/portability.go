package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// runPortabilityCases covers the places where dialects diverge in ways that
// change behaviour rather than syntax.
func runPortabilityCases(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("identifier comparison is case-sensitive", func(t *testing.T) {
		type testCase struct {
			name      string
			candidate string
			assert    func(t *testing.T, tasks []hmntsk.Task)
		}

		matched := func(t *testing.T, tasks []hmntsk.Task) {
			assert.Len(t, tasks, 1)
		}

		unmatched := func(t *testing.T, tasks []hmntsk.Task) {
			assert.Emptyf(t, tasks, "an identifier differing only in case must not match; "+
				"MySQL's default collation would say otherwise, which would silently change "+
				"who may claim a task")
		}

		cases := []testCase{
			{name: "an exact match is found", candidate: "Alice", assert: matched},
			{name: "a lowercased identifier does not match", candidate: "alice", assert: unmatched},
			{name: "an uppercased identifier does not match", candidate: "ALICE", assert: unmatched},
			{name: "a mixed-case identifier does not match", candidate: "aLiCe", assert: unmatched},
		}

		h := factory(t)

		var seq ids

		Seed(t, h, NewTask(seq.next(), func(task *hmntsk.Task) {
			task.Candidates = hmntsk.CandidatePool{Users: []string{"Alice"}}
			task.Assignee = ""
		}))

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				page, err := h.Store.Query(t.Context(), hmntsk.ResolvedQuery{
					Query: hmntsk.Query{Candidate: tc.candidate},
				})
				require.NoError(t, err)
				tc.assert(t, page.Tasks)
			})
		}
	})

	t.Run("task types and identifiers are case-sensitive too", func(t *testing.T) {
		h := factory(t)

		var seq ids

		Seed(t, h, NewTask(seq.next()))

		page, err := h.Store.Query(t.Context(), hmntsk.ResolvedQuery{
			Query: hmntsk.Query{Types: []string{"APPROVAL"}},
		})
		require.NoError(t, err)
		assert.Empty(t, page.Tasks)

		page, err = h.Store.Query(t.Context(), hmntsk.ResolvedQuery{
			Query: hmntsk.Query{Types: []string{TaskType}},
		})
		require.NoError(t, err)
		assert.Len(t, page.Tasks, 1)
	})

	t.Run("timestamps round-trip at microsecond precision", func(t *testing.T) {
		type testCase struct {
			name  string
			value time.Time
		}

		cases := []testCase{
			{name: "a whole second", value: time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)},
			{name: "one microsecond", value: time.Date(2026, time.March, 1, 12, 0, 0, 1000, time.UTC)},
			{name: "a full microsecond component", value: Reference},
			{
				name:  "the last microsecond of a second",
				value: time.Date(2026, time.March, 1, 12, 0, 0, 999999000, time.UTC),
			},
			{
				name:  "a distant deadline",
				value: time.Date(2099, time.December, 31, 23, 59, 59, 999999000, time.UTC),
			},
			{
				name:  "an instant beyond the 32-bit epoch ceiling",
				value: time.Date(2040, time.January, 1, 0, 0, 0, 0, time.UTC),
			},
		}

		h := factory(t)

		var seq ids

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				due := tc.value

				stored := Seed(t, h, NewTask(seq.next(), func(task *hmntsk.Task) {
					task.DueAt = &due
					task.CreatedAt = due
					task.UpdatedAt = due
				}))

				require.NotNil(t, stored.DueAt)
				assert.Truef(t, due.Equal(*stored.DueAt),
					"a deadline must read back identically: wrote %s, read %s", due, *stored.DueAt)
				assert.Equal(t, time.UTC, stored.DueAt.Location(),
					"timestamps are stored as UTC, never converted by the session's zone")
				assert.True(t, due.Equal(stored.CreatedAt))
			})
		}
	})

	t.Run("a timestamp supplied in another zone comes back as UTC", func(t *testing.T) {
		h := factory(t)

		var seq ids

		jakarta := time.FixedZone("WIB", 7*60*60)
		due := time.Date(2026, time.March, 1, 19, 0, 0, 123456000, jakarta)

		stored := Seed(t, h, NewTask(seq.next(), func(task *hmntsk.Task) { task.DueAt = &due }))

		require.NotNil(t, stored.DueAt)
		assert.True(t, due.Equal(*stored.DueAt), "the instant must be preserved")
		assert.Equal(t, time.UTC, stored.DueAt.Location())
	})

	t.Run("a duplicate identifier is refused", func(t *testing.T) {
		h := factory(t)

		var seq ids

		task := NewTask(seq.next())
		Seed(t, h, task)

		err := h.Store.Do(t.Context(), func(ctx context.Context) error {
			return h.Store.Create(ctx, task)
		})
		assert.ErrorIs(t, err, hmntsk.ErrConflict)
	})

	t.Run("an unknown task is reported as not found", func(t *testing.T) {
		h := factory(t)

		_, err := h.Store.Get(t.Context(), "no-such-task")
		assert.ErrorIs(t, err, hmntsk.ErrNotFound)
	})
}
