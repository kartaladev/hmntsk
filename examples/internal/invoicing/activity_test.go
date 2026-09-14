package invoicing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
)

func TestActivityOf(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		taskType string
		assert   func(t *testing.T, activity string)
	}

	cases := []testCase{
		{
			name:     "a review is the review activity",
			taskType: invoicing.ReviewType,
			assert:   func(t *testing.T, activity string) { assert.Equal(t, invoicing.ActivityReview, activity) },
		},
		{
			name:     "an approval is the approve activity",
			taskType: invoicing.ApproveType,
			assert:   func(t *testing.T, activity string) { assert.Equal(t, invoicing.ActivityApprove, activity) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, invoicing.ActivityOf(tc.taskType))
		})
	}
}
