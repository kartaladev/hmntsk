package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/kartaladev/hmntsk/notify"
)

// RunHub runs hub on its own goroutine and returns once its broadcaster has
// confirmed its subscription, so a signal broadcast from then on reaches the
// hub's streams. It is how a host starts a hub beside its server.
//
// It returns an error, with the hub stopped, when the run ends before the hub
// is ready, which is how a broadcaster that cannot subscribe reports, or when
// timeout passes first. stop cancels the run and returns once it has ended; it
// is always safe to call, and to call again.
func RunHub(ctx context.Context, hub *notify.Hub, timeout time.Duration) (stop func(), err error) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	var runErr error

	go func() {
		defer close(done)

		runErr = hub.Run(runCtx)
	}()

	stop = func() {
		cancel()
		<-done
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-hub.Ready():
		return stop, nil
	case <-done:
		cancel()

		return stop, fmt.Errorf("hub stopped before it was ready: %w", runErr)
	case <-timer.C:
		stop()

		return stop, fmt.Errorf("hub not ready within %s: %w", timeout, errTimeout)
	}
}
