package hmntsk_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// expectedLegal restates the transition table from the task-lifecycle
// capability independently of the implementation, so that the test fails if the
// production table drifts from the specification rather than agreeing with a
// mistake in it.
//
//	CREATED     -> READY, RESERVED (single candidate), ERROR
//	READY       -> RESERVED, SUSPENDED, OBSOLETE, EXITED
//	RESERVED    -> READY, IN_PROGRESS, RESERVED (delegate), SUSPENDED, OBSOLETE, EXITED
//	IN_PROGRESS -> RESERVED, COMPLETED, FAILED, ERROR, SUSPENDED, EXITED
//	SUSPENDED   -> the state occupied immediately before suspension, or EXITED
var expectedLegal = map[hmntsk.Status][]hmntsk.Status{
	hmntsk.StatusCreated: {
		hmntsk.StatusReady, hmntsk.StatusReserved, hmntsk.StatusError,
	},
	hmntsk.StatusReady: {
		hmntsk.StatusReserved, hmntsk.StatusSuspended, hmntsk.StatusObsolete, hmntsk.StatusExited,
	},
	hmntsk.StatusReserved: {
		hmntsk.StatusReady, hmntsk.StatusInProgress, hmntsk.StatusReserved,
		hmntsk.StatusSuspended, hmntsk.StatusObsolete, hmntsk.StatusExited,
	},
	hmntsk.StatusInProgress: {
		hmntsk.StatusReserved, hmntsk.StatusCompleted, hmntsk.StatusFailed,
		hmntsk.StatusError, hmntsk.StatusSuspended, hmntsk.StatusExited,
	},
	// The three suspendable states are exactly the values SuspendedFrom can
	// hold, so restoring "the state occupied immediately before suspension"
	// enumerates to these three.
	hmntsk.StatusSuspended: {
		hmntsk.StatusReady, hmntsk.StatusReserved, hmntsk.StatusInProgress, hmntsk.StatusExited,
	},
}

func TestCanTransitionCoversEveryPair(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		from   hmntsk.Status
		to     hmntsk.Status
		assert func(t *testing.T, allowed bool)
	}

	legal := func(t *testing.T, allowed bool) { assert.True(t, allowed, "must be permitted") }
	illegal := func(t *testing.T, allowed bool) { assert.False(t, allowed, "must be refused") }

	statuses := hmntsk.Statuses()
	require.Len(t, statuses, 10)

	cases := make([]testCase, 0, len(statuses)*len(statuses))

	for _, from := range statuses {
		for _, to := range statuses {
			tc := testCase{
				name:   fmt.Sprintf("%s to %s", from, to),
				from:   from,
				to:     to,
				assert: illegal,
			}

			for _, permitted := range expectedLegal[from] {
				if permitted == to {
					tc.assert = legal

					break
				}
			}

			cases = append(cases, tc)
		}
	}

	// Every terminal state must be a dead end, whatever the table says.
	for _, from := range statuses {
		if !from.IsTerminal() {
			continue
		}

		require.Emptyf(t, expectedLegal[from], "terminal state %s must have no outgoing transitions", from)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.CanTransition(tc.from, tc.to))
		})
	}
}

func TestLegalTransitionsMatchesSpecification(t *testing.T) {
	t.Parallel()

	got := make(map[hmntsk.Transition]bool)
	for _, tr := range hmntsk.LegalTransitions() {
		assert.Falsef(t, got[tr], "duplicate entry %s in the transition table", tr)
		got[tr] = true
	}

	want := make(map[hmntsk.Transition]bool)

	for from, tos := range expectedLegal {
		for _, to := range tos {
			want[hmntsk.Transition{From: from, To: to}] = true
		}
	}

	assert.Equal(t, want, got)
}

func TestAllowedTransitions(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		from   hmntsk.Status
		assert func(t *testing.T, allowed []hmntsk.Status)
	}

	cases := []testCase{
		{
			name: "a suspended task may return to any suspendable state or be cancelled",
			from: hmntsk.StatusSuspended,
			assert: func(t *testing.T, allowed []hmntsk.Status) {
				assert.ElementsMatch(t, []hmntsk.Status{
					hmntsk.StatusReady, hmntsk.StatusReserved,
					hmntsk.StatusInProgress, hmntsk.StatusExited,
				}, allowed)
			},
		},
		{
			name: "a reserved task may be delegated to itself",
			from: hmntsk.StatusReserved,
			assert: func(t *testing.T, allowed []hmntsk.Status) {
				assert.Contains(t, allowed, hmntsk.StatusReserved)
			},
		},
		{
			name: "a completed task is a dead end",
			from: hmntsk.StatusCompleted,
			assert: func(t *testing.T, allowed []hmntsk.Status) {
				assert.Empty(t, allowed)
			},
		},
		{
			name: "an unknown state is a dead end",
			from: hmntsk.Status("NOPE"),
			assert: func(t *testing.T, allowed []hmntsk.Status) {
				assert.Empty(t, allowed)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.AllowedTransitions(tc.from))
		})
	}
}
