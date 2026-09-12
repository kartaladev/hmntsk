// Package transportcore owns the hmntsk REST contract.
//
// The route table, the request and response shapes, validation, the mapping
// from the engine's errors to HTTP status codes and the generated OpenAPI
// document all live here, written once. The transport/http, transport/gin and
// transport/fiber modules are binders: they translate their framework's request
// into a [Request], call the handler, and write the [Response] back.
//
// The seam sits below http.Handler deliberately. Gin is built on the standard
// library's HTTP types and a handler boundary would have been nearly free for
// it, but Fiber is built on fasthttp and is not compatible with them at all.
// Routing every endpoint through a fasthttp-to-net/http conversion would impose
// exactly the overhead Fiber users chose Fiber to avoid. A DTO boundary costs
// each binder a few lines and costs none of them a conversion layer.
//
// Nothing in this package imports net/http.
package transportcore

import "context"

// Request is one HTTP request, reduced to what the contract actually needs.
//
// It names no framework's types, and no standard-library HTTP types either.
// A binder fills it in; a handler reads it.
type Request struct {
	// Method is the HTTP method, upper-case.
	Method string
	// Path is the request path as routed, for diagnostics.
	Path string
	// Params are the path parameters the route declared, by name.
	Params map[string]string
	// Query is the parsed query string.
	Query map[string][]string
	// Body is the raw request body. It is passed to the engine unparsed
	// wherever it is a task payload, so that field order and number literals
	// survive.
	Body []byte
	// Actor is who is acting, as the host's middleware established it. The
	// engine authenticates nobody and this is the only way it learns who is
	// calling.
	Actor string
}

// Param returns a path parameter.
func (r Request) Param(name string) string { return r.Params[name] }

// QueryValue returns the first value of a query parameter.
func (r Request) QueryValue(name string) string {
	values := r.Query[name]
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

// QueryValues returns every value of a query parameter.
func (r Request) QueryValues(name string) []string { return r.Query[name] }

// Response is one HTTP response.
type Response struct {
	// Status is the HTTP status code.
	Status int
	// Headers are response headers, beyond the content type the binder sets.
	Headers map[string]string
	// Body is the raw response body, already encoded.
	Body []byte
}

// ContentTypeJSON is what every response in this contract carries.
const ContentTypeJSON = "application/json; charset=utf-8"

// Handler answers one request. It never returns an error: every failure the
// contract knows about is a [Response] with a status code and a body, because
// that is what a client receives either way.
type Handler func(ctx context.Context, req Request) Response

// Route is one endpoint of the contract.
//
// The pattern uses {name} for path parameters, which is the standard library's
// syntax; binders for frameworks that spell it differently translate it when
// they register the route.
type Route struct {
	// Method is the HTTP method.
	Method string
	// Pattern is the path pattern, such as /v1/tasks/{id}/claim.
	Pattern string
	// OperationID names the operation in the generated OpenAPI document.
	OperationID string
	// Summary is a one-line description of what the route does.
	Summary string
	// Handler answers the request.
	Handler Handler
}

// Params returns the names of the path parameters a pattern declares, in order.
func (r Route) Params() []string {
	var (
		names []string
		open  int
	)

	for i := range len(r.Pattern) {
		switch r.Pattern[i] {
		case '{':
			open = i + 1
		case '}':
			if open > 0 {
				names = append(names, r.Pattern[open:i])
				open = 0
			}
		}
	}

	return names
}
