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

// App returns an application serving only the contract, with an unmatched path
// answered in the contract's own error shape rather than Fiber's.
func App(api *transportcore.API, opts ...Option) (*fiber.App, error) {
	app := fiber.New()

	if err := Mount(app, api, opts...); err != nil {
		return nil, err
	}

	app.Use(NotFoundHandler())

	return app, nil
}

// NotFoundHandler answers a path the contract does not serve. A host mounting
// the routes on its own app installs it last.
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
func handle(cfg *config, route transportcore.Route) fiber.Handler {
	params := route.Params()

	return func(c fiber.Ctx) error {
		request := transportcore.Request{
			Method: route.Method,
			Path:   c.Path(),
			Params: make(map[string]string, len(params)),
			Query:  queryValues(c),
			Body:   c.Body(),
			Actor:  cfg.actor(c),
		}

		for _, name := range params {
			request.Params[name] = c.Params(name)
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
