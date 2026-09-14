package websocket_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/websocket"
)

// userFromContext stands in for whatever a host's authentication middleware
// puts on a request.
func userFromContext(context.Context) string { return "alice" }

// Mounting the endpoint on the standard library's router. The hub must be
// running for connections to be accepted: a host runs hub.Run(ctx) in a
// goroutine for the life of the process.
//
// On Gin, mount the same handler with gin.WrapH:
//
//	router.GET("/v1/notifications/socket", gin.WrapH(handler))
//
// Fiber is not supported: its adaptor cannot hand over the hijacked connection
// a WebSocket needs, so Fiber hosts use the server-sent event stream.
func Example() {
	svc, err := notify.New(notify.NewMemoryStore())
	if err != nil {
		panic(err)
	}

	hub, err := notify.NewHub(svc.Broadcaster())
	if err != nil {
		panic(err)
	}

	handler, err := websocket.NewHandler(svc, hub,
		websocket.WithActor(func(r *http.Request) (string, error) { return userFromContext(r.Context()), nil }),
	)
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle(handler.Pattern(), handler)

	fmt.Println(handler.Pattern())
	// Output: GET /v1/notifications/socket
}
