package hmntsk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

func TestServiceResolveCandidates(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		pool hmntsk.CandidatePool
		// resolver builds the directory the service is wired with. A nil
		// builder wires none at all.
		resolver func(ctrl *gomock.Controller) hmntsk.GroupResolver
		assert   func(t *testing.T, actors []string, err error)
	}

	quiet := func(ctrl *gomock.Controller) hmntsk.GroupResolver { return hmntsk.NewMockGroupResolver(ctrl) }
	static := func(*gomock.Controller) hmntsk.GroupResolver {
		return hmntsk.NewStaticAssignment(map[string][]string{
			"approvers": {"carol", "bob"},
			"managers":  {"bob", "frank"},
		})
	}

	cases := []testCase{
		{
			name:     "candidate users need no directory",
			pool:     hmntsk.CandidatePool{Users: []string{"alice", "bob"}},
			resolver: quiet,
			assert: func(t *testing.T, actors []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"alice", "bob"}, actors)
			},
		},
		{
			name:     "groups are expanded through the service's directory",
			pool:     hmntsk.CandidatePool{Groups: []string{"approvers"}},
			resolver: static,
			assert: func(t *testing.T, actors []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"bob", "carol"}, actors)
			},
		},
		{
			name: "exclusions are removed",
			pool: hmntsk.CandidatePool{
				Users: []string{"alice"}, Groups: []string{"approvers"}, Excluded: []string{"carol"},
			},
			resolver: static,
			assert: func(t *testing.T, actors []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"alice", "bob"}, actors)
			},
		},
		{
			name: "an actor named twice is listed once, in sorted order",
			pool: hmntsk.CandidatePool{
				Users: []string{"zoe", "bob"}, Groups: []string{"approvers", "managers"},
			},
			resolver: static,
			assert: func(t *testing.T, actors []string, err error) {
				require.NoError(t, err)
				assert.Equal(t, []string{"bob", "carol", "frank", "zoe"}, actors)
			},
		},
		{
			name: "a directory failure is a fault",
			pool: hmntsk.CandidatePool{Groups: []string{"approvers"}},
			resolver: func(ctrl *gomock.Controller) hmntsk.GroupResolver {
				resolver := hmntsk.NewMockGroupResolver(ctrl)
				resolver.EXPECT().MembersOf(gomock.Any(), "approvers").Return(nil, errDirectoryDown)

				return resolver
			},
			assert: func(t *testing.T, actors []string, err error) {
				require.ErrorIs(t, err, hmntsk.ErrGroupResolution)
				assert.ErrorIs(t, err, errDirectoryDown, "the directory's own error stays inspectable")
				assert.Empty(t, actors)
			},
		},
		{
			name:     "a group pool with no directory configured is a fault",
			pool:     hmntsk.CandidatePool{Groups: []string{"approvers"}},
			resolver: nil,
			assert: func(t *testing.T, actors []string, err error) {
				var resolution *hmntsk.GroupResolutionError

				require.ErrorAs(t, err, &resolution, "a missing directory is a wiring fault, never an empty pool")
				assert.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.Empty(t, actors)
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

			actors, err := svc.ResolveCandidates(t.Context(), tc.pool)
			tc.assert(t, actors, err)
		})
	}
}
