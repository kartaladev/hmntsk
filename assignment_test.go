package hmntsk_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk"
)

var errDirectoryDown = errors.New("directory unreachable")

// testDirectory is the fixed organisation every assignment case is evaluated
// against.
func testDirectory() *hmntsk.StaticAssignment {
	return hmntsk.NewStaticAssignment(map[string][]string{
		"finance-approvers": {"alice", "bob"},
		"managers":          {"carol"},
		"empty-team":        {},
	})
}

func TestIsEligible(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		pool   hmntsk.CandidatePool
		actor  string
		assert func(t *testing.T, eligible bool, err error)
	}

	yes := func(t *testing.T, eligible bool, err error) {
		require.NoError(t, err)
		assert.True(t, eligible)
	}

	no := func(t *testing.T, eligible bool, err error) {
		require.NoError(t, err)
		assert.False(t, eligible)
	}

	cases := []testCase{
		{
			name:   "a candidate user is eligible",
			pool:   hmntsk.CandidatePool{Users: []string{"alice"}},
			actor:  "alice",
			assert: yes,
		},
		{
			name:   "a member of a candidate group is eligible",
			pool:   hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			actor:  "bob",
			assert: yes,
		},
		{
			name:   "an actor outside the pool is not eligible",
			pool:   hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			actor:  "carol",
			assert: no,
		},
		{
			name:   "an excluded actor is not eligible",
			pool:   hmntsk.CandidatePool{Groups: []string{"finance-approvers"}, Excluded: []string{"bob"}},
			actor:  "bob",
			assert: no,
		},
		{
			name: "exclusion overrides being listed as a candidate user",
			pool: hmntsk.CandidatePool{
				Users: []string{"alice"}, Excluded: []string{"alice"},
			},
			actor:  "alice",
			assert: no,
		},
		{
			name: "exclusion overrides both listings at once",
			pool: hmntsk.CandidatePool{
				Users:    []string{"alice"},
				Groups:   []string{"finance-approvers"},
				Excluded: []string{"alice"},
			},
			actor:  "alice",
			assert: no,
		},
		{
			name:   "eligibility is case-sensitive",
			pool:   hmntsk.CandidatePool{Users: []string{"alice"}},
			actor:  "Alice",
			assert: no,
		},
		{
			name:   "an empty pool makes nobody eligible",
			pool:   hmntsk.CandidatePool{},
			actor:  "alice",
			assert: no,
		},
		{
			name:   "an empty actor is never eligible",
			pool:   hmntsk.CandidatePool{Users: []string{"alice"}},
			actor:  "",
			assert: no,
		},
	}

	resolver := testDirectory()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			eligible, err := hmntsk.IsEligible(t.Context(), resolver, tc.pool, tc.actor)
			tc.assert(t, eligible, err)
		})
	}
}

func TestIsEligibleDistinguishesFaultFromDenial(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		pool   hmntsk.CandidatePool
		expect func(resolver *hmntsk.MockGroupResolver)
		assert func(t *testing.T, eligible bool, err error)
	}

	cases := []testCase{
		{
			name: "a resolver failure is a fault, not a denial",
			pool: hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			expect: func(resolver *hmntsk.MockGroupResolver) {
				resolver.EXPECT().
					GroupsOf(gomock.Any(), "alice").
					Return(nil, errDirectoryDown)
			},
			assert: func(t *testing.T, eligible bool, err error) {
				require.ErrorIs(t, err, hmntsk.ErrGroupResolution)
				assert.ErrorIs(t, err, errDirectoryDown, "the resolver's own error stays inspectable")
				assert.NotErrorIs(t, err, hmntsk.ErrUnauthorized,
					"a directory outage must not be reported as a permission denial")
				assert.False(t, eligible)
			},
		},
		{
			name: "exclusion is decided without consulting the directory at all",
			pool: hmntsk.CandidatePool{
				Groups: []string{"finance-approvers"}, Excluded: []string{"alice"},
			},
			expect: func(_ *hmntsk.MockGroupResolver) {},
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.False(t, eligible)
			},
		},
		{
			name: "a candidate user is decided without consulting the directory",
			pool: hmntsk.CandidatePool{
				Users: []string{"alice"}, Groups: []string{"finance-approvers"},
			},
			expect: func(_ *hmntsk.MockGroupResolver) {},
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.True(t, eligible)
			},
		},
		{
			name:   "a pool with no groups never reaches the directory",
			pool:   hmntsk.CandidatePool{Users: []string{"bob"}},
			expect: func(_ *hmntsk.MockGroupResolver) {},
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.False(t, eligible)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := hmntsk.NewMockGroupResolver(gomock.NewController(t))
			tc.expect(resolver)

			eligible, err := hmntsk.IsEligible(t.Context(), resolver, tc.pool, "alice")
			tc.assert(t, eligible, err)
		})
	}
}

func TestIsEligibleReflectsLiveMembership(t *testing.T) {
	t.Parallel()

	pool := hmntsk.CandidatePool{Groups: []string{"finance-approvers"}}

	before := hmntsk.NewStaticAssignment(map[string][]string{"finance-approvers": {"alice"}})
	after := hmntsk.NewStaticAssignment(map[string][]string{"finance-approvers": {"alice", "dave"}})

	eligible, err := hmntsk.IsEligible(t.Context(), before, pool, "dave")
	require.NoError(t, err)
	assert.False(t, eligible)

	eligible, err = hmntsk.IsEligible(t.Context(), after, pool, "dave")
	require.NoError(t, err)
	assert.True(t, eligible,
		"membership is resolved at the moment of the operation, never snapshotted at creation")
}

func TestResolveCandidates(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		pool   hmntsk.CandidatePool
		assert func(t *testing.T, eligible []string, err error)
	}

	cases := []testCase{
		{
			name: "users and group members are merged and deduplicated",
			pool: hmntsk.CandidatePool{
				Users: []string{"alice", "dave"}, Groups: []string{"finance-approvers", "managers"},
			},
			assert: func(t *testing.T, eligible []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"alice", "bob", "carol", "dave"}, eligible)
			},
		},
		{
			name: "exclusions are removed after expansion",
			pool: hmntsk.CandidatePool{
				Groups: []string{"finance-approvers"}, Excluded: []string{"bob"},
			},
			assert: func(t *testing.T, eligible []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"alice"}, eligible)
			},
		},
		{
			name: "an empty group contributes nobody and is not an error",
			pool: hmntsk.CandidatePool{Groups: []string{"empty-team"}},
			assert: func(t *testing.T, eligible []string, err error) {
				require.NoError(t, err)
				assert.Empty(t, eligible)
			},
		},
		{
			name: "an unknown group contributes nobody and is not an error",
			pool: hmntsk.CandidatePool{Groups: []string{"no-such-group"}},
			assert: func(t *testing.T, eligible []string, err error) {
				require.NoError(t, err)
				assert.Empty(t, eligible)
			},
		},
	}

	resolver := testDirectory()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			eligible, err := hmntsk.ResolveCandidates(t.Context(), resolver, tc.pool)
			tc.assert(t, eligible, err)
		})
	}
}

func TestAssignPlacesANewTask(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		pool     hmntsk.CandidatePool
		resolver hmntsk.GroupResolver
		strategy hmntsk.AssignmentStrategy
		assert   func(t *testing.T, task hmntsk.Task, events []hmntsk.Event, err error)
	}

	cases := []testCase{
		{
			name:     "a pool of one is reserved for that actor",
			pool:     hmntsk.CandidatePool{Users: []string{"alice"}},
			resolver: testDirectory(),
			assert: func(t *testing.T, task hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, task.Status)
				assert.Equal(t, "alice", task.Assignee)
				require.Len(t, events, 1)
				assert.Equal(t, hmntsk.EventTypeCreated, events[0].Type)
			},
		},
		{
			name:     "a group resolving to one actor is reserved too",
			pool:     hmntsk.CandidatePool{Groups: []string{"managers"}},
			resolver: testDirectory(),
			assert: func(t *testing.T, task hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, task.Status)
				assert.Equal(t, "carol", task.Assignee)
			},
		},
		{
			name:     "a pool of several stays in the pool",
			pool:     hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			resolver: testDirectory(),
			assert: func(t *testing.T, task hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReady, task.Status)
				assert.Empty(t, task.Assignee)
			},
		},
		{
			name:     "a pool reduced to one by exclusion is reserved",
			pool:     hmntsk.CandidatePool{Groups: []string{"finance-approvers"}, Excluded: []string{"bob"}},
			resolver: testDirectory(),
			assert: func(t *testing.T, task hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, task.Status)
				assert.Equal(t, "alice", task.Assignee)
			},
		},
		{
			name:     "an empty pool faults the task rather than orphaning it",
			pool:     hmntsk.CandidatePool{Groups: []string{"empty-team"}},
			resolver: testDirectory(),
			assert: func(t *testing.T, task hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusError, task.Status)
				assert.Contains(t, task.Reason, "no actor is eligible")
				require.Len(t, events, 1)
				assert.Equal(t, hmntsk.EventTypeErrored, events[0].Type)
				assert.Contains(t, events[0].Transition.Comment, "no actor is eligible",
					"the reason must be recorded in history")
			},
		},
		{
			name:     "a pool excluded down to nobody faults the task",
			pool:     hmntsk.CandidatePool{Users: []string{"alice"}, Excluded: []string{"alice"}},
			resolver: testDirectory(),
			assert: func(t *testing.T, task hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusError, task.Status)
			},
		},
		{
			name:     "a directory that cannot answer faults the task",
			pool:     hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			resolver: failingResolver{},
			assert: func(t *testing.T, task hmntsk.Task, events []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusError, task.Status,
					"a resolution failure during creation produces an ERROR task, not a returned error")
				assert.Contains(t, task.Reason, "candidate resolution failed")
				require.Len(t, events, 1)
				assert.Equal(t, hmntsk.EventTypeErrored, events[0].Type)
			},
		},
		{
			name:     "a strategy that picks an ineligible actor faults the task",
			pool:     hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			resolver: testDirectory(),
			strategy: fixedStrategy{actor: "mallory"},
			assert: func(t *testing.T, task hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusError, task.Status)
				assert.Contains(t, task.Reason, "ineligible actor")
			},
		},
		{
			name:     "a strategy may reserve out of a pool of several",
			pool:     hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			resolver: testDirectory(),
			strategy: fixedStrategy{actor: "bob"},
			assert: func(t *testing.T, task hmntsk.Task, _ []hmntsk.Event, err error) {
				require.NoError(t, err)
				assert.Equal(t, hmntsk.StatusReserved, task.Status)
				assert.Equal(t, "bob", task.Assignee)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			task := hmntsk.Task{
				ID:         "task-1",
				Type:       "approval",
				Status:     hmntsk.StatusCreated,
				Candidates: tc.pool,
				CreatedBy:  "system",
			}

			next, events, err := hmntsk.Assign(t.Context(), tc.resolver, tc.strategy, task, testNow)
			tc.assert(t, next, events, err)
		})
	}
}

func TestAuthorizePerOperation(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		task   hmntsk.Task
		op     hmntsk.Operation
		actor  string
		assert func(t *testing.T, err error)
	}

	permitted := func(t *testing.T, err error) { require.NoError(t, err) }

	refused := func(t *testing.T, err error) {
		require.ErrorIs(t, err, hmntsk.ErrUnauthorized)
	}

	reserved := func() hmntsk.Task {
		task := fixture(hmntsk.StatusReserved)
		task.Candidates = hmntsk.CandidatePool{Groups: []string{"finance-approvers"}}

		return task
	}

	pooled := func() hmntsk.Task {
		task := fixture(hmntsk.StatusReady)
		task.Candidates = hmntsk.CandidatePool{Groups: []string{"finance-approvers"}, Excluded: []string{"bob"}}

		return task
	}

	cases := []testCase{
		{name: "an eligible actor may claim", task: pooled(), op: hmntsk.OpClaim, actor: "alice", assert: permitted},
		{name: "an excluded actor may not claim", task: pooled(), op: hmntsk.OpClaim, actor: "bob", assert: refused},
		{name: "an outsider may not claim", task: pooled(), op: hmntsk.OpClaim, actor: "carol", assert: refused},
		{name: "no actor at all may not claim", task: pooled(), op: hmntsk.OpClaim, actor: "", assert: refused},

		{name: "the assignee may release", task: reserved(), op: hmntsk.OpRelease, actor: "alice", assert: permitted},
		{name: "a non-assignee may not release", task: reserved(), op: hmntsk.OpRelease, actor: "bob", assert: refused},
		{name: "the assignee may start", task: reserved(), op: hmntsk.OpStart, actor: "alice", assert: permitted},
		{name: "a non-assignee may not start", task: reserved(), op: hmntsk.OpStart, actor: "bob", assert: refused},
		{name: "the assignee may save progress", task: reserved(), op: hmntsk.OpSaveProgress, actor: "alice", assert: permitted},
		{name: "a non-assignee may not save progress", task: reserved(), op: hmntsk.OpSaveProgress, actor: "bob", assert: refused},
		{name: "the assignee may complete", task: reserved(), op: hmntsk.OpComplete, actor: "alice", assert: permitted},
		{name: "a non-assignee may not complete", task: reserved(), op: hmntsk.OpComplete, actor: "bob", assert: refused},
		{name: "the assignee may fail", task: reserved(), op: hmntsk.OpFail, actor: "alice", assert: permitted},
		{name: "a non-assignee may not fail", task: reserved(), op: hmntsk.OpFail, actor: "bob", assert: refused},
		{name: "the assignee may suspend", task: reserved(), op: hmntsk.OpSuspend, actor: "alice", assert: permitted},
		{name: "a non-assignee may not suspend", task: reserved(), op: hmntsk.OpSuspend, actor: "bob", assert: refused},
		{name: "the assignee may resume", task: reserved(), op: hmntsk.OpResume, actor: "alice", assert: permitted},
		{name: "a non-assignee may not resume", task: reserved(), op: hmntsk.OpResume, actor: "bob", assert: refused},
		{name: "eligibility for an unheld task does not authorise completing it", task: pooled(), op: hmntsk.OpComplete, actor: "alice", assert: refused},
	}

	resolver := testDirectory()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.Authorize(t.Context(), resolver, tc.task, tc.op, tc.actor))
		})
	}
}

func TestAuthorizeDelegate(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		actor  string
		target string
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "the assignee may delegate to an eligible actor", actor: "alice", target: "bob",
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name: "delegating to an ineligible actor is refused", actor: "alice", target: "carol",
			assert: func(t *testing.T, err error) { require.ErrorIs(t, err, hmntsk.ErrUnauthorized) },
		},
		{
			name: "delegating to an excluded actor is refused", actor: "alice", target: "dave",
			assert: func(t *testing.T, err error) { require.ErrorIs(t, err, hmntsk.ErrUnauthorized) },
		},
		{
			name: "a non-assignee may not delegate", actor: "bob", target: "alice",
			assert: func(t *testing.T, err error) { require.ErrorIs(t, err, hmntsk.ErrUnauthorized) },
		},
	}

	resolver := hmntsk.NewStaticAssignment(map[string][]string{
		"finance-approvers": {"alice", "bob", "dave"},
	})

	task := fixture(hmntsk.StatusReserved)
	task.Candidates = hmntsk.CandidatePool{
		Groups: []string{"finance-approvers"}, Excluded: []string{"dave"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.AuthorizeDelegate(t.Context(), resolver, task, tc.actor, tc.target))
		})
	}
}

// failingResolver is a directory that is always down.
type failingResolver struct{}

func (failingResolver) GroupsOf(context.Context, string) ([]string, error) {
	return nil, errDirectoryDown
}

func (failingResolver) MembersOf(context.Context, string) ([]string, error) {
	return nil, errDirectoryDown
}

// fixedStrategy always names the same actor.
type fixedStrategy struct{ actor string }

func (s fixedStrategy) Assign(context.Context, hmntsk.Task, []string) (string, error) {
	return s.actor, nil
}
