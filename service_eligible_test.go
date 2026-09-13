package hmntsk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

func TestServiceEligible(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name  string
		pool  hmntsk.CandidatePool
		actor string
		// resolver builds the directory the service is wired with. A nil
		// builder wires none at all.
		resolver func(ctrl *gomock.Controller) hmntsk.GroupResolver
		assert   func(t *testing.T, eligible bool, err error)
	}

	quiet := func(ctrl *gomock.Controller) hmntsk.GroupResolver { return hmntsk.NewMockGroupResolver(ctrl) }

	cases := []testCase{
		{
			name:     "a candidate user is eligible",
			pool:     hmntsk.CandidatePool{Users: []string{"alice"}},
			actor:    "alice",
			resolver: quiet,
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.True(t, eligible)
			},
		},
		{
			name:  "a member of a candidate group is eligible",
			pool:  hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			actor: "alice",
			resolver: func(ctrl *gomock.Controller) hmntsk.GroupResolver {
				resolver := hmntsk.NewMockGroupResolver(ctrl)
				resolver.EXPECT().GroupsOf(gomock.Any(), "alice").Return([]string{"finance-approvers"}, nil)

				return resolver
			},
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.True(t, eligible)
			},
		},
		{
			name: "an excluded member is refused without consulting the directory",
			pool: hmntsk.CandidatePool{
				Groups: []string{"finance-approvers"}, Excluded: []string{"alice"},
			},
			actor:    "alice",
			resolver: quiet,
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.False(t, eligible)
			},
		},
		{
			name:  "an outsider is not eligible",
			pool:  hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			actor: "carol",
			resolver: func(ctrl *gomock.Controller) hmntsk.GroupResolver {
				resolver := hmntsk.NewMockGroupResolver(ctrl)
				resolver.EXPECT().GroupsOf(gomock.Any(), "carol").Return([]string{"sales"}, nil)

				return resolver
			},
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.False(t, eligible)
			},
		},
		{
			name:     "no actor is never eligible",
			pool:     hmntsk.CandidatePool{Users: []string{""}, Groups: []string{"finance-approvers"}},
			actor:    "",
			resolver: quiet,
			assert: func(t *testing.T, eligible bool, err error) {
				require.NoError(t, err)
				assert.False(t, eligible)
			},
		},
		{
			name:  "a directory failure is a fault, not a denial",
			pool:  hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			actor: "alice",
			resolver: func(ctrl *gomock.Controller) hmntsk.GroupResolver {
				resolver := hmntsk.NewMockGroupResolver(ctrl)
				resolver.EXPECT().GroupsOf(gomock.Any(), "alice").Return(nil, errDirectoryDown)

				return resolver
			},
			assert: func(t *testing.T, eligible bool, err error) {
				require.ErrorIs(t, err, hmntsk.ErrGroupResolution)
				assert.ErrorIs(t, err, errDirectoryDown, "the directory's own error stays inspectable")
				assert.False(t, eligible)
			},
		},
		{
			name:     "a group pool with no directory configured is a fault",
			pool:     hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
			actor:    "alice",
			resolver: nil,
			assert: func(t *testing.T, eligible bool, err error) {
				require.ErrorIs(t, err, hmntsk.ErrGroupResolution,
					"a missing directory is a wiring fault, never a quiet refusal")
				assert.False(t, eligible)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var opts []hmntsk.Option
			if tc.resolver != nil {
				opts = append(opts, hmntsk.WithGroupResolver(tc.resolver(gomock.NewController(t))))
			}

			svc, err := hmntsk.New(memstore.New(), opts...)
			require.NoError(t, err)

			task := hmntsk.Task{ID: "task-1", Candidates: tc.pool}

			eligible, err := svc.Eligible(t.Context(), task, tc.actor)
			tc.assert(t, eligible, err)
		})
	}
}
