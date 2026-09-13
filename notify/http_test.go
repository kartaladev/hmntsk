package notify_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// actorHeader is where these tests' host middleware puts the acting user.
const actorHeader = "X-Actor"

// headerActor is the host's way of establishing the acting user in these tests.
func headerActor(r *http.Request) (string, error) { return r.Header.Get(actorHeader), nil }

// httpEnv is a service on the memory store, a running hub on its broadcaster,
// and a handler over both.
type httpEnv struct {
	svc     *notify.Service
	hub     *notify.Hub
	handler *notify.Handler
}

// newHTTPEnv builds an environment. hubOpts configure the hub; handlerOpts are
// added after WithActor.
func newHTTPEnv(t *testing.T, run bool, hubOpts []notify.HubOption, handlerOpts ...notify.HandlerOption) *httpEnv {
	t.Helper()

	svc, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	hub, err := notify.NewHub(svc.Broadcaster(), hubOpts...)
	require.NoError(t, err)

	if run {
		runHub(t, hub)

		// The hub's listener registers asynchronously; wait until it receives.
		probe, err := hub.Subscribe("probe")
		require.NoError(t, err)
		awaitSubscribed(t, svc.Broadcaster(), probe, "probe")
		probe.Close()
	}

	handler, err := notify.NewHandler(svc, hub, append([]notify.HandlerOption{notify.WithActor(headerActor)}, handlerOpts...)...)
	require.NoError(t, err)

	return &httpEnv{svc: svc, hub: hub, handler: handler}
}

// publish publishes drafts and fails the test on an error.
func (e *httpEnv) publish(t *testing.T, drafts ...notify.Draft) []notify.Notification {
	t.Helper()

	result, err := e.svc.Publish(t.Context(), drafts...)
	require.NoError(t, err)

	return result.Created
}

// do serves one request as an actor and returns the recorded response.
func (e *httpEnv) do(t *testing.T, method, target, actor string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, body)
	if actor != "" {
		req.Header.Set(actorHeader, actor)
	}

	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)

	return rec
}

// errorBody is the error response shape.
type errorBody struct {
	Error struct {
		Code    string                   `json:"code"`
		Message string                   `json:"message"`
		Issues  []notify.ValidationIssue `json:"issues"`
	} `json:"error"`
}

// decodeError reads an error response.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()

	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "error body: %s", rec.Body.String())

	return body
}

// offer is a draft for a recipient on its own subject.
func offer(recipient, source string) notify.Draft {
	return notify.Draft{Recipient: recipient, SourceID: source, Subject: "task-" + source, Kind: "offer", SubjectVersion: 1}
}

func TestNewHandler(t *testing.T) {
	t.Parallel()

	svc, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	hub, err := notify.NewHub(svc.Broadcaster())
	require.NoError(t, err)

	type testCase struct {
		name   string
		svc    *notify.Service
		hub    *notify.Hub
		opts   []notify.HandlerOption
		assert func(t *testing.T, handler *notify.Handler, err error)
	}

	refused := func(t *testing.T, handler *notify.Handler, err error) {
		t.Helper()

		require.ErrorIs(t, err, notify.ErrConfiguration)
		assert.Nil(t, handler)
	}

	patterns := func(handler *notify.Handler) []string {
		var out []string
		for _, route := range handler.Routes() {
			out = append(out, route.Method+" "+route.Pattern)
		}

		return out
	}

	cases := []testCase{
		{name: "a handler without a way to establish the acting user is refused", svc: svc, hub: hub, assert: refused},
		{
			name: "a nil subscription policy is refused", svc: svc, hub: hub,
			opts:   []notify.HandlerOption{notify.WithActor(headerActor), notify.WithSubscriptionAuthorizer(nil)},
			assert: refused,
		},
		{name: "a nil service is refused", hub: hub, opts: []notify.HandlerOption{notify.WithActor(headerActor)}, assert: refused},
		{name: "a nil hub is refused", svc: svc, opts: []notify.HandlerOption{notify.WithActor(headerActor)}, assert: refused},
		{
			name: "by default the contract is served under /v1, in a stable order", svc: svc, hub: hub,
			opts: []notify.HandlerOption{notify.WithActor(headerActor)},
			assert: func(t *testing.T, handler *notify.Handler, err error) {
				require.NoError(t, err)
				assert.Equal(t, "/v1", notify.DefaultBasePath)
				assert.Equal(t, []string{
					"GET /v1/notifications",
					"GET /v1/notifications/count",
					"POST /v1/notifications/{id}/read",
					"POST /v1/notifications/read-all",
					"GET /v1/notifications/stream",
				}, patterns(handler))

				for _, route := range handler.Routes() {
					assert.NotNil(t, route.Handler)
				}
			},
		},
		{
			name: "a base path replaces the default, without a trailing slash", svc: svc, hub: hub,
			opts: []notify.HandlerOption{notify.WithActor(headerActor), notify.WithBasePath("/api/")},
			assert: func(t *testing.T, handler *notify.Handler, err error) {
				require.NoError(t, err)
				require.NotEmpty(t, handler.Routes())
				assert.Equal(t, "GET /api/notifications", patterns(handler)[0])
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler, err := notify.NewHandler(tc.svc, tc.hub, tc.opts...)
			tc.assert(t, handler, err)
		})
	}
}

func TestHandlerListAndCount(t *testing.T) {
	t.Parallel()

	type listBody struct {
		Notifications []notify.Notification `json:"notifications"`
		NextCursor    string                `json:"nextCursor"`
	}

	type testCase struct {
		name   string
		assert func(t *testing.T, env *httpEnv)
	}

	cases := []testCase{
		{
			name: "listing returns only the caller's notifications, newest first",
			assert: func(t *testing.T, env *httpEnv) {
				env.publish(t, offer("alice", "a1"))
				env.publish(t, offer("bob", "b1"))
				env.publish(t, offer("alice", "a2"))

				rec := env.do(t, http.MethodGet, "/v1/notifications", "alice", nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

				var body listBody
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				require.Len(t, body.Notifications, 2)

				for _, n := range body.Notifications {
					assert.Equal(t, "alice", n.Recipient)
				}
			},
		},
		{
			name: "a page and its cursor continue the listing",
			assert: func(t *testing.T, env *httpEnv) {
				for i := range 5 {
					env.publish(t, offer("alice", fmt.Sprintf("a%d", i)))
				}

				first := env.do(t, http.MethodGet, "/v1/notifications?limit=2", "alice", nil)
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())

				var page1 listBody
				require.NoError(t, json.Unmarshal(first.Body.Bytes(), &page1))
				require.Len(t, page1.Notifications, 2)
				require.NotEmpty(t, page1.NextCursor)

				second := env.do(t, http.MethodGet, "/v1/notifications?limit=2&cursor="+url.QueryEscape(page1.NextCursor), "alice", nil)
				require.Equal(t, http.StatusOK, second.Code, second.Body.String())

				var page2 listBody
				require.NoError(t, json.Unmarshal(second.Body.Bytes(), &page2))
				require.Len(t, page2.Notifications, 2)
				assert.NotEqual(t, page1.Notifications[0].ID, page2.Notifications[0].ID)
			},
		},
		{
			name: "filters by state, kind and subject apply",
			assert: func(t *testing.T, env *httpEnv) {
				created := env.publish(t, offer("alice", "a1"), notify.Draft{
					Recipient: "alice", SourceID: "a2", Subject: "task-9", Kind: "taken",
				})

				// Publish reports what it created in subject order, not draft
				// order, so the offer is found by its kind.
				var offered string

				for _, n := range created {
					if n.Kind == "offer" {
						offered = n.ID
					}
				}

				_, err := env.svc.MarkRead(t.Context(), "alice", offered)
				require.NoError(t, err)

				rec := env.do(t, http.MethodGet, "/v1/notifications?state=ACTIVE&kind=taken&subject=task-9", "alice", nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				var body listBody
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				require.Len(t, body.Notifications, 1)
				assert.Equal(t, "taken", body.Notifications[0].Kind)
			},
		},
		{
			name: "counting returns the caller's active notifications",
			assert: func(t *testing.T, env *httpEnv) {
				created := env.publish(t, offer("alice", "a1"), offer("alice", "a2"), offer("alice", "a3"), offer("alice", "a4"))
				env.publish(t, offer("bob", "b1"))
				_, err := env.svc.MarkRead(t.Context(), "alice", created[0].ID)
				require.NoError(t, err)

				rec := env.do(t, http.MethodGet, "/v1/notifications/count", "alice", nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.JSONEq(t, `{"count":3}`, rec.Body.String())
			},
		},
		{
			name: "a malformed cursor is a bad request",
			assert: func(t *testing.T, env *httpEnv) {
				rec := env.do(t, http.MethodGet, "/v1/notifications?cursor=not-a-cursor", "alice", nil)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				assert.Equal(t, "validation_failed", decodeError(t, rec).Error.Code)
			},
		},
		{
			name: "a page size above the cap, or that is not a number, is a bad request",
			assert: func(t *testing.T, env *httpEnv) {
				for _, limit := range []string{"501", "-1", "many"} {
					rec := env.do(t, http.MethodGet, "/v1/notifications?limit="+limit, "alice", nil)
					assert.Equalf(t, http.StatusBadRequest, rec.Code, "limit=%s: %s", limit, rec.Body.String())
				}
			},
		},
		{
			name: "an unknown state filter is a bad request",
			assert: func(t *testing.T, env *httpEnv) {
				rec := env.do(t, http.MethodGet, "/v1/notifications?state=UNREAD", "alice", nil)
				assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			},
		},
		{
			name: "a recipient parameter cannot list someone else's notifications",
			assert: func(t *testing.T, env *httpEnv) {
				env.publish(t, offer("bob", "b1"))

				rec := env.do(t, http.MethodGet, "/v1/notifications?recipient=bob", "alice", nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				var body listBody
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Empty(t, body.Notifications)
			},
		},
		{
			name: "no acting user is forbidden, for a listing and a count",
			assert: func(t *testing.T, env *httpEnv) {
				for _, target := range []string{"/v1/notifications", "/v1/notifications/count"} {
					rec := env.do(t, http.MethodGet, target, "", nil)
					require.Equalf(t, http.StatusForbidden, rec.Code, "%s: %s", target, rec.Body.String())
					assert.Equal(t, "forbidden", decodeError(t, rec).Error.Code)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, newHTTPEnv(t, false, nil))
		})
	}
}

func TestHandlerMarkRead(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		assert func(t *testing.T, env *httpEnv)
	}

	cases := []testCase{
		{
			name: "marking one's own notification read answers it, now read",
			assert: func(t *testing.T, env *httpEnv) {
				created := env.publish(t, offer("alice", "a1"))

				rec := env.do(t, http.MethodPost, "/v1/notifications/"+created[0].ID+"/read", "alice", nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				var body notify.Notification
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, created[0].ID, body.ID)
				assert.Equal(t, notify.StateRead, body.State)
				assert.NotNil(t, body.ReadAt)
			},
		},
		{
			name: "someone else's notification is not found, identically to one that does not exist",
			assert: func(t *testing.T, env *httpEnv) {
				created := env.publish(t, offer("alice", "a1"))

				others := env.do(t, http.MethodPost, "/v1/notifications/"+created[0].ID+"/read", "bob", nil)
				missing := env.do(t, http.MethodPost, "/v1/notifications/no-such-id/read", "bob", nil)

				require.Equal(t, http.StatusNotFound, others.Code, others.Body.String())
				require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
				assert.Equal(t, "not_found", decodeError(t, others).Error.Code)
				assert.Equal(t, missing.Body.String(), others.Body.String(), "the bodies are byte-identical")

				n, err := env.svc.Get(t.Context(), "alice", created[0].ID)
				require.NoError(t, err)
				assert.Equal(t, notify.StateActive, n.State, "alice's notification is unchanged")
			},
		},
		{
			name: "marking all read up to an instant leaves what arrived later",
			assert: func(t *testing.T, env *httpEnv) {
				early := env.publish(t, offer("alice", "a1"))
				through := early[0].CreatedAt

				time.Sleep(2 * time.Millisecond)

				later := env.publish(t, offer("alice", "a2"))

				body := strings.NewReader(`{"through":"` + through.Format(time.RFC3339Nano) + `"}`)
				rec := env.do(t, http.MethodPost, "/v1/notifications/read-all", "alice", body)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.JSONEq(t, `{"marked":1}`, rec.Body.String())

				n, err := env.svc.Get(t.Context(), "alice", later[0].ID)
				require.NoError(t, err)
				assert.Equal(t, notify.StateActive, n.State)
			},
		},
		{
			name: "marking all read with no instant marks everything so far",
			assert: func(t *testing.T, env *httpEnv) {
				env.publish(t, offer("alice", "a1"), offer("alice", "a2"))

				rec := env.do(t, http.MethodPost, "/v1/notifications/read-all", "alice", nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.JSONEq(t, `{"marked":2}`, rec.Body.String())
			},
		},
		{
			name: "an unparseable instant or body is a bad request",
			assert: func(t *testing.T, env *httpEnv) {
				for _, body := range []string{`{"through":"yesterday"}`, `{"through":`} {
					rec := env.do(t, http.MethodPost, "/v1/notifications/read-all", "alice", strings.NewReader(body))
					assert.Equalf(t, http.StatusBadRequest, rec.Code, "%s: %s", body, rec.Body.String())
				}
			},
		},
		{
			name: "no acting user is forbidden",
			assert: func(t *testing.T, env *httpEnv) {
				created := env.publish(t, offer("alice", "a1"))

				for _, target := range []string{"/v1/notifications/" + created[0].ID + "/read", "/v1/notifications/read-all"} {
					rec := env.do(t, http.MethodPost, target, "", nil)
					assert.Equalf(t, http.StatusForbidden, rec.Code, "%s: %s", target, rec.Body.String())
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, newHTTPEnv(t, false, nil))
		})
	}
}

// stream is a client reading a server-sent event stream line by line.
type stream struct {
	resp  *http.Response
	lines chan string
}

// openStream opens the stream as an actor, with an optional query, against a
// live server.
func openStream(t *testing.T, server *httptest.Server, actor, query string) *stream {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/notifications/stream"+query, http.NoBody)
	require.NoError(t, err)

	if actor != "" {
		req.Header.Set(actorHeader, actor)
	}

	resp, err := server.Client().Do(req) //nolint:bodyclose // closed in the t.Cleanup below, once the reader has drained
	require.NoError(t, err)

	s := &stream{resp: resp, lines: make(chan string, 64)}

	go func() {
		defer close(s.lines)

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			s.lines <- scanner.Text()
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()

		for range s.lines { //nolint:revive // drain until the reader exits
		}
	})

	return s
}

// expect waits for a line satisfying match, failing the test after a timeout.
func (s *stream) expect(t *testing.T, what string, match func(line string) bool) string {
	t.Helper()

	deadline := time.After(3 * time.Second)

	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				t.Fatalf("the stream ended before %s", what)
			}

			if match(line) {
				return line
			}
		case <-deadline:
			t.Fatalf("no %s arrived", what)
		}
	}
}

// equals matches a line exactly.
func equals(want string) func(string) bool {
	return func(line string) bool { return line == want }
}

// stalledWriter is a response writer whose client never accepts a write: each
// write blocks until the write deadline passes, then fails.
type stalledWriter struct {
	mu        sync.Mutex
	header    http.Header
	deadlines int
	deadline  time.Time
}

// Header implements http.ResponseWriter.
func (w *stalledWriter) Header() http.Header { return w.header }

// WriteHeader implements http.ResponseWriter.
func (w *stalledWriter) WriteHeader(int) {}

// Write implements http.ResponseWriter.
func (w *stalledWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()

	if deadline.IsZero() {
		return len(p), nil
	}

	time.Sleep(time.Until(deadline))

	return 0, errors.New("i/o timeout")
}

// Flush implements http.Flusher.
func (w *stalledWriter) Flush() {}

// SetWriteDeadline is what http.ResponseController calls.
func (w *stalledWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.deadlines++
	w.deadline = deadline

	return nil
}

// deadlineCount reports how many write deadlines were set.
func (w *stalledWriter) deadlineCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.deadlines
}

func TestHandlerStream(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name        string
		run         bool
		hubOpts     []notify.HubOption
		handlerOpts []notify.HandlerOption
		assert      func(t *testing.T, env *httpEnv, server *httptest.Server)
	}

	cases := []testCase{
		{
			name: "a stream opens with event-stream headers and a connected comment, and carries change signals",
			run:  true,
			assert: func(t *testing.T, env *httpEnv, server *httptest.Server) {
				s := openStream(t, server, "alice", "")
				require.Equal(t, http.StatusOK, s.resp.StatusCode)
				assert.Equal(t, "text/event-stream", s.resp.Header.Get("Content-Type"))
				assert.Equal(t, "no-cache", s.resp.Header.Get("Cache-Control"))
				assert.Equal(t, "no", s.resp.Header.Get("X-Accel-Buffering"))

				s.expect(t, "the connected comment", equals(": connected"))

				env.publish(t, notify.Draft{
					Recipient: "alice", SourceID: "a1", Subject: "task-1", Kind: "offer",
					Title: "secret title", Data: json.RawMessage(`{"secret":true}`),
				})

				s.expect(t, "an unread-changed event", equals("event: unread-changed"))
				data := s.expect(t, "the event data", func(line string) bool { return strings.HasPrefix(line, "data: ") })

				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &payload))
				assert.Equal(t, "created", payload["change"])
				assert.NotEmpty(t, payload["at"])
				assert.NotContains(t, data, "secret", "a signal carries no content")
			},
		},
		{
			name:    "an idle stream receives heartbeats",
			run:     true,
			hubOpts: []notify.HubOption{notify.WithHeartbeat(20 * time.Millisecond)},
			assert: func(t *testing.T, _ *httpEnv, server *httptest.Server) {
				s := openStream(t, server, "alice", "")
				s.expect(t, "the connected comment", equals(": connected"))
				s.expect(t, "a heartbeat", equals(": heartbeat"))
				s.expect(t, "a second heartbeat", equals(": heartbeat"))
			},
		},
		{
			name: "a stream is unavailable while the hub is not receiving signals",
			run:  false,
			assert: func(t *testing.T, _ *httpEnv, server *httptest.Server) {
				s := openStream(t, server, "alice", "")
				assert.Equal(t, http.StatusServiceUnavailable, s.resp.StatusCode)
			},
		},
		{
			name:    "a stream over the recipient's cap is too many requests",
			run:     true,
			hubOpts: []notify.HubOption{notify.WithMaxStreamsPerRecipient(1)},
			assert: func(t *testing.T, _ *httpEnv, server *httptest.Server) {
				first := openStream(t, server, "alice", "")
				first.expect(t, "the connected comment", equals(": connected"))

				second := openStream(t, server, "alice", "")
				assert.Equal(t, http.StatusTooManyRequests, second.resp.StatusCode)
			},
		},
		{
			name: "following someone else under the default policy is forbidden",
			run:  true,
			assert: func(t *testing.T, _ *httpEnv, server *httptest.Server) {
				s := openStream(t, server, "alice", "?recipient=bob")
				assert.Equal(t, http.StatusForbidden, s.resp.StatusCode)
			},
		},
		{
			name: "no acting user is forbidden",
			run:  true,
			assert: func(t *testing.T, _ *httpEnv, server *httptest.Server) {
				s := openStream(t, server, "", "")
				assert.Equal(t, http.StatusForbidden, s.resp.StatusCode)
			},
		},
		{
			name: "a host policy lets a supervisor follow a report",
			run:  true,
			handlerOpts: []notify.HandlerOption{notify.WithSubscriptionAuthorizer(notify.SubscriptionAuthorizerFunc(
				func(ctx context.Context, actor, recipient string) error {
					if actor == "alice" && recipient == "bob" {
						return nil
					}

					return notify.SelfOnly.AuthorizeSubscription(ctx, actor, recipient)
				}))},
			assert: func(t *testing.T, env *httpEnv, server *httptest.Server) {
				s := openStream(t, server, "alice", "?recipient=bob")
				require.Equal(t, http.StatusOK, s.resp.StatusCode)
				s.expect(t, "the connected comment", equals(": connected"))

				env.publish(t, offer("bob", "b1"))

				s.expect(t, "bob's unread-changed event", equals("event: unread-changed"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newHTTPEnv(t, tc.run, tc.hubOpts, tc.handlerOpts...)

			server := httptest.NewServer(env.handler)
			t.Cleanup(server.Close)

			tc.assert(t, env, server)
		})
	}
}

func TestHandlerStreamClosesAStalledClient(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, true, []notify.HubOption{
		notify.WithHeartbeat(10 * time.Millisecond), notify.WithWriteTimeout(20 * time.Millisecond),
	})

	writer := &stalledWriter{header: make(http.Header)}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/notifications/stream", nil)
	req.Header.Set(actorHeader, "alice")

	served := make(chan struct{})

	go func() {
		defer close(served)
		env.handler.ServeHTTP(writer, req)
	}()

	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream did not close a client that accepts no writes")
	}

	assert.Positive(t, writer.deadlineCount(), "every write is bounded by the write timeout")
}

func TestWriteError(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		err    error
		assert func(t *testing.T, rec *httptest.ResponseRecorder)
	}

	status := func(code int, errorCode string) func(t *testing.T, rec *httptest.ResponseRecorder) {
		return func(t *testing.T, rec *httptest.ResponseRecorder) {
			t.Helper()

			assert.Equal(t, code, rec.Code)
			assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

			body := decodeError(t, rec)
			assert.Equal(t, errorCode, body.Error.Code)
			assert.NotEmpty(t, body.Error.Message)
		}
	}

	cases := []testCase{
		{
			name: "a validation error is a bad request carrying its issues",
			err:  &notify.ValidationError{Subject: "request", Issues: []notify.ValidationIssue{{Pointer: "/limit", Detail: "too large"}}},
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				status(http.StatusBadRequest, "validation_failed")(t, rec)
				assert.Equal(t, []notify.ValidationIssue{{Pointer: "/limit", Detail: "too large"}}, decodeError(t, rec).Error.Issues)
			},
		},
		{name: "unauthorized is forbidden", err: fmt.Errorf("%w: no acting user", notify.ErrUnauthorized), assert: status(http.StatusForbidden, "forbidden")},
		{name: "not found is not found", err: notify.ErrNotFound, assert: status(http.StatusNotFound, "not_found")},
		{name: "too many streams is too many requests", err: fmt.Errorf("%w: cap", notify.ErrTooManyStreams), assert: status(http.StatusTooManyRequests, "too_many_streams")},
		{name: "unavailable is service unavailable", err: fmt.Errorf("%w: hub stopped", notify.ErrUnavailable), assert: status(http.StatusServiceUnavailable, "unavailable")},
		{
			name: "anything unanticipated is an internal error without its detail",
			err:  errors.New("pq: connection refused to 10.0.0.5:5432"),
			assert: func(t *testing.T, rec *httptest.ResponseRecorder) {
				status(http.StatusInternalServerError, "internal")(t, rec)
				assert.NotContains(t, rec.Body.String(), "10.0.0.5")
				assert.NotContains(t, rec.Body.String(), "pq:")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			notify.WriteError(rec, tc.err)
			tc.assert(t, rec)
		})
	}
}
