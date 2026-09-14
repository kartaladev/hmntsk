package demo

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ActorHeader is the request header the scenarios name their actor with.
//
// It is not authentication: it stands for the middleware a real host runs,
// which establishes who the caller is before hmntsk is asked anything. The
// server side reads it with Actor; clients set it on each request.
const ActorHeader = "X-Demo-User"

// Actor is the acting user a scenario's server reads from a request, in the
// shape httptransport.WithActorFunc takes.
func Actor(r *http.Request) string {
	return r.Header.Get(ActorHeader)
}

// NotifyActor is Actor in the shape notify.WithActor and the WebSocket
// handler's WithActor take. It never fails: a request without the header has no
// actor, which notify answers as forbidden.
func NotifyActor(r *http.Request) (string, error) {
	return Actor(r), nil
}

// Background runs run on its own goroutine until stop is called, and stop
// returns only once run has returned. It is how a host keeps a relay, a hub or
// a sweeper running beside its server.
func Background(ctx context.Context, run func(context.Context) error) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = run(ctx)
	}()

	return func() {
		cancel()
		<-done
	}
}

// errTimeout reports a condition that did not hold in time.
var errTimeout = errors.New("demo: condition did not hold in time")

// WaitUntil polls condition until it holds or timeout passes. The scenarios use
// it to wait for notify.Hub.Running, which offers no channel to wait on.
func WaitUntil(condition func() bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for !condition() {
		if time.Now().After(deadline) {
			return errTimeout
		}

		time.Sleep(time.Millisecond)
	}

	return nil
}
