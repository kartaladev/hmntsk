// Package demo holds what every scenario prints and controls the same way:
// section headers, a clock the scenario advances, and stable names for
// identifiers that differ on every run.
//
// None of it is something a host needs. It exists so that a scenario's output
// is identical on every run and its test can compare it exactly.
package demo

import (
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Default prints the header of a scenario's default section: the library as it
// behaves with nothing configured beyond what is required.
func Default(w io.Writer, title string) {
	fmt.Fprintf(w, "\n== default: %s\n", title)
}

// Override prints the header of a scenario's override section: the same
// capability with a consumer's replacement applied.
func Override(w io.Writer, title string) {
	fmt.Fprintf(w, "\n== override: %s\n", title)
}

// Clock is a clock that moves only when a scenario advances it. It satisfies
// both hmntsk.Clock and notify.Clock, and is safe for concurrent use because a
// relay, hub or sweeper may read it from its own goroutine.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock stopped at start.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start}
}

// Now returns the clock's current instant.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// Serve serves handler on a free loopback port until stop is called, and
// returns the base URL to reach it. A real host serves on an address it
// chooses; a scenario must not collide with anything already listening.
//
// It is not httptest.NewServer: that belongs in tests, and its Close waits for
// open requests, which a notification stream never finishes on its own. stop
// closes connections instead.
func Serve(handler http.Handler) (base string, stop func(), err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen: %w", err)
	}

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = server.Serve(listener)
	}()

	stop = func() {
		_ = server.Close()

		<-done
	}

	return "http://" + listener.Addr().String(), stop, nil
}

// Names maps generated identifiers, which differ on every run, to stable names
// for printing. It is safe for concurrent use.
type Names struct {
	mu    sync.Mutex
	names map[string]string
}

// NewNames returns an empty set of names.
func NewNames() *Names {
	return &Names{names: make(map[string]string)}
}

// Name registers a stable name for an identifier. An empty identifier is
// ignored.
func (n *Names) Name(id, name string) {
	if id == "" {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	n.names[id] = name
}

// Mask replaces every registered identifier in s with its name in angle
// brackets.
func (n *Names) Mask(s string) string {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Longest identifiers first, so that one identifier containing another is
	// replaced whole.
	ids := slices.SortedFunc(maps.Keys(n.names), func(a, b string) int { return len(b) - len(a) })

	pairs := make([]string, 0, 2*len(ids))
	for _, id := range ids {
		pairs = append(pairs, id, "<"+n.names[id]+">")
	}

	return strings.NewReplacer(pairs...).Replace(s)
}
