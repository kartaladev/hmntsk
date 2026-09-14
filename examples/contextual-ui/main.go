// Command contextual-ui serves a browser demo of contextual tasks: hmntsk's
// tasks inside the pages of a small purchasing application. An order placed on
// its orders page starts an invoice review; the invoice page, which every task
// links to through its type's route, is where the review and then the approval
// are done through a form rendered from the type's schemas. An inbox with
// bucket counts and a live notification badge sit alongside.
//
// It is an illustration, not a UI library. The page is a small React app in
// web/, built into dist/ and embedded here, so this runs with Go alone:
//
//	go run ./contextual-ui
//
// then open http://127.0.0.1:8080. Set HMNTSK_DEMO_ADDR to listen elsewhere.
//
// The sign-in page asks for no password and sets a cookie that the server
// trusts. That is for demonstration only and is not authentication.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := serveUntilInterrupted(); err != nil {
		fmt.Fprintln(os.Stderr, "contextual-ui:", err)
		os.Exit(1)
	}
}

// serveUntilInterrupted exists so that stop runs before main exits.
func serveUntilInterrupted() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return run(ctx, os.Stdout)
}

func run(ctx context.Context, w io.Writer) error {
	dir, err := os.MkdirTemp("", "hmntsk-contextual-ui-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	s, err := newServer(ctx, config{DataDir: dir})
	if err != nil {
		return err
	}
	defer s.Close()

	stopRelay := s.start(ctx)
	defer stopRelay()

	addr := os.Getenv("HMNTSK_DEMO_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		// Every request inherits ctx, so an interrupt also ends the open
		// notification streams, which never finish on their own, and Shutdown
		// does not wait for them.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	served := make(chan error, 1)

	go func() { served <- httpServer.ListenAndServe() }()

	fmt.Fprintf(w, "contextual tasks demo on http://%s (SQLite database in %s); Ctrl+C to stop\n", addr, dir)

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}
