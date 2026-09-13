package webhook_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// testSecret is the shared key every test in this package signs and verifies
// with.
var testSecret = []byte("a-shared-secret")

// assertVerbatim asserts that got is byte-for-byte want.
//
// assert.JSONEq is deliberately not used here, and must not be: these
// assertions exist to prove that a caller's reference parameters survive the
// sink unchanged, and a semantic JSON comparison is blind to exactly the
// properties at stake. It passes on a payload whose keys have been re-sorted
// and whose 64-bit integer has been rounded through a float64 — which is to
// say, it passes on a body that has been reformatted, when "not reformatted"
// is the requirement.
func assertVerbatim(t *testing.T, want string, got []byte, msgAndArgs ...any) {
	t.Helper()

	//nolint:testifylint // byte-exact comparison is the property under test.
	assert.Equal(t, want, string(got), msgAndArgs...)
}
