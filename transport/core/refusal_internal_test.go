package transportcore

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kartaladev/hmntsk"
)

// TestRefuseMapsPolicyErrors pins how a policy's error reaches the wire. The
// refusal type is unexported, so this is the one test that has to sit inside
// the package.
func TestRefuseMapsPolicyErrors(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		err    error
		assert func(t *testing.T, refused error)
	}

	cases := []testCase{
		{
			name: "a refusal that is also a validation error is still a 403",
			err: &hmntsk.ValidationError{Subject: "request", Issues: []hmntsk.ValidationIssue{{
				Detail: "this task is not open to you",
			}}},
			assert: func(t *testing.T, refused error) {
				assert.Equal(t, StatusForbidden, StatusFor(refused))
				assert.Equal(t, CodeForbidden, CodeFor(refused))
				assert.Contains(t, refused.Error(), "not open to you", "the policy's own reason reaches the caller")
			},
		},
		{
			name: "a plain policy error is a 403",
			err:  errors.New("closed at the weekend"),
			assert: func(t *testing.T, refused error) {
				assert.Equal(t, StatusForbidden, StatusFor(refused))
			},
		},
		{
			name: "a directory failure a policy passes on is a 500, never a 403",
			err:  &hmntsk.GroupResolutionError{Actor: "bob", Cause: errors.New("directory unreachable")},
			assert: func(t *testing.T, refused error) {
				assert.Equal(t, StatusInternalServerError, StatusFor(refused))
				assert.Equal(t, CodeInternal, CodeFor(refused))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, refuse(tc.err))
		})
	}
}
