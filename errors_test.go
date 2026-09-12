package hmntsk_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// sentinels is the full published taxonomy. Every case below is checked against
// all of them, so a new sentinel cannot be added without deciding, for every
// existing error, whether it matches.
var sentinels = map[string]error{
	"ErrConflict":          hmntsk.ErrConflict,
	"ErrIllegalTransition": hmntsk.ErrIllegalTransition,
	"ErrNotFound":          hmntsk.ErrNotFound,
	"ErrValidation":        hmntsk.ErrValidation,
	"ErrUnregisteredType":  hmntsk.ErrUnregisteredType,
	"ErrUnauthorized":      hmntsk.ErrUnauthorized,
	"ErrGroupResolution":   hmntsk.ErrGroupResolution,
	"ErrConfiguration":     hmntsk.ErrConfiguration,
}

func TestErrorTaxonomyIsDistinguishable(t *testing.T) {
	t.Parallel()

	resolverCause := errors.New("directory unreachable")

	type testCase struct {
		name string
		err  error
		// matches lists every sentinel the error must satisfy. Any sentinel not
		// listed must not match.
		matches []string
		assert  func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name:    "stale version is a conflict but not an illegal transition",
			err:     &hmntsk.ConflictError{TaskID: "t-1", Expected: 3, Current: 4},
			matches: []string{"ErrConflict"},
			assert: func(t *testing.T, err error) {
				var conflict *hmntsk.ConflictError

				require.ErrorAs(t, err, &conflict)
				assert.Equal(t, int64(4), conflict.Current,
					"the current version must be recoverable for the 409 body")
				assert.Contains(t, err.Error(), "current version 4")
			},
		},
		{
			name: "an illegal transition is also a conflict",
			err: &hmntsk.TransitionError{
				TaskID: "t-1", Operation: "Complete", From: hmntsk.StatusReady, To: hmntsk.StatusCompleted,
			},
			matches: []string{"ErrIllegalTransition", "ErrConflict"},
			assert: func(t *testing.T, err error) {
				var transition *hmntsk.TransitionError

				require.ErrorAs(t, err, &transition)
				assert.Equal(t, hmntsk.StatusReady, transition.From)
				assert.NotErrorIs(t, hmntsk.ErrConflict, hmntsk.ErrIllegalTransition,
					"the nesting must be one-way: a plain conflict is not a transition error")
			},
		},
		{
			name:    "a missing task is not found",
			err:     &hmntsk.NotFoundError{TaskID: "t-9"},
			matches: []string{"ErrNotFound"},
			assert: func(t *testing.T, err error) {
				assert.Contains(t, err.Error(), "t-9")
			},
		},
		{
			name: "a schema failure is a validation error",
			err: &hmntsk.ValidationError{
				Subject: "output",
				Issues: []hmntsk.ValidationIssue{
					{Pointer: "/approved", Detail: "is required"},
				},
			},
			matches: []string{"ErrValidation"},
			assert: func(t *testing.T, err error) {
				var validation *hmntsk.ValidationError

				require.ErrorAs(t, err, &validation)
				require.Len(t, validation.Issues, 1)
				assert.Equal(t, "/approved", validation.Issues[0].Pointer)
				assert.Contains(t, err.Error(), "/approved: is required")
			},
		},
		{
			name:    "an unknown type is also a validation error",
			err:     &hmntsk.UnregisteredTypeError{Type: "approvel"},
			matches: []string{"ErrUnregisteredType", "ErrValidation"},
			assert: func(t *testing.T, err error) {
				assert.Contains(t, err.Error(), `"approvel"`,
					"the 400 body must name the unknown type")
				assert.NotErrorIs(t, hmntsk.ErrValidation, hmntsk.ErrUnregisteredType,
					"the nesting must be one-way")
			},
		},
		{
			name: "an ineligible actor is an authorisation error",
			err: &hmntsk.AuthorizationError{
				TaskID: "t-1", Actor: "mallory", Operation: "Claim", Reason: "actor is not a candidate",
			},
			matches: []string{"ErrUnauthorized"},
			assert: func(t *testing.T, err error) {
				assert.Contains(t, err.Error(), "mallory")
			},
		},
		{
			name: "a resolver failure is a fault, never a denial",
			err: &hmntsk.GroupResolutionError{
				Actor: "alice", Groups: []string{"finance"}, Cause: resolverCause,
			},
			matches: []string{"ErrGroupResolution"},
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, resolverCause, "the resolver's own error must stay inspectable")
				assert.NotErrorIs(t, err, hmntsk.ErrUnauthorized,
					"a fault must never be reported to a user as a permission denial")
			},
		},
		{
			name:    "a wiring mistake is a configuration error",
			err:     &hmntsk.ConfigurationError{Detail: "durable sink is not transactional"},
			matches: []string{"ErrConfiguration"},
			assert: func(t *testing.T, err error) {
				assert.Contains(t, err.Error(), "durable sink is not transactional")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			expected := make(map[string]bool, len(tc.matches))
			for _, name := range tc.matches {
				require.Containsf(t, sentinels, name, "unknown sentinel %q in case", name)
				expected[name] = true
			}

			for name, sentinel := range sentinels {
				if expected[name] {
					assert.ErrorIsf(t, tc.err, sentinel, "must match %s", name)

					continue
				}

				assert.NotErrorIsf(t, tc.err, sentinel, "must not match %s", name)
			}

			tc.assert(t, tc.err)
		})
	}
}

func TestErrorTaxonomyWrappingSurvivesFmtErrorf(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		err      error
		sentinel error
	}

	cases := []testCase{
		{name: "conflict", err: &hmntsk.ConflictError{TaskID: "t"}, sentinel: hmntsk.ErrConflict},
		{name: "transition", err: &hmntsk.TransitionError{TaskID: "t"}, sentinel: hmntsk.ErrConflict},
		{name: "not found", err: &hmntsk.NotFoundError{TaskID: "t"}, sentinel: hmntsk.ErrNotFound},
		{name: "validation", err: &hmntsk.ValidationError{}, sentinel: hmntsk.ErrValidation},
		{name: "unregistered", err: &hmntsk.UnregisteredTypeError{}, sentinel: hmntsk.ErrValidation},
		{name: "authorisation", err: &hmntsk.AuthorizationError{}, sentinel: hmntsk.ErrUnauthorized},
		{name: "resolver", err: &hmntsk.GroupResolutionError{}, sentinel: hmntsk.ErrGroupResolution},
		{name: "configuration", err: &hmntsk.ConfigurationError{}, sentinel: hmntsk.ErrConfiguration},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wrapped := errors.Join(errors.New("outer"), tc.err)
			assert.ErrorIs(t, wrapped, tc.sentinel)
		})
	}
}
