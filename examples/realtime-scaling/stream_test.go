package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenStream answers the stream request with local servers, so it needs no
// broker. It runs in parallel, which in Go means after TestRun and its leak
// check have finished.
func TestOpenStream(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		handler http.HandlerFunc
		assert  func(t *testing.T, s *stream, err error)
	}

	cases := []testCase{
		{
			name: "an open stream returns once the server has subscribed it",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, ": connected\n")
				// A failed flush leaves the client waiting for its first line,
				// which the case then reports.
				_ = http.NewResponseController(w).Flush()
				<-r.Context().Done()
			},
			assert: func(t *testing.T, s *stream, err error) {
				require.NoError(t, err)
				require.NotNil(t, s)
			},
		},
		{
			name: "a refused stream names its status, with no read error to wrap",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "forbidden", http.StatusForbidden)
			},
			assert: func(t *testing.T, _ *stream, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "status 403")
				assert.NotContains(t, err.Error(), "%!w", "a message with nothing to wrap")
				assert.NoError(t, errors.Unwrap(err))
			},
		},
		{
			name: "an unexpected first line is named",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "hello\n")
			},
			assert: func(t *testing.T, _ *stream, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), `"hello\n"`)
				assert.NotContains(t, err.Error(), "%!w", "a message with nothing to wrap")
			},
		},
		{
			name: "a stream that ends before its first line wraps the read error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, ": conn")
			},
			assert: func(t *testing.T, _ *stream, err error) {
				require.ErrorIs(t, err, io.EOF)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tc.handler)
			t.Cleanup(server.Close)

			s, err := openStream(t.Context(), server.URL, "bob")
			if s != nil {
				t.Cleanup(s.close)
			}

			tc.assert(t, s, err)
		})
	}
}
