package hmntsk_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

func TestTaskJSONRoundTripPreservesPayloads(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		payload string
		assert  func(t *testing.T, decoded hmntsk.Task)
	}

	cases := []testCase{
		{
			name:    "an integer beyond exact float range survives",
			payload: `{"amount":9007199254740993}`,
			assert: func(t *testing.T, decoded hmntsk.Task) {
				assertVerbatim(t, `{"amount":9007199254740993}`, decoded.Input,
					"a payload decoded through float64 would come back as 9007199254740992")
			},
		},
		{
			name:    "a very large identifier survives",
			payload: `{"id":18446744073709551615}`,
			assert: func(t *testing.T, decoded hmntsk.Task) {
				assertVerbatim(t, `{"id":18446744073709551615}`, decoded.Input)
			},
		},
		{
			name:    "fields absent from any schema survive",
			payload: `{"known":1,"unknown":{"deep":[true,null,"x"]}}`,
			assert: func(t *testing.T, decoded hmntsk.Task) {
				assertVerbatim(t, `{"known":1,"unknown":{"deep":[true,null,"x"]}}`, decoded.Input)
			},
		},
		{
			name:    "field order is not rewritten",
			payload: `{"zulu":1,"alpha":2,"mike":3}`,
			assert: func(t *testing.T, decoded hmntsk.Task) {
				assertVerbatim(t, `{"zulu":1,"alpha":2,"mike":3}`, decoded.Input,
					"decoding into a map and re-encoding would sort these keys")
			},
		},
		{
			name:    "number formatting is not normalised",
			payload: `{"rate":1.500,"exp":1e3}`,
			assert: func(t *testing.T, decoded hmntsk.Task) {
				assertVerbatim(t, `{"rate":1.500,"exp":1e3}`, decoded.Input)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			original := hmntsk.Task{
				ID:      "019243af-9f1c-7000-8000-0123456789ab",
				Type:    "approval",
				Status:  hmntsk.StatusReady,
				Version: 3,
				Input:   json.RawMessage(tc.payload),
				Output:  json.RawMessage(tc.payload),
			}

			encoded, err := json.Marshal(original)
			require.NoError(t, err)

			var decoded hmntsk.Task

			require.NoError(t, json.Unmarshal(encoded, &decoded))

			assert.Equal(t, string(decoded.Input), string(decoded.Output),
				"input and output must be treated the same way")

			tc.assert(t, decoded)
		})
	}
}

func TestTaskJSONRoundTripPreservesStructure(t *testing.T) {
	t.Parallel()

	due := time.Date(2026, time.March, 1, 12, 0, 0, 123456000, time.UTC)
	lock := due.Add(time.Minute)

	original := hmntsk.Task{
		ID:            "019243af-9f1c-7000-8000-0123456789ab",
		Type:          "approval",
		Version:       7,
		Status:        hmntsk.StatusSuspended,
		SuspendedFrom: hmntsk.StatusInProgress,
		Priority:      hmntsk.PriorityHighest,
		Assignee:      "alice",
		Candidates: hmntsk.CandidatePool{
			Users:    []string{"alice", "bob"},
			Groups:   []string{"finance-approvers"},
			Excluded: []string{"bob"},
		},
		Correlation: hmntsk.CorrelationData{OwnerType: "process", OwnerRef: "p-1", ActivityKey: "a-1"},
		Callback: &hmntsk.CallbackTarget{
			Address:             "https://host.example/hook",
			ReferenceParameters: json.RawMessage(`{"corr":"abc"}`),
		},
		Escalation: &hmntsk.EscalationPolicy{
			Action:    hmntsk.EscalationWiden,
			AddGroups: []string{"managers"},
		},
		Input:       json.RawMessage(`{"amount":1}`),
		Progress:    json.RawMessage(`{"note":"half done"}`),
		CreatedAt:   due.Add(-time.Hour),
		UpdatedAt:   due.Add(-time.Minute),
		DueAt:       &due,
		LockedBy:    "sweeper-1",
		LockedUntil: &lock,
	}

	encoded, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded hmntsk.Task

	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assert.Equal(t, original.SuspendedFrom, decoded.SuspendedFrom)
	assert.Equal(t, original.Version, decoded.Version)
	assert.Equal(t, original.Candidates, decoded.Candidates)
	assert.Equal(t, original.Escalation, decoded.Escalation)
	require.NotNil(t, decoded.DueAt)
	assert.True(t, original.DueAt.Equal(*decoded.DueAt))
	require.NotNil(t, decoded.LockedUntil)
	assert.Equal(t, "sweeper-1", decoded.LockedBy)
	assert.True(t, original.LockedUntil.Equal(*decoded.LockedUntil))
}

func TestTaskCloneDoesNotAlias(t *testing.T) {
	t.Parallel()

	due := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	original := hmntsk.Task{
		Candidates:  hmntsk.CandidatePool{Users: []string{"alice"}, Groups: []string{"g"}},
		Correlation: hmntsk.CorrelationData{Extra: map[string]string{"k": "v"}},
		Callback:    &hmntsk.CallbackTarget{ReferenceParameters: json.RawMessage(`{"a":1}`)},
		Escalation:  &hmntsk.EscalationPolicy{AddGroups: []string{"managers"}},
		Input:       json.RawMessage(`{"amount":1}`),
		DueAt:       &due,
	}

	clone := original.Clone()
	clone.Candidates.Users[0] = "mallory"
	clone.Correlation.Extra["k"] = "tampered"
	clone.Callback.ReferenceParameters[0] = '['
	clone.Escalation.AddGroups[0] = "interns"
	clone.Input[1] = 'X'
	*clone.DueAt = due.Add(time.Hour)

	assert.Equal(t, "alice", original.Candidates.Users[0])
	assert.Equal(t, "v", original.Correlation.Extra["k"])
	assertVerbatim(t, `{"a":1}`, original.Callback.ReferenceParameters)
	assert.Equal(t, "managers", original.Escalation.AddGroups[0])
	assertVerbatim(t, `{"amount":1}`, original.Input)
	assert.True(t, due.Equal(*original.DueAt))
}

func TestTaskNormalizeTruncatesToMicrosecondUTC(t *testing.T) {
	t.Parallel()

	jakarta := time.FixedZone("WIB", 7*60*60)
	created := time.Date(2026, time.March, 1, 19, 0, 0, 123456789, jakarta)

	task := hmntsk.Task{CreatedAt: created, UpdatedAt: created, DueAt: &created}.Normalize()

	assert.Equal(t, time.UTC, task.CreatedAt.Location())
	assert.Equal(t, 123456000, task.CreatedAt.Nanosecond(),
		"nanoseconds beyond microsecond precision must be dropped, not rounded up")
	require.NotNil(t, task.DueAt)
	assert.Equal(t, 123456000, task.DueAt.Nanosecond())
}

func TestTaskIsOverdue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	type testCase struct {
		name   string
		task   hmntsk.Task
		assert func(t *testing.T, overdue bool)
	}

	yes := func(t *testing.T, overdue bool) { assert.True(t, overdue) }
	no := func(t *testing.T, overdue bool) { assert.False(t, overdue) }

	cases := []testCase{
		{
			name:   "a ready task past its deadline is overdue",
			task:   hmntsk.Task{Status: hmntsk.StatusReady, DueAt: &past},
			assert: yes,
		},
		{
			name:   "a task exactly at its deadline is overdue",
			task:   hmntsk.Task{Status: hmntsk.StatusReady, DueAt: &now},
			assert: yes,
		},
		{
			name:   "a task before its deadline is not overdue",
			task:   hmntsk.Task{Status: hmntsk.StatusReady, DueAt: &future},
			assert: no,
		},
		{
			name:   "a task without a deadline is never overdue",
			task:   hmntsk.Task{Status: hmntsk.StatusReady},
			assert: no,
		},
		{
			name:   "a suspended task is never overdue",
			task:   hmntsk.Task{Status: hmntsk.StatusSuspended, DueAt: &past},
			assert: no,
		},
		{
			name:   "a completed task is never overdue",
			task:   hmntsk.Task{Status: hmntsk.StatusCompleted, DueAt: &past},
			assert: no,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.task.IsOverdue(now))
		})
	}
}
