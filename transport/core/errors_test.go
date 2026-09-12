package transportcore_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

func TestErrorMapping(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		err    error
		status int
		code   transportcore.ErrorCode
	}

	cases := []testCase{
		{
			name:   "a stale version is a conflict",
			err:    &hmntsk.ConflictError{TaskID: "t", Expected: 1, Current: 4},
			status: transportcore.StatusConflict, code: transportcore.CodeConflict,
		},
		{
			name: "an illegal transition is a conflict, not a validation failure",
			err: &hmntsk.TransitionError{
				TaskID: "t", Operation: "Complete",
				From: hmntsk.StatusReady, To: hmntsk.StatusCompleted,
			},
			status: transportcore.StatusConflict, code: transportcore.CodeConflict,
		},
		{
			name:   "an unknown task is not found",
			err:    &hmntsk.NotFoundError{TaskID: "t"},
			status: transportcore.StatusNotFound, code: transportcore.CodeNotFound,
		},
		{
			name:   "a schema failure is a bad request",
			err:    &hmntsk.ValidationError{Subject: "input"},
			status: transportcore.StatusBadRequest, code: transportcore.CodeValidation,
		},
		{
			name:   "an unregistered type is a bad request with its own code",
			err:    &hmntsk.UnregisteredTypeError{Type: "approvel"},
			status: transportcore.StatusBadRequest,
			code:   transportcore.CodeUnregisteredType,
		},
		{
			name:   "an ineligible actor is forbidden",
			err:    &hmntsk.AuthorizationError{TaskID: "t", Actor: "mallory"},
			status: transportcore.StatusForbidden, code: transportcore.CodeForbidden,
		},
		{
			name: "a directory that cannot answer is a server error, never a 403",
			err: &hmntsk.GroupResolutionError{
				Actor: "alice", Cause: errors.New("directory unreachable"),
			},
			status: transportcore.StatusInternalServerError, code: transportcore.CodeInternal,
		},
		{
			name:   "anything unanticipated is a server error",
			err:    errors.New("something else entirely"),
			status: transportcore.StatusInternalServerError, code: transportcore.CodeInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.status, transportcore.StatusFor(tc.err))
			assert.Equal(t, tc.code, transportcore.CodeFor(tc.err))
		})
	}
}

// TestNarrowerErrorsWinOverTheirParents guards the ordering of the mapping.
//
// An unregistered type matches the validation sentinel as well as its own, and
// an illegal transition matches the conflict sentinel as well as its own, so a
// mapping written in the wrong order answers with the parent and loses the
// detail a client needs.
func TestNarrowerErrorsWinOverTheirParents(t *testing.T) {
	t.Parallel()

	unregistered := &hmntsk.UnregisteredTypeError{Type: "approvel"}
	require.ErrorIs(t, unregistered, hmntsk.ErrValidation, "the nesting is real")
	assert.Equal(t, transportcore.CodeUnregisteredType, transportcore.CodeFor(unregistered),
		"but the narrower classification is the one the client sees")

	transition := &hmntsk.TransitionError{TaskID: "t"}
	require.ErrorIs(t, transition, hmntsk.ErrConflict)
	assert.Equal(t, transportcore.StatusConflict, transportcore.StatusFor(transition))

	resolution := &hmntsk.GroupResolutionError{Cause: errors.New("down")}
	assert.NotEqual(t, transportcore.StatusForbidden, transportcore.StatusFor(resolution),
		"telling a user they lack a permission they may well have is worse than "+
			"telling them the server is broken, which it is")
}
