package hmntsk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

func TestStatusIsTerminal(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		status hmntsk.Status
		assert func(t *testing.T, terminal bool)
	}

	nonTerminal := func(t *testing.T, terminal bool) {
		t.Helper()
		assert.False(t, terminal)
	}

	terminal := func(t *testing.T, term bool) {
		t.Helper()
		assert.True(t, term)
	}

	cases := []testCase{
		{name: "CREATED is not terminal", status: hmntsk.StatusCreated, assert: nonTerminal},
		{name: "READY is not terminal", status: hmntsk.StatusReady, assert: nonTerminal},
		{name: "RESERVED is not terminal", status: hmntsk.StatusReserved, assert: nonTerminal},
		{name: "IN_PROGRESS is not terminal", status: hmntsk.StatusInProgress, assert: nonTerminal},
		{name: "SUSPENDED is not terminal", status: hmntsk.StatusSuspended, assert: nonTerminal},
		{name: "COMPLETED is terminal", status: hmntsk.StatusCompleted, assert: terminal},
		{name: "FAILED is terminal", status: hmntsk.StatusFailed, assert: terminal},
		{name: "ERROR is terminal", status: hmntsk.StatusError, assert: terminal},
		{name: "EXITED is terminal", status: hmntsk.StatusExited, assert: terminal},
		{name: "OBSOLETE is terminal", status: hmntsk.StatusObsolete, assert: terminal},
		{name: "an unknown value is not terminal", status: hmntsk.Status("NOPE"), assert: nonTerminal},
	}

	// Guard against a state being added to the type but forgotten here.
	covered := make(map[hmntsk.Status]bool, len(cases))
	for _, tc := range cases {
		covered[tc.status] = true
	}

	for _, status := range hmntsk.Statuses() {
		require.Truef(t, covered[status], "status %s has no terminality case", status)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.status.IsTerminal())
		})
	}
}

func TestStatusValid(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		status hmntsk.Status
		assert func(t *testing.T, valid bool)
	}

	cases := []testCase{
		{
			name:   "a defined state is valid",
			status: hmntsk.StatusReady,
			assert: func(t *testing.T, valid bool) { assert.True(t, valid) },
		},
		{
			name:   "the empty string is not a state",
			status: hmntsk.Status(""),
			assert: func(t *testing.T, valid bool) { assert.False(t, valid) },
		},
		{
			name:   "case matters",
			status: hmntsk.Status("ready"),
			assert: func(t *testing.T, valid bool) { assert.False(t, valid) },
		},
		{
			name:   "an undefined state is not valid",
			status: hmntsk.Status("ARCHIVED"),
			assert: func(t *testing.T, valid bool) { assert.False(t, valid) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.status.Valid())
		})
	}
}

func TestStatusIsSuspendable(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		status hmntsk.Status
		assert func(t *testing.T, suspendable bool)
	}

	yes := func(t *testing.T, suspendable bool) { assert.True(t, suspendable) }
	no := func(t *testing.T, suspendable bool) { assert.False(t, suspendable) }

	cases := []testCase{
		{name: "CREATED", status: hmntsk.StatusCreated, assert: no},
		{name: "READY", status: hmntsk.StatusReady, assert: yes},
		{name: "RESERVED", status: hmntsk.StatusReserved, assert: yes},
		{name: "IN_PROGRESS", status: hmntsk.StatusInProgress, assert: yes},
		{name: "SUSPENDED", status: hmntsk.StatusSuspended, assert: no},
		{name: "COMPLETED", status: hmntsk.StatusCompleted, assert: no},
		{name: "FAILED", status: hmntsk.StatusFailed, assert: no},
		{name: "ERROR", status: hmntsk.StatusError, assert: no},
		{name: "EXITED", status: hmntsk.StatusExited, assert: no},
		{name: "OBSOLETE", status: hmntsk.StatusObsolete, assert: no},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.status.IsSuspendable())
		})
	}
}

func TestPriorityValid(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		priority hmntsk.Priority
		assert   func(t *testing.T, valid bool)
	}

	cases := []testCase{
		{
			name:     "the most urgent priority is in range",
			priority: hmntsk.PriorityHighest,
			assert:   func(t *testing.T, valid bool) { assert.True(t, valid) },
		},
		{
			name:     "the default priority is in range",
			priority: hmntsk.PriorityDefault,
			assert:   func(t *testing.T, valid bool) { assert.True(t, valid) },
		},
		{
			name:     "the least urgent priority is in range",
			priority: hmntsk.PriorityLowest,
			assert:   func(t *testing.T, valid bool) { assert.True(t, valid) },
		},
		{
			name:     "below the range is rejected",
			priority: hmntsk.PriorityHighest - 1,
			assert:   func(t *testing.T, valid bool) { assert.False(t, valid) },
		},
		{
			name:     "above the range is rejected",
			priority: hmntsk.PriorityLowest + 1,
			assert:   func(t *testing.T, valid bool) { assert.False(t, valid) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.priority.Valid())
		})
	}
}
