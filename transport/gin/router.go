// Package gintransport binds the hmntsk REST contract to gin.
//
// It is a binder and nothing more: it turns a *gin.Context into a
// transportcore.Request, calls the handler the route table names, and writes
// the transportcore.Response back. Every decision about routes, shapes,
// validation and status codes was made once, in transport/core.
package gintransport

import (
	"context"
	"io"
	"strings"

	"github.com/gin-gonic/gin"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// MaxBodyBytes is how much of a request body the binder will read.
const MaxBodyBytes = 8 << 20

// ActorKey is the gin context key the binder reads the acting actor from when
// the host has not supplied a rule of its own.
const ActorKey = "hmntsk.actor"

// ActorFunc extracts the acting actor from a request.
//
// The engine authenticates nobody: whoever the host's middleware decided is
// calling is who it acts for, and this is the one place that crosses over.
type ActorFunc func(c *gin.Context) string

// contextKey is the private type the acting actor travels under when a host
// puts it on the request's context rather than on gin's.
type contextKey struct{}

// ContextWithActor returns a context carrying the acting actor.
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

// Option configures the binding.
type Option func(*config)

// WithActorFunc supplies the rule for deciding who is calling. The default
// reads gin's own context under [ActorKey], then the request context.
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

// Mount registers the contract's routes on a gin router, so that a host can
// serve them alongside its own.
func Mount(router gin.IRouter, api *transportcore.API, opts ...Option) error {
	if router == nil || api == nil {
		return errConfiguration("a router and an API are required")
	}

	cfg := &config{actor: defaultActor, maxBytes: MaxBodyBytes}

	for _, opt := range opts {
		opt(cfg)
	}

	for _, route := range api.Routes() {
		router.Handle(route.Method, ginPattern(route.Pattern), handle(cfg, route))
	}

	return nil
}

// Engine returns a router serving only the contract, with an unmatched path
// answered in the contract's own error shape rather than gin's.
func Engine(api *transportcore.API, opts ...Option) (*gin.Engine, error) {
	engine := gin.New()

	if err := Mount(engine, api, opts...); err != nil {
		return nil, err
	}

	engine.NoRoute(NotFoundHandler())
	engine.NoMethod(NotFoundHandler())

	return engine, nil
}

// NotFoundHandler answers a path the contract does not serve, in the contract's
// own error shape rather than gin's plain text. A host mounting the routes on
// its own engine installs it with NoRoute and NoMethod.
func NotFoundHandler() gin.HandlerFunc {
	return func(c *gin.Context) { write(c, transportcore.NotFoundResponse()) }
}

// defaultActor reads whatever the host's middleware left behind, on gin's
// context or on the request's.
func defaultActor(c *gin.Context) string {
	if actor, ok := c.Get(ActorKey); ok {
		if text, ok := actor.(string); ok && text != "" {
			return text
		}
	}

	return ActorFromContext(c.Request.Context())
}

// ginPattern rewrites the contract's {name} placeholders into gin's :name.
func ginPattern(pattern string) string {
	replacer := strings.NewReplacer("{", ":", "}", "")

	return replacer.Replace(pattern)
}

// handle adapts one route.
func handle(cfg *config, route transportcore.Route) gin.HandlerFunc {
	params := route.Params()

	return func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, cfg.maxBytes))
		if err != nil {
			write(c, transportcore.Response{
				Status: transportcore.StatusBadRequest,
				Body:   []byte(`{"error":{"code":"validation_failed","message":"the request body could not be read"}}`),
			})

			return
		}

		request := transportcore.Request{
			Method: c.Request.Method,
			Path:   c.Request.URL.Path,
			Params: make(map[string]string, len(params)),
			Query:  c.Request.URL.Query(),
			Body:   body,
			Actor:  cfg.actor(c),
		}

		for _, name := range params {
			request.Params[name] = c.Param(name)
		}

		write(c, route.Handler(c.Request.Context(), request))
	}
}

// write renders a response.
func write(c *gin.Context, response transportcore.Response) {
	for name, value := range response.Headers {
		c.Header(name, value)
	}

	c.Data(response.Status, transportcore.ContentTypeJSON, response.Body)
	c.Abort()
}

// errConfiguration is a wiring mistake found at construction.
type errConfiguration string

// Error implements the error interface.
func (e errConfiguration) Error() string {
	return "hmntsk: invalid configuration: " + strings.TrimSpace(string(e))
}
