package notify_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/kartaladev/hmntsk/notify"
)

// Mounting the notification contract on a standard library mux, behind the
// host's own authentication. The host's middleware decides who is acting; here
// a header stands in for it.
//
// On Gin, mount the same handler with r.Any("/v1/notifications/*path",
// gin.WrapH(handler)). On Fiber, which is not built on the standard library's
// HTTP types, mount it through its adaptor: app.All("/v1/notifications/*",
// adaptor.HTTPHandler(handler)). See docs/notifications.md for what streams
// through Fiber's adaptor.
func ExampleNewHandler() {
	svc, err := notify.New(notify.NewMemoryStore())
	if err != nil {
		panic(err)
	}

	hub, err := notify.NewHub(svc.Broadcaster())
	if err != nil {
		panic(err)
	}

	handler, err := notify.NewHandler(svc, hub, notify.WithActor(func(r *http.Request) (string, error) {
		return r.Header.Get("X-User"), nil
	}))
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/notifications", handler)
	mux.Handle("/v1/notifications/", handler)

	server := httptest.NewServer(mux)
	defer server.Close()

	_, err = svc.Publish(context.Background(), notify.Draft{
		Recipient: "alice", SourceID: "event-1", Subject: "task-1", Kind: "offer", Title: "A task is available",
	})
	if err != nil {
		panic(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/v1/notifications/count", http.NoBody)
	if err != nil {
		panic(err)
	}

	req.Header.Set("X-User", "alice")

	resp, err := server.Client().Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	fmt.Println(resp.StatusCode, string(body))
	// Output: 200 {"count":1}
}
