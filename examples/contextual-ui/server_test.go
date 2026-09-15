package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/examples/internal/demo"
)

// testStart is the fixed clock's start, and testInvoiceDelay how long every
// supplier takes to invoice in a test.
var testStart = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

const testInvoiceDelay = 30 * time.Second

// testServer is a seeded server, its address and the clock it runs on.
type testServer struct {
	s     *server
	base  string
	clock *demo.Clock
}

// newTestServer seeds a server on a fixed clock and projects the seed's events
// into notifications once, so that every count below is exact.
func newTestServer(t *testing.T) testServer {
	t.Helper()

	clock := demo.NewClock(testStart)

	s, err := newServer(t.Context(), config{
		DataDir:      t.TempDir(),
		Clock:        clock,
		InvoiceDelay: func() time.Duration { return testInvoiceDelay },
	})
	require.NoError(t, err)
	t.Cleanup(s.Close)

	relayPass(t, s)

	httpServer := httptest.NewServer(s.Handler())
	t.Cleanup(httpServer.Close)

	return testServer{s: s, base: httpServer.URL, clock: clock}
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

	return do(t, base, method, path, user, "application/json", reader)
}

// uploadFile is a file for upload; a nil one sends none.
type uploadFile struct {
	name    string
	content []byte
}

// upload posts a multipart form, as the page's upload does.
func upload(t *testing.T, base, path, user string, fields map[string]string, file *uploadFile) reply {
	t.Helper()

	var body bytes.Buffer

	form := multipart.NewWriter(&body)

	for name, value := range fields {
		require.NoError(t, form.WriteField(name, value))
	}

	if file != nil {
		part, err := form.CreateFormFile("file", file.name)
		require.NoError(t, err)

		_, err = part.Write(file.content)
		require.NoError(t, err)
	}

	require.NoError(t, form.Close())

	return do(t, base, http.MethodPost, path, user, form.FormDataContentType(), &body)
}

func do(t *testing.T, base, method, path, user, contentType string, body io.Reader) reply {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, base+path, body)
	require.NoError(t, err)

	if body != http.NoBody {
		req.Header.Set("Content-Type", contentType)
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
		ID                 string     `json:"id"`
		Supplier           string     `json:"supplier"`
		SupplierRegistered bool       `json:"supplierRegistered"`
		Description        string     `json:"description"`
		Amount             int64      `json:"amount"`
		RequestedBy        string     `json:"requestedBy"`
		Status             string     `json:"status"`
		InvoiceID          string     `json:"invoiceId"`
		InvoiceExpectedAt  *time.Time `json:"invoiceExpectedAt"`
	}

	recordTaskView struct {
		ID          string    `json:"id"`
		Type        string    `json:"type"`
		Status      string    `json:"status"`
		Assignee    string    `json:"assignee"`
		ActivityKey string    `json:"activityKey"`
		CreatedAt   time.Time `json:"createdAt"`
	}

	documentView struct {
		ID          string `json:"id"`
		FileName    string `json:"fileName"`
		ContentType string `json:"contentType"`
		Size        int64  `json:"size"`
		Method      string `json:"method"`
		SentTo      string `json:"sentTo"`
		CreatedBy   string `json:"createdBy"`
	}

	invoiceView struct {
		ID       string `json:"id"`
		OrderID  string `json:"orderId"`
		Supplier string `json:"supplier"`
		Amount   int64  `json:"amount"`
	}

	orderRecordView struct {
		Order     orderView        `json:"order"`
		Invoice   *invoiceView     `json:"invoice"`
		Documents []documentView   `json:"documents"`
		Tasks     []recordTaskView `json:"tasks"`
	}

	// taskView is the part of a task the tests drive the API with.
	taskView struct {
		ID      string          `json:"id"`
		Version int64           `json:"version"`
		Status  string          `json:"status"`
		Output  json.RawMessage `json:"output"`
	}
)

// pdf is enough of a PDF for content sniffing to call it one.
var pdf = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n%%EOF\n")

func TestServerRoutes(t *testing.T) {
	t.Parallel()

	// Every case reads the same seeded server, so none of them may change it:
	// the requests that would are refused ones.
	ts := newTestServer(t)

	type testCase struct {
		name   string
		method string
		path   string
		user   string
		body   string
		// form, when set, sends the body as a multipart upload instead.
		form   map[string]string
		file   *uploadFile
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

	record := func(t *testing.T, r reply) orderRecordView {
		t.Helper()
		require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

		return decode[orderRecordView](t, r)
	}

	cases := []testCase{
		{name: "the page is served at the root", method: http.MethodGet, path: "/", assert: isPage},
		{name: "the sign-in page is a client route", method: http.MethodGet, path: "/login", assert: isPage},
		{name: "the orders page is a client route", method: http.MethodGet, path: "/orders", assert: isPage},
		{
			name: "an order page is a client route", method: http.MethodGet,
			path: "/orders/ORD-101/approve-order", assert: isPage,
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
			name: "invoice records are gone: the order record holds its invoice", method: http.MethodGet,
			path: "/demo/invoices/INV-107", user: "erin", assert: status(http.StatusNotFound),
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
				assert.Equal(t, []string{"budget-holders", "finance-managers"}, users[2].Groups)
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
			name: "the supplier registry is listed with where purchase orders go", method: http.MethodGet,
			path: "/demo/suppliers", user: "erin",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

				suppliers := decode[struct {
					Suppliers []supplier `json:"suppliers"`
				}](t, r).Suppliers
				require.Len(t, suppliers, 5)
				assert.Equal(t, supplier{Name: "Acme Paper", Contact: "orders@acme-paper.example"}, suppliers[0])
			},
		},
		{
			name: "the supplier registry needs a session", method: http.MethodGet,
			path: "/demo/suppliers", assert: status(http.StatusUnauthorized),
		},
		{
			name: "the seeded orders are listed newest first, at every stage", method: http.MethodGet,
			path: "/demo/orders", user: "erin",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

				orders := decode[struct {
					Orders []orderView `json:"orders"`
				}](t, r).Orders
				require.Len(t, orders, 8)

				statuses := make([]string, 0, len(orders))
				for _, o := range orders {
					statuses = append(statuses, o.Status)
				}

				assert.Equal(t, []string{
					"invoice-approval", "invoice-approval", "invoice-review", "awaiting-invoice",
					"awaiting-purchase-order", "awaiting-purchase-order", "pending-approval", "pending-approval",
				}, statuses)
				assert.Equal(t, "ORD-108", orders[0].ID)
				assert.Equal(t, "INV-108", orders[0].InvoiceID)
				assert.True(t, orders[0].SupplierRegistered)
				assert.False(t, orders[3+1].SupplierRegistered, "Vandelay Imports is not on the registry")
				require.NotNil(t, orders[3].InvoiceExpectedAt)
				assert.Equal(t, testStart.Add(testInvoiceDelay), *orders[3].InvoiceExpectedAt)
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
			name: "an order awaiting approval has only its approval", method: http.MethodGet,
			path: "/demo/orders/ORD-101", user: "dave",
			assert: func(t *testing.T, r reply) {
				got := record(t, r)
				assert.Equal(t, "pending-approval", got.Order.Status)
				assert.Nil(t, got.Invoice)
				assert.Empty(t, got.Documents)
				require.Len(t, got.Tasks, 1)
				// carol is the only budget holder, so the engine reserves the
				// approval for her as it creates it.
				assert.Equal(t, recordTaskView{
					ID: "approve-order-ORD-101", Type: "purchase.approve-order", Status: "RESERVED", Assignee: "carol",
					ActivityKey: "approve-order", CreatedAt: testStart,
				}, got.Tasks[0])
			},
		},
		{
			name: "an unregistered supplier's order waits for its purchase order to be uploaded", method: http.MethodGet,
			path: "/demo/orders/ORD-104", user: "dave",
			assert: func(t *testing.T, r reply) {
				got := record(t, r)
				require.Len(t, got.Tasks, 1)
				assert.Equal(t, "purchase.upload-order", got.Tasks[0].Type)
				assert.Equal(t, "purchase-order", got.Tasks[0].ActivityKey)
			},
		},
		{
			name: "an order awaiting its invoice has sent its purchase order", method: http.MethodGet,
			path: "/demo/orders/ORD-105", user: "dave",
			assert: func(t *testing.T, r reply) {
				got := record(t, r)
				assert.Nil(t, got.Invoice)
				assert.Empty(t, got.Tasks)
				require.Len(t, got.Documents, 1)
				assert.Equal(t, documentView{
					ID: "PO-105", FileName: "PO-105.txt", ContentType: "text/plain; charset=utf-8",
					Size: got.Documents[0].Size, Method: "sent", SentTo: "corporate@hooli-travel.example", CreatedBy: "erin",
				}, got.Documents[0])
				assert.Positive(t, got.Documents[0].Size)
			},
		},
		{
			name: "an order record holds its invoice and every task on it", method: http.MethodGet,
			path: "/demo/orders/ORD-107", user: "dave",
			assert: func(t *testing.T, r reply) {
				got := record(t, r)
				assert.Equal(t, "Umbrella Catering", got.Order.Supplier)
				require.NotNil(t, got.Invoice)
				assert.Equal(t, invoiceView{ID: "INV-107", OrderID: "ORD-107", Supplier: "Umbrella Catering", Amount: 640}, *got.Invoice)
				require.Len(t, got.Tasks, 1)
				assert.Equal(t, "purchase.approve-invoice", got.Tasks[0].Type)
				assert.Equal(t, "RESERVED", got.Tasks[0].Status)
				assert.Equal(t, "alice", got.Tasks[0].Assignee)
			},
		},
		{
			name: "an unknown order is not found", method: http.MethodGet,
			path: "/demo/orders/ORD-999", user: "alice", assert: status(http.StatusNotFound),
		},
		{
			name: "an order record needs a session", method: http.MethodGet,
			path: "/demo/orders/ORD-101", assert: status(http.StatusUnauthorized),
		},
		{
			name: "a purchase order document downloads as an attachment", method: http.MethodGet,
			path: "/demo/documents/PO-107", user: "dave",
			assert: func(t *testing.T, r reply) {
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
				assert.Equal(t, "text/plain; charset=utf-8", r.header.Get("Content-Type"))
				assert.Equal(t, `attachment; filename=PO-107.txt`, r.header.Get("Content-Disposition"))
				assert.Equal(t, "nosniff", r.header.Get("X-Content-Type-Options"))
				assert.Contains(t, string(r.body), "PURCHASE ORDER PO-107")
				assert.Contains(t, string(r.body), "events@umbrella-catering.example")
			},
		},
		{
			name: "an unknown document is not found", method: http.MethodGet,
			path: "/demo/documents/PO-999", user: "dave", assert: status(http.StatusNotFound),
		},
		{
			name: "a document needs a session", method: http.MethodGet,
			path: "/demo/documents/PO-107", assert: status(http.StatusUnauthorized),
		},
		{
			name: "sending a purchase order needs a session", method: http.MethodPost,
			path: "/demo/orders/ORD-103/purchase-order/send", body: `{"taskId":"purchase-order-ORD-103","version":1}`,
			assert: status(http.StatusUnauthorized),
		},
		{
			name: "sending a purchase order for an unknown order is not found", method: http.MethodPost,
			path: "/demo/orders/ORD-999/purchase-order/send", user: "erin",
			body: `{"taskId":"purchase-order-ORD-103","version":1}`, assert: status(http.StatusNotFound),
		},
		{
			name: "sending needs a JSON body naming the task", method: http.MethodPost,
			path: "/demo/orders/ORD-103/purchase-order/send", user: "erin", body: `taskId=1`,
			assert: status(http.StatusBadRequest),
		},
		{
			name: "sending with another order's task is refused", method: http.MethodPost,
			path: "/demo/orders/ORD-103/purchase-order/send", user: "erin",
			body: `{"taskId":"approve-order-ORD-101","version":1}`, assert: status(http.StatusBadRequest),
		},
		{
			name: "an unregistered supplier's purchase order cannot be sent", method: http.MethodPost,
			path: "/demo/orders/ORD-104/purchase-order/send", user: "erin",
			body: `{"taskId":"purchase-order-ORD-104","version":1}`, assert: status(http.StatusBadRequest),
		},
		{
			name: "sending for a task nobody has started is a conflict", method: http.MethodPost,
			path: "/demo/orders/ORD-103/purchase-order/send", user: "erin",
			body: `{"taskId":"purchase-order-ORD-103","version":1}`, assert: status(http.StatusConflict),
		},
		{
			name: "sending for an unknown task is not found", method: http.MethodPost,
			path: "/demo/orders/ORD-103/purchase-order/send", user: "erin",
			body: `{"taskId":"purchase-order-ORD-999","version":1}`, assert: status(http.StatusNotFound),
		},
		{
			name: "a registered supplier's purchase order cannot be uploaded", method: http.MethodPost,
			path: "/demo/orders/ORD-103/purchase-order/upload", user: "erin",
			form: map[string]string{"taskId": "purchase-order-ORD-103", "version": "1"},
			file: &uploadFile{name: "po.pdf", content: pdf}, assert: status(http.StatusBadRequest),
		},
		{
			name: "an upload needs a file", method: http.MethodPost,
			path: "/demo/orders/ORD-104/purchase-order/upload", user: "erin",
			form:   map[string]string{"taskId": "purchase-order-ORD-104", "version": "1"},
			assert: status(http.StatusBadRequest),
		},
		{
			name: "an upload needs a version", method: http.MethodPost,
			path: "/demo/orders/ORD-104/purchase-order/upload", user: "erin",
			form: map[string]string{"taskId": "purchase-order-ORD-104", "version": "one"},
			file: &uploadFile{name: "po.pdf", content: pdf}, assert: status(http.StatusBadRequest),
		},
		{
			name: "an upload must be a PDF or an image", method: http.MethodPost,
			path: "/demo/orders/ORD-104/purchase-order/upload", user: "erin",
			form:   map[string]string{"taskId": "purchase-order-ORD-104", "version": "1"},
			file:   &uploadFile{name: "po.html", content: []byte("<html><script>alert(1)</script></html>")},
			assert: status(http.StatusUnsupportedMediaType),
		},
		{
			name: "the task API acts as the cookie's user", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me&status=READY", user: "alice", assert: counts(2),
		},
		{
			name: "alice's own bucket holds the task she claimed", method: http.MethodGet,
			path: "/v1/tasks/count?assignee=me", user: "alice", assert: counts(1),
		},
		{
			name: "carol approves the orders", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me", user: "carol", assert: counts(2),
		},
		{
			name: "erin issues the purchase orders", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me", user: "erin", assert: counts(2),
		},
		{
			name: "dave takes part in no task", method: http.MethodGet,
			path: "/v1/tasks/count?candidate=me", user: "dave", assert: counts(0),
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
			method: http.MethodGet, path: "/v1/notifications/count", user: "alice", assert: counts(2),
		},
		{
			name: "bob was also told alice took one", method: http.MethodGet,
			path: "/v1/notifications/count", user: "bob", assert: counts(3),
		},
		{
			name: "erin is offered the purchase orders", method: http.MethodGet,
			path: "/v1/notifications/count", user: "erin", assert: counts(2),
		},
		{
			name: "without a cookie the notification API has no actor", method: http.MethodGet,
			path: "/v1/notifications/count", assert: status(http.StatusForbidden),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.form != nil {
				tc.assert(t, upload(t, ts.base, tc.path, tc.user, tc.form, tc.file))

				return
			}

			tc.assert(t, send(t, ts.base, tc.method, tc.path, tc.user, tc.body))
		})
	}
}

// claimAndStart claims and starts one task as user, through the task API the
// page uses, and answers the started task. A task the engine already reserved
// for user, as the only person in its pool, needs no claim.
func claimAndStart(t *testing.T, base, user, taskID string) taskView {
	t.Helper()

	path := "/v1/tasks/" + taskID

	read := request(t, base, http.MethodGet, path, user)
	require.Equal(t, http.StatusOK, read.status, "read: %s", read.body)

	held := decode[taskView](t, read)

	if held.Status == "READY" {
		claimed := send(t, base, http.MethodPost, path+"/claim", user, `{}`)
		require.Equal(t, http.StatusOK, claimed.status, "claim: %s", claimed.body)

		held = decode[taskView](t, claimed)
	}

	started := send(t, base, http.MethodPost, path+"/start", user,
		`{"version":`+strconv.FormatInt(held.Version, 10)+`}`)
	require.Equal(t, http.StatusOK, started.status, "start: %s", started.body)

	return decode[taskView](t, started)
}

// work claims, starts and completes one task as user with output.
func work(t *testing.T, base, user, taskID, output string) {
	t.Helper()

	started := claimAndStart(t, base, user, taskID)

	completed := send(t, base, http.MethodPost, "/v1/tasks/"+taskID+"/complete", user,
		`{"version":`+strconv.FormatInt(started.Version, 10)+`,"output":`+output+`}`)
	require.Equal(t, http.StatusOK, completed.status, "complete: %s", completed.body)
	require.Equal(t, "COMPLETED", decode[taskView](t, completed).Status)
}

// TestOrderWorkflow places an order and works it from approval to its
// invoice's, as erin, carol, alice and bob would from the pages. Each case is a
// sequence on a server of its own, because every step needs the state the
// previous one left.
func TestOrderWorkflow(t *testing.T) {
	t.Parallel()

	sendIt := func(t *testing.T, ts testServer, started taskView) reply {
		t.Helper()

		return send(t, ts.base, http.MethodPost, "/demo/orders/ORD-201/purchase-order/send", "erin",
			`{"taskId":"`+started.ID+`","version":`+strconv.FormatInt(started.Version, 10)+`}`)
	}

	uploadIt := func(t *testing.T, ts testServer, started taskView) reply {
		t.Helper()

		return upload(t, ts.base, "/demo/orders/ORD-201/purchase-order/upload", "erin",
			map[string]string{"taskId": started.ID, "version": strconv.FormatInt(started.Version, 10)},
			&uploadFile{name: "../signed purchase order.pdf", content: pdf})
	}

	type testCase struct {
		name     string
		supplier string
		// orderApproval is the order approval's output. The rest of the
		// workflow runs only when it approves.
		orderApproval string
		// poType is the purchase order task's type, without its prefix, and
		// purchaseOrder issues it once started.
		poType        string
		purchaseOrder func(t *testing.T, ts testServer, started taskView) reply
		// review is the invoice review's output; approval, when set, is the
		// invoice approval's.
		review   string
		approval string
		assert   func(t *testing.T, ts testServer, record orderRecordView)
	}

	cases := []testCase{
		{
			name:          "a registered supplier's order is approved, its purchase order sent, and its invoice approved",
			supplier:      "globex cloud",
			orderApproval: `{"approved":true}`,
			poType:        "send-order",
			purchaseOrder: sendIt,
			review:        `{"matchesOrder":true}`,
			approval:      `{"approved":true,"reason":"within-budget"}`,
			assert: func(t *testing.T, ts testServer, record orderRecordView) {
				assert.Equal(t, "Globex Cloud", record.Order.Supplier, "recorded under the registry's spelling")
				assert.Equal(t, "approved", record.Order.Status)
				require.Len(t, record.Documents, 1)
				assert.Equal(t, "sent", record.Documents[0].Method)
				assert.Equal(t, "procurement@globex.example", record.Documents[0].SentTo)

				types := make([]string, 0, len(record.Tasks))
				for _, task := range record.Tasks {
					types = append(types, task.Type)
				}

				assert.Equal(t, []string{
					"purchase.approve-order", "purchase.send-order", "purchase.review-invoice", "purchase.approve-invoice",
				}, types)

				po := decode[taskView](t, request(t, ts.base, http.MethodGet, "/v1/tasks/purchase-order-ORD-201", "erin"))
				assert.JSONEq(t, `{"documentId":"PO-201","sentTo":"procurement@globex.example"}`, string(po.Output))
			},
		},
		{
			name:          "an unregistered supplier's purchase order is uploaded, and its invoice rejected",
			supplier:      "Stark Industries",
			orderApproval: `{"approved":true}`,
			poType:        "upload-order",
			purchaseOrder: uploadIt,
			review:        `{"matchesOrder":true}`,
			approval:      `{"approved":false,"reason":"over-budget"}`,
			assert: func(t *testing.T, ts testServer, record orderRecordView) {
				assert.Equal(t, "rejected", record.Order.Status)
				require.Len(t, record.Documents, 1)
				assert.Equal(t, documentView{
					ID: "PO-201", FileName: "signed purchase order.pdf", ContentType: "application/pdf",
					Size: int64(len(pdf)), Method: "uploaded", CreatedBy: "erin",
				}, record.Documents[0])

				downloaded := request(t, ts.base, http.MethodGet, "/demo/documents/PO-201", "alice")
				require.Equal(t, http.StatusOK, downloaded.status)
				assert.Equal(t, pdf, downloaded.body)
				assert.Equal(t, "application/pdf", downloaded.header.Get("Content-Type"))
			},
		},
		{
			name:          "a declined order goes no further",
			supplier:      "Globex Cloud",
			orderApproval: `{"approved":false,"note":"no budget this quarter"}`,
			assert: func(t *testing.T, _ testServer, record orderRecordView) {
				assert.Equal(t, "declined", record.Order.Status)
				require.Len(t, record.Tasks, 1, "no purchase order is asked for")
				assert.Empty(t, record.Documents)
			},
		},
		{
			name:          "a disputed review ends the workflow with no invoice approval",
			supplier:      "Globex Cloud",
			orderApproval: `{"approved":true}`,
			poType:        "send-order",
			purchaseOrder: sendIt,
			review:        `{"matchesOrder":false,"note":"billed twice"}`,
			assert: func(t *testing.T, _ testServer, record orderRecordView) {
				assert.Equal(t, "disputed", record.Order.Status)
				require.Len(t, record.Tasks, 3, "no approval is created for a disputed invoice")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := newTestServer(t)

			record := func() orderRecordView {
				t.Helper()

				r := request(t, ts.base, http.MethodGet, "/demo/orders/ORD-201", "erin")
				require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)

				return decode[orderRecordView](t, r)
			}

			// waitingOn is the order's task of taskType, which the step waits on.
			waitingOn := func(t *testing.T, taskType string) recordTaskView {
				t.Helper()

				tasks := record().Tasks

				i := slices.IndexFunc(tasks, func(task recordTaskView) bool { return task.Type == taskType })
				require.GreaterOrEqual(t, i, 0, "the order has a %s task: %+v", taskType, tasks)
				require.Contains(t, []string{"READY", "RESERVED"}, tasks[i].Status, "the step waits on it")

				return tasks[i]
			}

			// step lets the relay act on what the last step committed, then lets
			// a minute pass before the next person acts, as it would.
			step := func() {
				t.Helper()

				relayPass(t, ts.s)
				ts.clock.Advance(time.Minute)
			}

			unreadBefore := count(t, request(t, ts.base, http.MethodGet, "/v1/notifications/count", "carol"))

			placed := send(t, ts.base, http.MethodPost, "/demo/orders", "erin",
				`{"supplier":"`+tc.supplier+`","description":"Laptops for the new starters","amount":4200}`)
			require.Equal(t, http.StatusCreated, placed.status, "body: %s", placed.body)
			assert.Equal(t, "pending-approval", decode[orderView](t, placed).Status)

			step()

			assert.Equal(t, unreadBefore+1, count(t, request(t, ts.base, http.MethodGet, "/v1/notifications/count", "carol")),
				"the budget holder is offered the new order")

			work(t, ts.base, "carol", waitingOn(t, "purchase.approve-order").ID, tc.orderApproval)
			step()

			if tc.purchaseOrder != nil {
				assert.Equal(t, "awaiting-purchase-order", record().Order.Status)

				started := claimAndStart(t, ts.base, "erin", waitingOn(t, "purchase."+tc.poType).ID)

				issued := tc.purchaseOrder(t, ts, started)
				require.Equal(t, http.StatusOK, issued.status, "body: %s", issued.body)
				assert.Equal(t, "COMPLETED", decode[struct {
					Task taskView `json:"task"`
				}](t, issued).Task.Status)

				relayPass(t, ts.s)

				awaiting := record().Order
				assert.Equal(t, "awaiting-invoice", awaiting.Status)
				require.NotNil(t, awaiting.InvoiceExpectedAt)
				assert.Equal(t, ts.clock.Now().Add(testInvoiceDelay), *awaiting.InvoiceExpectedAt)

				received, err := ts.s.receiveDueInvoices(t.Context())
				require.NoError(t, err)
				assert.Equal(t, 1, received, "only the seed's ORD-105, which was due long ago: ORD-201's is not due yet")
				assert.Equal(t, "awaiting-invoice", record().Order.Status)

				ts.clock.Advance(testInvoiceDelay)

				received, err = ts.s.receiveDueInvoices(t.Context())
				require.NoError(t, err)
				assert.Equal(t, 1, received, "ORD-201's invoice")

				step()

				arrived := record()
				assert.Equal(t, "invoice-review", arrived.Order.Status)
				require.NotNil(t, arrived.Invoice)
				assert.Equal(t, "INV-201", arrived.Invoice.ID)

				work(t, ts.base, "alice", waitingOn(t, "purchase.review-invoice").ID, tc.review)
				step()
			}

			if tc.approval != "" {
				assert.Equal(t, "invoice-approval", record().Order.Status)

				work(t, ts.base, "bob", waitingOn(t, "purchase.approve-invoice").ID, tc.approval)
				step()
			}

			// A further pass delivers nothing new and must change nothing.
			relayPass(t, ts.s)

			tc.assert(t, ts, record())
		})
	}
}
