package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBasePath is the prefix every route is served under unless
// [WithBasePath] replaces it.
const DefaultBasePath = "/v1"

// maxBodyBytes caps a request body. The only body this contract reads is a
// read-all instant.
const maxBodyBytes = 1 << 16

// Handler serves the notification HTTP contract: listing, counting and marking
// a user's notifications read, and a server-sent event stream of their change
// signals.
//
// It is a standard library [http.Handler] the host mounts on its own router,
// behind its own middleware. It authenticates nobody: the acting user comes
// from the function given to [WithActor]. Every endpoint acts on the acting
// user's own notifications only.
type Handler struct {
	svc        *Service
	hub        *Hub
	actor      func(*http.Request) (string, error)
	authorizer SubscriptionAuthorizer
	basePath   string
	mux        *http.ServeMux
	routes     []Route
}

// HandlerOption configures a [Handler].
type HandlerOption func(*handlerConfig)

// handlerConfig is what the options set.
type handlerConfig struct {
	actor         func(*http.Request) (string, error)
	basePath      string
	authorizer    SubscriptionAuthorizer
	authorizerSet bool
}

// WithActor supplies how the acting user is established from a request, such
// as reading what the host's authentication middleware put on its context. It
// is required. An empty actor is answered as forbidden; an error is answered
// through [WriteError].
func WithActor(actor func(*http.Request) (string, error)) HandlerOption {
	return func(c *handlerConfig) { c.actor = actor }
}

// WithBasePath serves the contract somewhere other than [DefaultBasePath]. A
// trailing slash is ignored, and an empty path keeps the default.
func WithBasePath(path string) HandlerOption {
	return func(c *handlerConfig) {
		if trimmed := strings.TrimRight(path, "/"); trimmed != "" {
			c.basePath = trimmed
		}
	}
}

// WithSubscriptionAuthorizer replaces the default stream policy, [SelfOnly],
// with the host's own, which then decides every stream alone. A nil policy is a
// [ConfigurationError]; pass [AllowAll] to permit every subscription.
func WithSubscriptionAuthorizer(authorizer SubscriptionAuthorizer) HandlerOption {
	return func(c *handlerConfig) {
		c.authorizer = authorizer
		c.authorizerSet = true
	}
}

// Route is one endpoint of the contract, for a router that registers routes one
// at a time rather than mounting the whole [Handler].
type Route struct {
	// Method is the HTTP method.
	Method string
	// Pattern is the path pattern, with {id} for the path parameter, in the
	// standard library's syntax.
	Pattern string
	// Handler serves the route. It reads {id} with [http.Request.PathValue], so
	// a router that is not the standard library's must set it.
	Handler http.Handler
}

// NewHandler builds the contract over a service and the hub its streams
// subscribe through.
//
// With no options beyond the required [WithActor] it serves under
// [DefaultBasePath] and authorizes streams with [SelfOnly]. A nil service or
// hub, a missing actor, or a nil subscription policy is a [ConfigurationError].
func NewHandler(svc *Service, hub *Hub, opts ...HandlerOption) (*Handler, error) {
	cfg := handlerConfig{basePath: DefaultBasePath, authorizer: SelfOnly}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	switch {
	case svc == nil:
		return nil, &ConfigurationError{Detail: "a handler needs a service"}
	case hub == nil:
		return nil, &ConfigurationError{Detail: "a handler needs a hub"}
	case cfg.actor == nil:
		return nil, &ConfigurationError{Detail: "WithActor is required: the handler authenticates nobody"}
	case cfg.authorizerSet && cfg.authorizer == nil:
		return nil, &ConfigurationError{
			Detail: "WithSubscriptionAuthorizer was given no policy; pass notify.AllowAll to permit every subscription",
		}
	}

	h := &Handler{
		svc: svc, hub: hub, actor: cfg.actor, authorizer: cfg.authorizer,
		basePath: cfg.basePath, mux: http.NewServeMux(),
	}

	h.routes = []Route{
		{Method: http.MethodGet, Pattern: h.path("/notifications"), Handler: h.acting(h.list)},
		{Method: http.MethodGet, Pattern: h.path("/notifications/count"), Handler: h.acting(h.count)},
		{Method: http.MethodPost, Pattern: h.path("/notifications/{id}/read"), Handler: h.acting(h.markRead)},
		{Method: http.MethodPost, Pattern: h.path("/notifications/read-all"), Handler: h.acting(h.markAllRead)},
		{Method: http.MethodGet, Pattern: h.path("/notifications/stream"), Handler: h.acting(h.stream)},
	}

	for _, route := range h.routes {
		h.mux.Handle(route.Method+" "+route.Pattern, route.Handler)
	}

	return h, nil
}

// Routes returns the contract, in a stable order.
func (h *Handler) Routes() []Route { return append([]Route(nil), h.routes...) }

// ServeHTTP implements [http.Handler].
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// path prefixes a route with the base path.
func (h *Handler) path(suffix string) string { return h.basePath + suffix }

// acting wraps an endpoint with establishing the acting user, answering
// forbidden when there is none.
func (h *Handler) acting(serve func(w http.ResponseWriter, r *http.Request, actor string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.actor(r)
		if err != nil {
			WriteError(w, err)

			return
		}

		if actor == "" {
			WriteError(w, fmt.Errorf("%w: no acting user is established", ErrUnauthorized))

			return
		}

		serve(w, r, actor)
	})
}

// listResponse is the listing's body.
type listResponse struct {
	Notifications []Notification `json:"notifications"`
	NextCursor    string         `json:"nextCursor,omitempty"`
}

// list answers GET /notifications. It lists the acting user's notifications;
// no parameter names another recipient.
func (h *Handler) list(w http.ResponseWriter, r *http.Request, actor string) {
	values := r.URL.Query()

	q := ListQuery{
		Recipient: actor,
		Kinds:     values["kind"],
		Subject:   values.Get("subject"),
		Cursor:    values.Get("cursor"),
	}

	for _, state := range values["state"] {
		q.States = append(q.States, State(state))
	}

	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			WriteError(w, &ValidationError{Subject: "request", Issues: []ValidationIssue{
				{Pointer: "/limit", Detail: "is not a number"},
			}})

			return
		}

		q.Limit = limit
	}

	page, err := h.svc.List(r.Context(), q)
	if err != nil {
		WriteError(w, err)

		return
	}

	notifications := page.Notifications
	if notifications == nil {
		notifications = []Notification{}
	}

	writeJSON(w, http.StatusOK, listResponse{Notifications: notifications, NextCursor: page.NextCursor})
}

// count answers GET /notifications/count.
func (h *Handler) count(w http.ResponseWriter, r *http.Request, actor string) {
	count, err := h.svc.CountActive(r.Context(), actor)
	if err != nil {
		WriteError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, struct {
		Count int64 `json:"count"`
	}{Count: count})
}

// markRead answers POST /notifications/{id}/read with the notification, now
// read. Another user's notification is not found, exactly like a missing one.
func (h *Handler) markRead(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")

	if _, err := h.svc.MarkRead(r.Context(), actor, id); err != nil {
		WriteError(w, err)

		return
	}

	n, err := h.svc.Get(r.Context(), actor, id)
	if err != nil {
		WriteError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, n)
}

// markAllRead answers POST /notifications/read-all. The optional body
// {"through": RFC 3339} bounds what is marked; without it, everything so far.
func (h *Handler) markAllRead(w http.ResponseWriter, r *http.Request, actor string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		WriteError(w, &ValidationError{Subject: "request", Issues: []ValidationIssue{{Detail: "the body could not be read"}}})

		return
	}

	var body struct {
		Through time.Time `json:"through"`
	}

	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			WriteError(w, &ValidationError{Subject: "request", Issues: []ValidationIssue{
				{Pointer: "/through", Detail: "must be an RFC 3339 instant in a JSON object"},
			}})

			return
		}
	}

	result, err := h.svc.MarkAllRead(r.Context(), actor, body.Through)
	if err != nil {
		WriteError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, struct {
		Marked int64 `json:"marked"`
	}{Marked: result.Marked})
}

// subscriptionRefusedError carries a subscription policy's refusal to the error
// mapping. It is answered as forbidden with the policy's own message, however
// the policy's error happens to be classified.
type subscriptionRefusedError struct{ cause error }

// Error implements the error interface.
func (e *subscriptionRefusedError) Error() string { return e.cause.Error() }

// Unwrap makes the error match [ErrUnauthorized].
func (e *subscriptionRefusedError) Unwrap() error { return ErrUnauthorized }

// stream answers GET /notifications/stream with a server-sent event stream of a
// recipient's change signals: the acting user's own by default, or the one the
// recipient parameter names, if the subscription policy permits it.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request, actor string) {
	recipient := r.URL.Query().Get("recipient")
	if recipient == "" {
		recipient = actor
	}

	if err := h.authorizer.AuthorizeSubscription(r.Context(), actor, recipient); err != nil {
		WriteError(w, &subscriptionRefusedError{cause: err})

		return
	}

	subscription, err := h.hub.Subscribe(recipient)
	if err != nil {
		WriteError(w, err)

		return
	}
	defer subscription.Close()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	controller := http.NewResponseController(w)
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()

	write := func(chunk string) error {
		if err := controller.SetWriteDeadline(time.Now().Add(h.hub.WriteTimeout())); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			return err
		}

		if _, err := io.WriteString(w, chunk); err != nil {
			return err
		}

		if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}

		return nil
	}

	if write(": connected\n\n") != nil {
		return
	}

	heartbeat := time.NewTicker(h.hub.Heartbeat())
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-subscription.Ready():
			signal, ok := subscription.Take()
			if !ok {
				continue
			}

			if write(eventFor(signal)) != nil {
				return
			}
		case <-heartbeat.C:
			if write(": heartbeat\n\n") != nil {
				return
			}
		}
	}
}

// eventFor renders a signal as an unread-changed event carrying only the
// change and when it happened.
func eventFor(signal Signal) string {
	data, _ := json.Marshal(struct {
		Change Change    `json:"change"`
		At     time.Time `json:"at"`
	}{Change: signal.Change, At: normalizeTime(signal.At)})

	return "event: unread-changed\ndata: " + string(data) + "\n\n"
}

// errorResponse is every error's body: the same shape and code vocabulary as the
// task engine's HTTP contract, written here rather than imported.
type errorResponse struct {
	Error errorDetail `json:"error"`
}

// errorDetail says what went wrong, in terms a client can act on.
type errorDetail struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Issues  []ValidationIssue `json:"issues,omitempty"`
}

// internalMessage is what an unanticipated failure tells a client, in place of
// a detail that could reveal internals.
const internalMessage = "the request could not be completed"

// WriteError answers an error with the status and body the contract maps it to:
//
//   - a validation error: 400, code validation_failed, with its issues;
//   - [ErrUnauthorized]: 403, forbidden;
//   - [ErrNotFound]: 404, not_found;
//   - [ErrTooManyStreams]: 429, too_many_streams;
//   - [ErrUnavailable]: 503, unavailable;
//   - anything else: 500, internal, with a generic message and no detail.
//
// It is exported so that other transports, such as notify/websocket, answer
// with the same mapping instead of copying it.
func WriteError(w http.ResponseWriter, err error) {
	status, detail := http.StatusInternalServerError, errorDetail{Code: "internal", Message: internalMessage}

	var validation *ValidationError

	switch {
	case errors.As(err, &validation):
		status, detail = http.StatusBadRequest, errorDetail{
			Code: "validation_failed", Message: err.Error(), Issues: validation.Issues,
		}
	case errors.Is(err, ErrValidation):
		status, detail = http.StatusBadRequest, errorDetail{Code: "validation_failed", Message: err.Error()}
	case errors.Is(err, ErrUnauthorized):
		status, detail = http.StatusForbidden, errorDetail{Code: "forbidden", Message: err.Error()}
	case errors.Is(err, ErrNotFound):
		status, detail = http.StatusNotFound, errorDetail{Code: "not_found", Message: err.Error()}
	case errors.Is(err, ErrTooManyStreams):
		status, detail = http.StatusTooManyRequests, errorDetail{Code: "too_many_streams", Message: err.Error()}
	case errors.Is(err, ErrUnavailable):
		status, detail = http.StatusServiceUnavailable, errorDetail{Code: "unavailable", Message: err.Error()}
	}

	writeJSON(w, status, errorResponse{Error: detail})
}

// writeJSON writes a JSON body with a status.
func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":{"code":"internal","message":"` + internalMessage + `"}}`)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
