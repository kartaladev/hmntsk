package demo_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/examples/internal/demo"
)

func TestActor(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		header map[string]string
		assert func(t *testing.T, actor string)
	}

	cases := []testCase{
		{
			name:   "the demo header names the actor",
			header: map[string]string{demo.ActorHeader: "alice"},
			assert: func(t *testing.T, actor string) { assert.Equal(t, "alice", actor) },
		},
		{
			name:   "no header is no actor",
			header: map[string]string{},
			assert: func(t *testing.T, actor string) { assert.Empty(t, actor) },
		},
		{
			name:   "another header is not the actor",
			header: map[string]string{"X-User": "alice"},
			assert: func(t *testing.T, actor string) { assert.Empty(t, actor) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}

			tc.assert(t, demo.Actor(r))
		})
	}
}

func TestNotifyActor(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		header string
		assert func(t *testing.T, actor string, err error)
	}

	cases := []testCase{
		{
			name:   "the demo header names the actor, with no error",
			header: "bob",
			assert: func(t *testing.T, actor string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "bob", actor)
			},
		},
		{
			name: "no header is no actor, which notify answers as forbidden",
			assert: func(t *testing.T, actor string, err error) {
				require.NoError(t, err)
				assert.Empty(t, actor)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			if tc.header != "" {
				r.Header.Set(demo.ActorHeader, tc.header)
			}

			actor, err := demo.NotifyActor(r)
			tc.assert(t, actor, err)
		})
	}
}

func TestBackground(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})

	var finished atomic.Bool

	stop := demo.Background(t.Context(), func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		finished.Store(true)

		return ctx.Err()
	})

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("run was not started")
	}

	stop()

	assert.True(t, finished.Load(), "stop returns only once run has returned")
}
