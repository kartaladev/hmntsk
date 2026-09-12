// Package httptransport binds the hmntsk REST contract to net/http.
//
// It is a binder and nothing more: it turns an http.Request into a
// transportcore.Request, calls the handler the route table names, and writes
// the transportcore.Response back. The routes, the shapes, the validation and
// the status codes are all decided in transport/core, once, for every binding.
package httptransport

import (
	"context"
	"io"
	"net/http"
	"strings"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// MaxBodyBytes is how much of a request body the binder will read.
//
// A limit belongs here rather than in the contract: it is a transport concern,
// like TLS and rate limiting, and a host that needs a different one says so.
const MaxBodyBytes = 8 << 20

// ActorFunc extracts the acting actor from a request.
//
// The engine authenticates nobody. Whoever the host's middleware decided is
// calling is who the engine acts for, and this is the one place that crosses
// over.
type ActorFunc func(r *http.Request) string

// contextKey is the private type the acting actor travels under.
type contextKey struct{}

// ContextWithActor returns a context carrying the acting actor, for a host
// whose middleware has already identified the caller.
func ContextWithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, contextKey{}, actor)
}

// ActorFromContext returns the acting actor a context carries.
func ActorFromContext(ctx context.Context) string {
	actor, _ := ctx.Value(contextKey{}).(string)

	return actor
}

// config collects the options.
type config struct {
	actor    ActorFunc
	maxBytes int64
}

// Option configures the handler.
type Option func(*config)

// WithActorFunc supplies the rule for deciding who is calling. The default
// reads what [ContextWithActor] put in the request context.
func WithActorFunc(fn ActorFunc) Option {
	return func(c *config) {
		if fn != nil {
			c.actor = fn
		}
	}
}

// WithMaxBodyBytes caps how much of a request body is read.
func WithMaxBodyBytes(limit int64) Option {
	return func(c *config) {
		if limit > 0 {
			c.maxBytes = limit
		}
	}
}

// Handler serves the contract on a fresh mux.
//
// Every route the contract declares is registered and nothing else is, so a
// path this API does not serve answers in the contract's own error shape rather
// than the framework's.
func Handler(api *transportcore.API, opts ...Option) (http.Handler, error) {
	mux := http.NewServeMux()

	if err := Mount(mux, api, opts...); err != nil {
		return nil, err
	}

	return mux, nil
}

// Mount registers the contract's routes on an existing mux, so that a host can
// serve them alongside its own.
func Mount(mux *http.ServeMux, api *transportcore.API, opts ...Option) error {
	if mux == nil || api == nil {
		return errConfiguration("a mux and an API are required")
	}

	cfg := &config{
		actor:    func(r *http.Request) string { return ActorFromContext(r.Context()) },
		maxBytes: MaxBodyBytes,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	for _, route := range api.Routes() {
		mux.Handle(route.Method+" "+route.Pattern, handle(cfg, route))
	}

	// net/http's mux answers an unmatched path with plain text; the contract
	// answers in JSON, and a client should not have to tell the difference
	// between "no such route" and "no such task" by content type.
	mux.Handle("/", notFound())

	return nil
}

// handle adapts one route.
func handle(cfg *config, route transportcore.Route) http.Handler {
	params := route.Params()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.maxBytes))
		if err != nil {
			write(w, transportcore.Response{
				Status: http.StatusRequestEntityTooLarge,
				Body:   []byte(`{"error":{"code":"validation_failed","message":"the request body is too large"}}`),
			})

			return
		}

		request := transportcore.Request{
			Method: r.Method,
			Path:   r.URL.Path,
			Params: make(map[string]string, len(params)),
			Query:  r.URL.Query(),
			Body:   body,
			Actor:  cfg.actor(r),
		}

		for _, name := range params {
			request.Params[name] = r.PathValue(name)
		}

		write(w, route.Handler(r.Context(), request))
	})
}

// notFound answers a path the contract does not serve.
func notFound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		write(w, transportcore.NotFoundResponse())
	})
}

// write renders a response.
func write(w http.ResponseWriter, response transportcore.Response) {
	for name, value := range response.Headers {
		w.Header().Set(name, value)
	}

	w.Header().Set("Content-Type", transportcore.ContentTypeJSON)
	w.WriteHeader(response.Status)

	_, _ = w.Write(response.Body)
}

// errConfiguration is a wiring mistake found at construction.
type errConfiguration string

// Error implements the error interface.
func (e errConfiguration) Error() string {
	return "hmntsk: invalid configuration: " + strings.TrimSpace(string(e))
}
