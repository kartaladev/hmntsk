package demo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/examples/internal/demo"
)

func TestRequireEnv(t *testing.T) {
	t.Parallel()

	const (
		name  = "HMNTSK_REDIS_ADDR"
		start = "docker run --rm -p 6379:6379 redis:8.2.9-alpine"
	)

	type testCase struct {
		name   string
		env    map[string]string
		assert func(t *testing.T, value string, err error)
	}

	cases := []testCase{
		{
			name: "a set variable is returned",
			env:  map[string]string{name: "127.0.0.1:6379"},
			assert: func(t *testing.T, value string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "127.0.0.1:6379", value)
			},
		},
		{
			name: "an unset variable names itself and how to start the service",
			env:  map[string]string{},
			assert: func(t *testing.T, value string, err error) {
				require.Error(t, err)
				assert.Empty(t, value)
				assert.Contains(t, err.Error(), name)
				assert.Contains(t, err.Error(), start)
			},
		},
		{
			name: "a blank variable counts as unset",
			env:  map[string]string{name: "   "},
			assert: func(t *testing.T, _ string, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), name)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			getenv := func(key string) string { return tc.env[key] }

			value, err := demo.RequireEnv(getenv, name, start)
			tc.assert(t, value, err)
		})
	}
}
