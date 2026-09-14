package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/examples/internal/demo"
)

// newTestServer seeds a server on a fixed clock and projects the seed's events
// into notifications once, so that every count below is exact.
func newTestServer(t *testing.T) (s *server, base string) {
	t.Helper()

	s, err := newServer(t.Context(), config{
		DataDir: t.TempDir(),
		Clock:   demo.NewClock(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)),
	})
	require.NoError(t, err)
	t.Cleanup(s.Close)

	relayPass(t, s)

	httpServer := httptest.NewServer(s.Handler())
	t.Cleanup(httpServer.Close)

	return s, httpServer.URL
}

// relayPass runs the relay once, where the demo runs it every second, so a test
// knows the notifications are there.
func relayPass(t *testing.T, s *server) {
	t.Helper()

	_, err := s.relay.Relay(t.Context())
	require.NoError(t, err)
}

// reply is what a test needs of a response, read in full and closed.
type reply struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
	body    []byte
}

func request(t *testing.T, base, method, path, user string) reply {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, base+path, http.NoBody)
	require.NoError(t, err)

	if user != "" {
		req.AddCookie(&http.Cookie{Name: userCookie, Value: user})
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return reply{status: resp.StatusCode, header: resp.Header, cookies: resp.Cookies(), body: body}
}

func count(t *testing.T, body []byte) int64 {
	t.Helper()

	var c struct {
		Count int64 `json:"count"`
	}

	require.NoError(t, json.Unmarshal(body, &c), "body: %s", body)

	return c.Count
}

func TestServerRoutes(t *testing.T) {
	t.Parallel()

	_, base := newTestServer(t)

	type testCase struct {
		name   string
		method string
		path   string
		user   string
		assert func(t *testing.T, r reply)
	}

	isPage := func(t *testing.T, r reply) {
		t.Helper()

		assert.Equal(t, http.StatusOK, r.status)
		assert.Contains(t, r.header.Get("Content-Type"), "text/html")
		assert.Contains(t, string(r.body), `<div id="root">`)
	}

	status := func(want int) func(t *testing.T, r reply) {
		return func(t *testing.T, r reply) {
			t.Helper()
			assert.Equal(t, want, r.status, "body: %s", r.body)
		}
	}

	counts := func(want int64) func(t *testing.T, r reply) {
		return func(t *testing.T, r reply) {
			t.Helper()
			require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
			assert.Equal(t, want, count(t, r.body))
		}
	}

	cases := []testCase{
		{name: "the page is served at the root", method: http.MethodGet, path: "/", assert: isPage},
		{
			name: "a client route falls back to the page", method: http.MethodGet,
			path: "/invoices/INV-101/approve", assert: isPage,
		},
		{
			name: "a missing asset is not the page", method: http.MethodGet,
			path: "/assets/missing.js", assert: status(http.StatusNotFound),
		},
		{
			name: "an unknown API route is not the page", method: http.MethodGet,
			path: "/v1/nothing-here", assert: status(http.StatusNotFound),
		},
		{
			name: "an unknown demo route is not the page", method: http.MethodGet,
			path: "/demo/nothing-here", assert: status(http.StatusNotFound),
		},
		{
			name: "a demo route with the wrong method is not the page", method: http.MethodGet,
			path: "/demo/invoices", assert: status(http.StatusNotFound),
		},
		{
			name: "the demo users are listed with their groups", method: http.MethodGet, path: "/demo/users",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status)

				var users []demoUser
				require.NoError(t, json.Unmarshal(r.body, &users))

				names := make([]string, 0, len(users))
				for _, u := range users {
					names = append(names, u.ID)
				}

				assert.Equal(t, []string{"alice", "bob", "carol", "dave"}, names)
				assert.Equal(t, []string{"finance-approvers"}, users[0].Groups)
			},
		},
		{
			name: "choosing a demo user sets the cookie", method: http.MethodPost, path: "/demo/user?name=bob",
			assert: func(t *testing.T, r reply) {
				assert.Equal(t, http.StatusNoContent, r.status)
				require.Len(t, r.cookies, 1)
				assert.Equal(t, userCookie, r.cookies[0].Name)
				assert.Equal(t, "bob", r.cookies[0].Value)
			},
		},
		{
			name: "an unknown demo user is refused", method: http.MethodPost,
			path: "/demo/user?name=mallory", assert: status(http.StatusBadRequest),
		},
		{
			name: "the task API acts as the cookie's user", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me&status=READY", user: "alice", assert: counts(6),
		},
		{
			name: "alice's own bucket holds the task she claimed", method: http.MethodGet,
			path: "/v1/tasks/count?assignee=me", user: "alice", assert: counts(1),
		},
		{
			name: "without a cookie the task API has no actor", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me", assert: status(http.StatusForbidden),
		},
		{
			name: "an unknown cookie value is no actor", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me", user: "mallory", assert: status(http.StatusForbidden),
		},
		{
			name: "the task API keeps its self-only default for team queues", method: http.MethodGet,
			path: "/v1/tasks?group=finance-approvers", user: "carol", assert: status(http.StatusForbidden),
		},
		{
			name:   "the notification API acts as the cookie's user: an offer per task alice may claim",
			method: http.MethodGet, path: "/v1/notifications/count", user: "alice", assert: counts(6),
		},
		{
			name: "bob was also told alice took one", method: http.MethodGet,
			path: "/v1/notifications/count", user: "bob", assert: counts(7),
		},
		{
			name: "without a cookie the notification API has no actor", method: http.MethodGet,
			path: "/v1/notifications/count", assert: status(http.StatusForbidden),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, request(t, base, tc.method, tc.path, tc.user))
		})
	}
}

// TestSimulatedInvoiceReachesTheViewer is a sequence, not a table: it needs the
// state one request leaves for the next.
func TestSimulatedInvoiceReachesTheViewer(t *testing.T) {
	t.Parallel()

	s, base := newTestServer(t)

	before := request(t, base, http.MethodGet, "/v1/notifications/count", "alice")

	created := request(t, base, http.MethodPost, "/demo/invoices", "alice")
	require.Equal(t, http.StatusCreated, created.status, "body: %s", created.body)
	assert.True(t, strings.HasPrefix(string(created.body), `{"invoice":"INV-`), "body: %s", created.body)

	relayPass(t, s)

	after := request(t, base, http.MethodGet, "/v1/notifications/count", "alice")
	assert.Equal(t, count(t, before.body)+1, count(t, after.body))
}
