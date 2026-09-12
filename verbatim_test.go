package hmntsk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// assertVerbatim asserts that got is byte-for-byte want.
//
// assert.JSONEq is deliberately not used here. These assertions exist to prove
// that key order, whitespace-free framing and number literals survive a round
// trip, and a semantic JSON comparison is blind to exactly those properties:
// it would pass on a payload whose keys were re-sorted and whose 64-bit integer
// had been rounded through a float64.
func assertVerbatim(t *testing.T, want string, got []byte, msgAndArgs ...any) {
	t.Helper()

	//nolint:testifylint // byte-exact comparison is the property under test.
	assert.Equal(t, want, string(got), msgAndArgs...)
}
