// Package fibertransport binds the hmntsk REST contract to Fiber v3.
//
// Fiber is built on fasthttp and is not compatible with the standard library's
// HTTP types. That is the reason the contract's seam sits where it does: a
// binder that had to convert every request between the two models would impose
// exactly the overhead Fiber users chose Fiber to avoid, and there is no
// conversion here at all. A fasthttp request becomes a transportcore.Request
// directly.
package fibertransport

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// ActorKey is the Fiber local the binder reads the acting actor from when the
// host has not supplied a rule of its own.
const ActorKey = "hmntsk.actor"

// ActorFunc extracts the acting actor from a request.
//
// The engine authenticates nobody: whoever the host's middleware decided is
// calling is who it acts for.
type ActorFunc func(c fiber.Ctx) string

// config collects the options.
type config struct {
	actor ActorFunc
}

// Option configures the binding.
type Option func(*config)

// WithActorFunc supplies the rule for deciding who is calling. The default
// reads the [ActorKey] local.
func WithActorFunc(fn ActorFunc) Option {
	return func(c *config) {
		if fn != nil {
			c.actor = fn
		}
	}
}

// Mount registers the contract's routes on a Fiber router, so that a host can
// serve them alongside its own.
func Mount(router fiber.Router, api *transportcore.API, opts ...Option) error {
	if router == nil || api == nil {
		return errConfiguration("a router and an API are required")
	}

	cfg := &config{actor: defaultActor}

	for _, opt := range opts {
		opt(cfg)
	}

	for _, route := range api.Routes() {
		router.Add([]string{route.Method}, fiberPattern(route.Pattern), handle(cfg, route))
	}

	return nil
}

// App returns an application serving the contract, with an unmatched path or
// method answered in the contract's own error shape rather than Fiber's.
//
// The host may add its own routes and middleware to the application afterwards,
// and they are served: unmatched requests are answered by the application's
// error handler, [NotFoundErrorHandler] over [fiber.DefaultErrorHandler], not by
// a catch-all route that would sit in front of them. A host that needs its own
// Fiber configuration or error handler builds the application itself and calls
// [Mount].
func App(api *transportcore.API, opts ...Option) (*fiber.App, error) {
	app := fiber.New(fiber.Config{ErrorHandler: NotFoundErrorHandler(fiber.DefaultErrorHandler)})

	if err := Mount(app, api, opts...); err != nil {
		return nil, err
	}

	return app, nil
}

// NotFoundErrorHandler answers the router's not-found and method-not-allowed
// errors in the contract's JSON 404, without the Allow header Fiber adds, and
// passes every other error to next. A nil next is [fiber.DefaultErrorHandler].
//
// A host building its own application installs it as the application's
// ErrorHandler, which keeps routes added after [Mount] reachable:
//
//	app := fiber.New(fiber.Config{ErrorHandler: fibertransport.NotFoundErrorHandler(mine)})
//
// It matches the errors, not where they came from: a host handler that itself
// returns [fiber.ErrNotFound] or [fiber.ErrMethodNotAllowed] is answered with
// the contract's 404 too. A host wanting its own 404 body writes that response
// instead of returning the error.
func NotFoundErrorHandler(next fiber.ErrorHandler) fiber.ErrorHandler {
	if next == nil {
		next = fiber.DefaultErrorHandler
	}

	return func(c fiber.Ctx, err error) error {
		if !errors.Is(err, fiber.ErrNotFound) && !errors.Is(err, fiber.ErrMethodNotAllowed) {
			return next(c, err)
		}

		// A wrong method is not-found in the contract, identically on every
		// binding, and none of them advertises the methods a path does serve.
		c.Response().Header.Del(fiber.HeaderAllow)

		return write(c, transportcore.NotFoundResponse())
	}
}

// NotFoundHandler answers a path the contract does not serve. A host mounting
// the routes on its own app may install it last with Use instead of
// [NotFoundErrorHandler]; routes added after it are then unreachable.
func NotFoundHandler() fiber.Handler {
	return func(c fiber.Ctx) error { return write(c, transportcore.NotFoundResponse()) }
}

// defaultActor reads whatever the host's middleware left behind.
func defaultActor(c fiber.Ctx) string {
	actor, _ := c.Locals(ActorKey).(string)

	return actor
}

// fiberPattern rewrites the contract's {name} placeholders into Fiber's :name.
func fiberPattern(pattern string) string {
	replacer := strings.NewReplacer("{", ":", "}", "")

	return replacer.Replace(pattern)
}

// handle adapts one route.
//
// Every value taken from the request is copied. Fiber's strings and byte slices
// point into buffers fasthttp reuses for the next request, and the engine keeps
// what it is given, such as a task's creator and payload, long after this
// handler returns. Passing them on uncopied would let the next caller overwrite
// what an earlier one stored.
func handle(cfg *config, route transportcore.Route) fiber.Handler {
	params := route.Params()

	return func(c fiber.Ctx) error {
		request := transportcore.Request{
			Method: route.Method,
			Path:   strings.Clone(c.Path()),
			Params: make(map[string]string, len(params)),
			Query:  queryValues(c),
			Body:   append([]byte(nil), c.Body()...),
			Actor:  strings.Clone(cfg.actor(c)),
		}

		for _, name := range params {
			request.Params[name] = strings.Clone(c.Params(name))
		}

		return write(c, route.Handler(c.Context(), request))
	}
}

// queryValues reads the query string, keeping repeated parameters.
//
// Fiber's own Queries() collapses a repeated parameter to one value, and the
// contract has two that may be repeated — status and type — so the underlying
// fasthttp arguments are read instead.
func queryValues(c fiber.Ctx) map[string][]string {
	values := make(map[string][]string)

	for key, value := range c.Request().URI().QueryArgs().All() {
		name := string(key)
		values[name] = append(values[name], string(value))
	}

	return values
}

// write renders a response.
func write(c fiber.Ctx, response transportcore.Response) error {
	for name, value := range response.Headers {
		c.Set(name, value)
	}

	c.Set(fiber.HeaderContentType, transportcore.ContentTypeJSON)

	return c.Status(response.Status).Send(response.Body)
}

// errConfiguration is a wiring mistake found at construction.
type errConfiguration string

// Error implements the error interface.
func (e errConfiguration) Error() string {
	return "hmntsk: invalid configuration: " + strings.TrimSpace(string(e))
}
