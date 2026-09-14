package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
// knows the notifications and the workflow's follow-up tasks are there.
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

	return send(t, base, method, path, user, "")
}

// send is request with a JSON body; an empty body sends none.
func send(t *testing.T, base, method, path, user, body string) reply {
	t.Helper()

	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, base+path, reader)
	require.NoError(t, err)

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if user != "" {
		req.AddCookie(&http.Cookie{Name: userCookie, Value: user})
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return reply{status: resp.StatusCode, header: resp.Header, cookies: resp.Cookies(), body: respBody}
}

func decode[T any](t *testing.T, r reply) T {
	t.Helper()

	var v T
	require.NoError(t, json.Unmarshal(r.body, &v), "body: %s", r.body)

	return v
}

func count(t *testing.T, r reply) int64 {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

	return decode[struct {
		Count int64 `json:"count"`
	}](t, r).Count
}

// The page's shapes, as a test reads them.
type (
	orderView struct {
		ID          string `json:"id"`
		InvoiceID   string `json:"invoiceId"`
		Supplier    string `json:"supplier"`
		Description string `json:"description"`
		Amount      int64  `json:"amount"`
		RequestedBy string `json:"requestedBy"`
		Status      string `json:"status"`
	}

	recordTaskView struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Status      string `json:"status"`
		Assignee    string `json:"assignee"`
		ActivityKey string `json:"activityKey"`
	}

	invoiceRecordView struct {
		Invoice struct {
			ID       string `json:"id"`
			Supplier string `json:"supplier"`
			Amount   int64  `json:"amount"`
		} `json:"invoice"`
		Order *orderView       `json:"order"`
		Tasks []recordTaskView `json:"tasks"`
	}
)

func TestServerRoutes(t *testing.T) {
	t.Parallel()

	_, base := newTestServer(t)

	type testCase struct {
		name   string
		method string
		path   string
		user   string
		body   string
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
			assert.Equal(t, want, count(t, r))
		}
	}

	cases := []testCase{
		{name: "the page is served at the root", method: http.MethodGet, path: "/", assert: isPage},
		{name: "the sign-in page is a client route", method: http.MethodGet, path: "/login", assert: isPage},
		{name: "the orders page is a client route", method: http.MethodGet, path: "/orders", assert: isPage},
		{
			name: "an invoice page is a client route", method: http.MethodGet,
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
			name: "simulating an invoice is gone: placing an order replaced it", method: http.MethodPost,
			path: "/demo/invoices", user: "erin", assert: status(http.StatusNotFound),
		},
		{
			name: "the demo users are listed with their roles and groups", method: http.MethodGet, path: "/demo/users",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status)

				users := decode[[]demoUser](t, r)

				names := make([]string, 0, len(users))
				for _, u := range users {
					names = append(names, u.ID)
				}

				assert.Equal(t, []string{"alice", "bob", "carol", "dave", "erin"}, names)
				assert.Equal(t, []string{"finance-approvers"}, users[0].Groups)
				assert.Equal(t, []string{"purchasing"}, users[4].Groups)
				assert.NotEmpty(t, users[4].Name)
				assert.NotEmpty(t, users[4].Role)
			},
		},
		{
			name: "signing in sets an HttpOnly session cookie and answers the user", method: http.MethodPost,
			path: "/demo/session", body: `{"user":"bob"}`,
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
				require.Len(t, r.cookies, 1)
				assert.Equal(t, userCookie, r.cookies[0].Name)
				assert.Equal(t, "bob", r.cookies[0].Value)
				assert.True(t, r.cookies[0].HttpOnly, "the page asks the server who is signed in")
				assert.Equal(t, "bob", decode[demoUser](t, r).ID)
			},
		},
		{
			name: "signing in as an unknown user is refused", method: http.MethodPost,
			path: "/demo/session", body: `{"user":"mallory"}`, assert: status(http.StatusBadRequest),
		},
		{
			name: "signing in without a JSON body is refused", method: http.MethodPost,
			path: "/demo/session", body: `bob`, assert: status(http.StatusBadRequest),
		},
		{
			name: "the session answers the signed-in user", method: http.MethodGet,
			path: "/demo/session", user: "erin",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
				assert.Equal(t, "erin", decode[demoUser](t, r).ID)
			},
		},
		{
			name: "without a cookie there is no session", method: http.MethodGet,
			path: "/demo/session", assert: status(http.StatusUnauthorized),
		},
		{
			name: "an unknown cookie value is no session", method: http.MethodGet,
			path: "/demo/session", user: "mallory", assert: status(http.StatusUnauthorized),
		},
		{
			name: "signing out expires the cookie", method: http.MethodDelete, path: "/demo/session", user: "bob",
			assert: func(t *testing.T, r reply) {
				assert.Equal(t, http.StatusNoContent, r.status)
				require.Len(t, r.cookies, 1)
				assert.Equal(t, userCookie, r.cookies[0].Name)
				assert.Negative(t, r.cookies[0].MaxAge)
			},
		},
		{
			name: "the seeded orders are listed newest first", method: http.MethodGet, path: "/demo/orders", user: "erin",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

				orders := decode[struct {
					Orders []orderView `json:"orders"`
				}](t, r).Orders
				require.Len(t, orders, 7)

				assert.Equal(t, orderView{
					ID: "ORD-107", InvoiceID: "INV-107", Supplier: "Acme Paper", Description: orders[0].Description,
					Amount: 410, RequestedBy: "erin", Status: "in-review",
				}, orders[0])
				assert.NotEmpty(t, orders[0].Description)
				assert.Equal(t, "ORD-101", orders[6].ID)
				assert.Equal(t, "awaiting-approval", orders[6].Status)
			},
		},
		{
			name: "orders need a session", method: http.MethodGet,
			path: "/demo/orders", assert: status(http.StatusUnauthorized),
		},
		{
			name: "placing an order needs a session", method: http.MethodPost, path: "/demo/orders",
			body:   `{"supplier":"Stark Industries","description":"Laptops","amount":4200}`,
			assert: status(http.StatusUnauthorized),
		},
		{
			name: "only purchasing places orders", method: http.MethodPost, path: "/demo/orders", user: "alice",
			body:   `{"supplier":"Stark Industries","description":"Laptops","amount":4200}`,
			assert: status(http.StatusForbidden),
		},
		{
			name: "an order needs a supplier", method: http.MethodPost, path: "/demo/orders", user: "erin",
			body:   `{"supplier":" ","description":"Laptops","amount":4200}`,
			assert: status(http.StatusBadRequest),
		},
		{
			name: "an order needs a description", method: http.MethodPost, path: "/demo/orders", user: "erin",
			body:   `{"supplier":"Stark Industries","description":"","amount":4200}`,
			assert: status(http.StatusBadRequest),
		},
		{
			name: "an order needs a positive amount", method: http.MethodPost, path: "/demo/orders", user: "erin",
			body:   `{"supplier":"Stark Industries","description":"Laptops","amount":0}`,
			assert: status(http.StatusBadRequest),
		},
		{
			name: "an order must be JSON", method: http.MethodPost, path: "/demo/orders", user: "erin",
			body: `supplier=Stark`, assert: status(http.StatusBadRequest),
		},
		{
			name: "an invoice record holds the invoice, its order and every task on it", method: http.MethodGet,
			path: "/demo/invoices/INV-106", user: "dave",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

				record := decode[invoiceRecordView](t, r)
				assert.Equal(t, "INV-106", record.Invoice.ID)
				assert.Equal(t, "Vandelay Imports", record.Invoice.Supplier)
				assert.Equal(t, int64(9900), record.Invoice.Amount)
				require.NotNil(t, record.Order)
				assert.Equal(t, "ORD-106", record.Order.ID)
				require.Len(t, record.Tasks, 1)
				assert.Equal(t, "invoice.approve", record.Tasks[0].Type)
				assert.Equal(t, "RESERVED", record.Tasks[0].Status)
				assert.Equal(t, "alice", record.Tasks[0].Assignee)
				assert.Equal(t, "approve", record.Tasks[0].ActivityKey)
			},
		},
		{
			name: "an unknown invoice is not found", method: http.MethodGet,
			path: "/demo/invoices/INV-999", user: "alice", assert: status(http.StatusNotFound),
		},
		{
			name: "an invoice record needs a session", method: http.MethodGet,
			path: "/demo/invoices/INV-101", assert: status(http.StatusUnauthorized),
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
			name: "erin takes part in no invoice task", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me", user: "erin", assert: counts(0),
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

			tc.assert(t, send(t, base, tc.method, tc.path, tc.user, tc.body))
		})
	}
}

// taskView is the part of a task the workflow test drives the API with.
type taskView struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Status  string `json:"status"`
}

// work claims, starts and completes one task as user, through the task API the
// page uses.
func work(t *testing.T, base, user, taskID, output string) {
	t.Helper()

	path := "/v1/tasks/" + taskID

	claimed := send(t, base, http.MethodPost, path+"/claim", user, `{}`)
	require.Equal(t, http.StatusOK, claimed.status, "claim: %s", claimed.body)

	started := send(t, base, http.MethodPost, path+"/start", user,
		`{"version":`+strconv.FormatInt(decode[taskView](t, claimed).Version, 10)+`}`)
	require.Equal(t, http.StatusOK, started.status, "start: %s", started.body)

	completed := send(t, base, http.MethodPost, path+"/complete", user,
		`{"version":`+strconv.FormatInt(decode[taskView](t, started).Version, 10)+`,"output":`+output+`}`)
	require.Equal(t, http.StatusOK, completed.status, "complete: %s", completed.body)
	require.Equal(t, "COMPLETED", decode[taskView](t, completed).Status)
}

// TestOrderWorkflow places an order and works its invoice through review and
// approval, as erin, alice and bob would from the pages. Each case is a
// sequence on a server of its own, because every step needs the state the
// previous one left.
func TestOrderWorkflow(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		// review is the review task's output; approval, when set, is the
		// approval task's.
		review   string
		approval string
		assert   func(t *testing.T, record invoiceRecordView)
	}

	cases := []testCase{
		{
			name:     "a matching review leads to an approval, and approving closes the order",
			review:   `{"matchesOrder":true}`,
			approval: `{"approved":true,"reason":"within-budget"}`,
			assert: func(t *testing.T, record invoiceRecordView) {
				require.Len(t, record.Tasks, 2)
				assert.Equal(t, "COMPLETED", record.Tasks[1].Status)
				assert.Equal(t, "approved", record.Order.Status)
			},
		},
		{
			name:     "a rejected approval rejects the order",
			review:   `{"matchesOrder":true}`,
			approval: `{"approved":false,"reason":"over-budget"}`,
			assert: func(t *testing.T, record invoiceRecordView) {
				require.Len(t, record.Tasks, 2)
				assert.Equal(t, "rejected", record.Order.Status)
			},
		},
		{
			name:   "a disputed review ends the workflow with no approval",
			review: `{"matchesOrder":false,"note":"billed twice"}`,
			assert: func(t *testing.T, record invoiceRecordView) {
				require.Len(t, record.Tasks, 1, "no approval is created for a disputed invoice")
				assert.Equal(t, "disputed", record.Order.Status)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, base := newTestServer(t)

			record := func() invoiceRecordView {
				t.Helper()

				r := request(t, base, http.MethodGet, "/demo/invoices/INV-201", "erin")
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

				return decode[invoiceRecordView](t, r)
			}

			unreadBefore := count(t, request(t, base, http.MethodGet, "/v1/notifications/count", "alice"))

			placed := send(t, base, http.MethodPost, "/demo/orders", "erin",
				`{"supplier":"Stark Industries","description":"Laptops for the new starters","amount":4200}`)
			require.Equal(t, http.StatusCreated, placed.status, "body: %s", placed.body)

			order := decode[orderView](t, placed)
			assert.Equal(t, orderView{
				ID: "ORD-201", InvoiceID: "INV-201", Supplier: "Stark Industries",
				Description: "Laptops for the new starters", Amount: 4200, RequestedBy: "erin", Status: "in-review",
			}, order)

			relayPass(t, s)

			assert.Equal(t, unreadBefore+1, count(t, request(t, base, http.MethodGet, "/v1/notifications/count", "alice")),
				"every approver is offered the new review")

			placedRecord := record()
			require.Len(t, placedRecord.Tasks, 1)
			assert.Equal(t, "invoice.review", placedRecord.Tasks[0].Type)
			assert.Equal(t, "READY", placedRecord.Tasks[0].Status)

			work(t, base, "alice", placedRecord.Tasks[0].ID, tc.review)
			relayPass(t, s)

			if tc.approval != "" {
				reviewed := record()
				require.Len(t, reviewed.Tasks, 2, "a matching review creates the approval")
				assert.Equal(t, "invoice.approve", reviewed.Tasks[1].Type)
				assert.Equal(t, "READY", reviewed.Tasks[1].Status)
				assert.Equal(t, "awaiting-approval", reviewed.Order.Status)

				work(t, base, "bob", reviewed.Tasks[1].ID, tc.approval)
				relayPass(t, s)
			}

			// A further pass delivers nothing new and must change nothing.
			relayPass(t, s)

			tc.assert(t, record())
		})
	}
}
