package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	cws "github.com/coder/websocket"

	"github.com/kartaladev/hmntsk/notify"
)

// DefaultReadLimit is the largest client message, in bytes, a connection
// accepts unless [WithReadLimit] replaces it. Every request a client sends is a
// short JSON object, so anything larger is refused by closing the connection.
const DefaultReadLimit int64 = 4096

// Subprotocol is the WebSocket subprotocol the handler offers. A client may
// request it; one that does not is still accepted.
const Subprotocol = "notify.v1"

// Handler serves a recipient's change signals over a WebSocket connection, and
// accepts requests to mark notifications read over it.
//
// It is a standard library [http.Handler] the host mounts on its own router,
// behind its own middleware. It authenticates nobody: the acting user comes from
// the function given to [WithActor].
type Handler struct {
	svc          *notify.Service
	hub          *notify.Hub
	actor        func(*http.Request) (string, error)
	authorizer   notify.SubscriptionAuthorizer
	origins      []string
	anyOrigin    bool
	readLimit    int64
	pingInterval time.Duration
	writeTimeout time.Duration
	pattern      string
}

// Option configures a [Handler].
type Option func(*config)

// config is what the options set. The durations and the read limit are pointers
// because an explicit zero is a wiring mistake to report, while an absent value
// is the default.
type config struct {
	actor         func(*http.Request) (string, error)
	basePath      string
	authorizer    notify.SubscriptionAuthorizer
	authorizerSet bool
	origins       []string
	anyOrigin     bool
	readLimit     *int64
	pingInterval  *time.Duration
	writeTimeout  *time.Duration
}

// WithActor supplies how the acting user is established from a request, such as
// reading what the host's authentication middleware put on its context. It is
// required. An empty actor is refused as forbidden; an error is answered through
// [notify.WriteError].
func WithActor(actor func(*http.Request) (string, error)) Option {
	return func(c *config) { c.actor = actor }
}

// WithBasePath serves the route somewhere other than
// [notify.DefaultBasePath]. A trailing slash is ignored, and an empty path keeps
// the default.
func WithBasePath(basePath string) Option {
	return func(c *config) {
		if trimmed := strings.TrimRight(basePath, "/"); trimmed != "" {
			c.basePath = trimmed
		}
	}
}

// WithSubscriptionAuthorizer replaces the default subscription policy,
// [notify.SelfOnly], with the host's own. A nil policy is a
// [notify.ConfigurationError]; pass [notify.AllowAll] to permit every
// subscription.
func WithSubscriptionAuthorizer(authorizer notify.SubscriptionAuthorizer) Option {
	return func(c *config) {
		c.authorizer = authorizer
		c.authorizerSet = true
	}
}

// WithOriginPatterns permits browser origins beyond the request's own host,
// which is the only one permitted by default. A pattern is matched against the
// origin's host, and may use the wildcards of [path.Match], such as
// "*.example.com".
func WithOriginPatterns(patterns ...string) Option {
	return func(c *config) { c.origins = append(c.origins, patterns...) }
}

// WithAnyOrigin permits every browser origin. It is the explicit opt-out from
// origin checking, for a host that checks origins somewhere else, and it is
// named so that accepting cross-site connections is never an accident.
func WithAnyOrigin() Option {
	return func(c *config) { c.anyOrigin = true }
}

// WithReadLimit replaces [DefaultReadLimit]. It must be positive.
func WithReadLimit(bytes int64) Option {
	return func(c *config) { c.readLimit = &bytes }
}

// WithPingInterval replaces the hub's heartbeat, [notify.Hub.Heartbeat], as how
// often an idle connection is pinged. It must be positive.
func WithPingInterval(interval time.Duration) Option {
	return func(c *config) { c.pingInterval = &interval }
}

// WithWriteTimeout replaces the hub's write timeout, [notify.Hub.WriteTimeout],
// as how long a write or a ping may wait on the client before the connection is
// closed. It must be positive.
func WithWriteTimeout(timeout time.Duration) Option {
	return func(c *config) { c.writeTimeout = &timeout }
}

// NewHandler builds the WebSocket endpoint over a service and the hub its
// connections subscribe through.
//
// With no options beyond the required [WithActor], it serves
// GET /v1/notifications/socket, authorizes connections with [notify.SelfOnly],
// accepts only same-host browser origins, reads messages of at most
// [DefaultReadLimit] bytes, and pings and times out writes at the hub's
// heartbeat and write timeout. A nil service or hub, a missing actor, a nil
// subscription policy, or a read limit, ping interval or write timeout that is
// not positive is a [notify.ConfigurationError].
func NewHandler(svc *notify.Service, hub *notify.Hub, opts ...Option) (*Handler, error) {
	cfg := config{basePath: notify.DefaultBasePath, authorizer: notify.SelfOnly}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	switch {
	case svc == nil:
		return nil, &notify.ConfigurationError{Detail: "a WebSocket handler needs a service"}
	case hub == nil:
		return nil, &notify.ConfigurationError{Detail: "a WebSocket handler needs a hub"}
	case cfg.actor == nil:
		return nil, &notify.ConfigurationError{Detail: "WithActor is required: the WebSocket handler authenticates nobody"}
	case cfg.authorizerSet && cfg.authorizer == nil:
		return nil, &notify.ConfigurationError{
			Detail: "WithSubscriptionAuthorizer was given no policy; pass notify.AllowAll to permit every subscription",
		}
	case cfg.readLimit != nil && *cfg.readLimit <= 0:
		return nil, &notify.ConfigurationError{Detail: "a WebSocket read limit must be positive"}
	case cfg.pingInterval != nil && *cfg.pingInterval <= 0:
		return nil, &notify.ConfigurationError{Detail: "a WebSocket ping interval must be positive"}
	case cfg.writeTimeout != nil && *cfg.writeTimeout <= 0:
		return nil, &notify.ConfigurationError{Detail: "a WebSocket write timeout must be positive"}
	}

	h := &Handler{
		svc:          svc,
		hub:          hub,
		actor:        cfg.actor,
		authorizer:   cfg.authorizer,
		origins:      cfg.origins,
		anyOrigin:    cfg.anyOrigin,
		readLimit:    DefaultReadLimit,
		pingInterval: hub.Heartbeat(),
		writeTimeout: hub.WriteTimeout(),
		pattern:      http.MethodGet + " " + cfg.basePath + "/notifications/socket",
	}

	if cfg.readLimit != nil {
		h.readLimit = *cfg.readLimit
	}

	if cfg.pingInterval != nil {
		h.pingInterval = *cfg.pingInterval
	}

	if cfg.writeTimeout != nil {
		h.writeTimeout = *cfg.writeTimeout
	}

	return h, nil
}

// Pattern is the route the handler serves, in the standard library's
// "METHOD /path" syntax, for mounting it on a [http.ServeMux]:
//
//	mux.Handle(h.Pattern(), h)
func (h *Handler) Pattern() string { return h.pattern }

// ServeHTTP implements [http.Handler].
//
// Every refusal is answered through [notify.WriteError] before the connection
// is upgraded, in this order: no acting user or a foreign browser origin is
// forbidden, a refused subscription is forbidden, a hub that is not receiving
// signals is unavailable, and a recipient over the hub's cap is too many
// requests. The subscription is released on every exit.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r)
	if err != nil {
		notify.WriteError(w, err)

		return
	}

	if actor == "" {
		notify.WriteError(w, fmt.Errorf("%w: no acting user is established", notify.ErrUnauthorized))

		return
	}

	if err := h.checkOrigin(r); err != nil {
		notify.WriteError(w, err)

		return
	}

	recipient := r.URL.Query().Get("recipient")
	if recipient == "" {
		recipient = actor
	}

	if err := h.authorizer.AuthorizeSubscription(r.Context(), actor, recipient); err != nil {
		notify.WriteError(w, &subscriptionRefusedError{cause: err})

		return
	}

	subscription, err := h.hub.Subscribe(recipient)
	if err != nil {
		notify.WriteError(w, err)

		return
	}
	defer subscription.Close()

	conn, err := cws.Accept(w, r, &cws.AcceptOptions{
		Subprotocols: []string{Subprotocol},
		// The origin was checked above, so that a refusal carries the notify
		// error body rather than the library's plain text.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept has already answered the client.
		return
	}
	defer func() { _ = conn.CloseNow() }()

	conn.SetReadLimit(h.readLimit)

	h.serve(r.Context(), conn, subscription, recipient)
}

// replyBuffer is how many replies a connection holds for its write loop. When
// it is full the reader waits, which slows that client alone.
const replyBuffer = 16

// signalFrame is the unread-changed message: the change and when it happened,
// and nothing else.
type signalFrame struct {
	Type   string        `json:"type"`
	Change notify.Change `json:"change"`
	At     time.Time     `json:"at"`
}

// serve runs one upgraded connection until the client goes, a write or ping
// fails, or the server's request context ends.
//
// The calling goroutine is the only writer: it writes signals, replies and
// pings. One child goroutine reads client requests and hands replies over. Both
// have stopped when serve returns.
//
// The connection's own context outlives the request's. When the server shuts
// down, the write loop still has to send the going-away close frame, and a read
// on a cancelled context would close the connection first with a status of its
// own.
func (h *Handler) serve(requestCtx context.Context, conn *cws.Conn, subscription *notify.Subscription, recipient string) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(requestCtx))

	replies := make(chan any, replyBuffer)
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)

		h.read(ctx, conn, recipient, replies)
	}()

	defer func() {
		cancel()
		_ = conn.CloseNow()
		<-readerDone
	}()

	ping := time.NewTicker(h.pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-requestCtx.Done():
			// The server is shutting down: tell the client, which reconnects
			// elsewhere and re-reads from the store.
			_ = conn.Close(cws.StatusGoingAway, "the server is shutting down")

			return
		case <-readerDone:
			// The client went away, or sent a message over the read limit and
			// was closed for it.
			return
		case <-subscription.Ready():
			signal, ok := subscription.Take()
			if !ok {
				continue
			}

			frame := signalFrame{Type: "unread-changed", Change: signal.Change, At: signal.At.UTC().Truncate(time.Microsecond)}
			if h.write(ctx, conn, frame) != nil {
				return
			}
		case reply := <-replies:
			if h.write(ctx, conn, reply) != nil {
				return
			}
		case <-ping.C:
			pingCtx, cancelPing := context.WithTimeout(ctx, h.writeTimeout)
			err := conn.Ping(pingCtx)

			cancelPing()

			if err != nil {
				return
			}
		}
	}
}

// write sends one JSON text frame, waiting at most the write timeout for the
// client to accept it.
func (h *Handler) write(ctx context.Context, conn *cws.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	writeCtx, cancel := context.WithTimeout(ctx, h.writeTimeout)
	defer cancel()

	return conn.Write(writeCtx, cws.MessageText, data)
}

// read reads client requests until the connection ends, and hands each reply to
// the write loop. Reading is also what answers the client's pings and completes
// the server's. A message over the read limit ends the connection with status
// 1009, which the library applies.
func (h *Handler) read(ctx context.Context, conn *cws.Conn, recipient string, replies chan<- any) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		select {
		case replies <- h.answer(ctx, recipient, data):
		case <-ctx.Done():
			return
		}
	}
}

// request is a client message.
type request struct {
	Type    string    `json:"type"`
	Ref     string    `json:"ref"`
	IDs     []string  `json:"ids"`
	Through time.Time `json:"through"`
}

// markedFrame answers a mark request with how many notifications it marked.
type markedFrame struct {
	Type   string `json:"type"`
	Ref    string `json:"ref"`
	Marked int64  `json:"marked"`
}

// errorFrame answers a request that failed, with the code the HTTP contract
// uses for the same failure.
type errorFrame struct {
	Type    string `json:"type"`
	Ref     string `json:"ref"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// answer applies one request to the connection's recipient and returns the
// reply. It acts on the connection's recipient only, so another recipient's
// notification is not found exactly like one that does not exist.
func (h *Handler) answer(ctx context.Context, recipient string, data []byte) any {
	var req request
	if err := json.Unmarshal(data, &req); err != nil {
		return errorReply("", &notify.ValidationError{Subject: "message", Issues: []notify.ValidationIssue{
			{Detail: "must be a JSON object with a type of mark-read or mark-all-read"},
		}})
	}

	var (
		result notify.MarkResult
		err    error
	)

	switch req.Type {
	case "mark-read":
		result, err = h.svc.MarkRead(ctx, recipient, req.IDs...)
	case "mark-all-read":
		result, err = h.svc.MarkAllRead(ctx, recipient, req.Through)
	default:
		err = &notify.ValidationError{Subject: "message", Issues: []notify.ValidationIssue{
			{Pointer: "/type", Detail: "must be mark-read or mark-all-read"},
		}}
	}

	if err != nil {
		return errorReply(req.Ref, err)
	}

	return markedFrame{Type: "marked", Ref: req.Ref, Marked: result.Marked}
}

// errorReply maps an error to a reply with the HTTP contract's codes. A
// not-found reply carries a fixed message, so that it cannot tell one missing
// identifier from another.
func errorReply(ref string, err error) errorFrame {
	frame := errorFrame{Type: "error", Ref: ref, Code: "internal", Message: "the request could not be completed"}

	switch {
	case errors.Is(err, notify.ErrValidation):
		frame.Code, frame.Message = "validation_failed", err.Error()
	case errors.Is(err, notify.ErrNotFound):
		frame.Code, frame.Message = "not_found", notify.ErrNotFound.Error()
	}

	return frame
}

// checkOrigin refuses a browser origin other than the request's own host,
// unless a listed pattern or the explicit opt-out permits it. A request with no
// Origin header is not from a browser page and is not checked.
func (h *Handler) checkOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" || h.anyOrigin {
		return nil
	}

	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: the request's origin %q is not a valid origin", notify.ErrUnauthorized, origin)
	}

	if strings.EqualFold(u.Host, r.Host) {
		return nil
	}

	for _, pattern := range h.origins {
		if matched, _ := path.Match(strings.ToLower(pattern), strings.ToLower(u.Host)); matched {
			return nil
		}
	}

	return fmt.Errorf("%w: the origin %q is not permitted", notify.ErrUnauthorized, origin)
}

// subscriptionRefusedError carries a subscription policy's refusal to the error
// mapping: it is answered as forbidden with the policy's own message, however
// the policy's error happens to be classified.
type subscriptionRefusedError struct{ cause error }

// Error implements the error interface.
func (e *subscriptionRefusedError) Error() string { return e.cause.Error() }

// Unwrap makes the error match [notify.ErrUnauthorized].
func (e *subscriptionRefusedError) Unwrap() error { return notify.ErrUnauthorized }
