package transportcore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

func TestTaskReadEligibleIsLazy(t *testing.T) {
	t.Parallel()

	calls := 0

	read := transportcore.NewTaskRead("alice", hmntsk.Task{ID: "t"}, func(context.Context) (bool, error) {
		calls++

		return true, nil
	})

	require.Zero(t, calls, "building a read resolves nothing")

	eligible, err := read.Eligible(t.Context())

	require.NoError(t, err)
	assert.True(t, eligible)
	assert.Equal(t, 1, calls, "eligibility is resolved when, and only when, a policy asks")
}

func TestParticipantsOnly(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		actor    string
		task     hmntsk.Task
		eligible func(t *testing.T) func(context.Context) (bool, error)
		assert   func(t *testing.T, err error)
	}

	task := hmntsk.Task{
		ID: "t", Assignee: "alice", CreatedBy: "owner",
		Candidates: hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
	}

	// unasked fails the case if the policy resolves eligibility it does not need.
	unasked := func(t *testing.T) func(context.Context) (bool, error) {
		return func(context.Context) (bool, error) {
			t.Error("the policy resolved eligibility for a read it could decide without it")

			return false, nil
		}
	}

	answers := func(eligible bool, err error) func(t *testing.T) func(context.Context) (bool, error) {
		return func(*testing.T) func(context.Context) (bool, error) {
			return func(context.Context) (bool, error) { return eligible, err }
		}
	}

	permitted := func(t *testing.T, err error) { require.NoError(t, err) }

	refused := func(t *testing.T, err error) {
		require.ErrorIs(t, err, hmntsk.ErrUnauthorized,
			"a host composing the default can tell a refusal from a failure")
	}

	directoryDown := &hmntsk.GroupResolutionError{Actor: "bob", Cause: errors.New("directory unreachable")}

	cases := []testCase{
		{name: "the holder may read without eligibility being resolved", actor: "alice", task: task, eligible: unasked, assert: permitted},
		{name: "the creator may read without eligibility being resolved", actor: "owner", task: task, eligible: unasked, assert: permitted},
		{name: "an eligible candidate may read", actor: "bob", task: task, eligible: answers(true, nil), assert: permitted},
		{name: "an ineligible outsider is refused", actor: "carol", task: task, eligible: answers(false, nil), assert: refused},
		{name: "no acting user is refused", actor: "", task: task, eligible: unasked, assert: refused},
		{
			name: "a directory failure is returned as itself, not as a refusal", actor: "bob", task: task,
			eligible: answers(false, directoryDown),
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrGroupResolution)
				assert.NotErrorIs(t, err, hmntsk.ErrUnauthorized,
					"the engine could not decide, which is not deciding against the caller")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			read := transportcore.NewTaskRead(tc.actor, tc.task, tc.eligible(t))

			tc.assert(t, transportcore.ParticipantsOnly.AuthorizeRead(t.Context(), read))
		})
	}
}

func TestTaskReadAuthorizerFuncPassesItsArgumentsThrough(t *testing.T) {
	t.Parallel()

	refusal := errors.New("closed")

	var got transportcore.TaskRead

	policy := transportcore.TaskReadAuthorizerFunc(func(_ context.Context, read transportcore.TaskRead) error {
		got = read

		return refusal
	})

	err := policy.AuthorizeRead(t.Context(), transportcore.NewTaskRead("carol", hmntsk.Task{ID: "t-9"}, nil))

	require.ErrorIs(t, err, refusal)
	assert.Equal(t, "carol", got.Actor)
	assert.Equal(t, hmntsk.TaskID("t-9"), got.Task.ID)
}

func TestAllowAllPermitsEveryRead(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		read   transportcore.TaskRead
		assert func(t *testing.T, err error)
	}

	permitted := func(t *testing.T, err error) { require.NoError(t, err) }

	cases := []testCase{
		{name: "an outsider", read: transportcore.NewTaskRead("carol", hmntsk.Task{ID: "t", Assignee: "alice"}, nil), assert: permitted},
		{name: "no acting user", read: transportcore.NewTaskRead("", hmntsk.Task{ID: "t"}, nil), assert: permitted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, transportcore.AllowAll.AuthorizeRead(t.Context(), tc.read))
		})
	}

	// The one opt-out still satisfies the query policy it always did.
	var _ transportcore.QueryAuthorizer = transportcore.AllowAll
}

func TestNewRefusesANilTaskReadAuthorizer(t *testing.T) {
	t.Parallel()

	svc, err := hmntsk.New(memstore.New())
	require.NoError(t, err)

	api, err := transportcore.New(svc, transportcore.WithTaskReadAuthorizer(nil))

	require.ErrorIs(t, err, hmntsk.ErrConfiguration,
		"a missing read policy is a wiring mistake, refused before any traffic")
	assert.Contains(t, err.Error(), "AllowAll", "the error names the explicit opt-out")
	assert.Nil(t, api)
}
