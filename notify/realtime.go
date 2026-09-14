// Change signals and the broadcaster that carries them between instances.
//
//go:generate mockgen -source=realtime.go -package=notify -destination=realtime_mock_test.go -typed

package notify

import (
	"context"
	"sync"
	"time"
)

// Change is what happened to a recipient's notifications.
type Change string

// The changes a signal reports.
const (
	// ChangeCreated reports a notification published for the recipient.
	ChangeCreated Change = "created"
	// ChangeRead reports notifications marked read.
	ChangeRead Change = "read"
	// ChangeClosed reports notifications closed.
	ChangeClosed Change = "closed"
	// ChangePruned reports ACTIVE notifications evicted by pruning.
	ChangePruned Change = "pruned"
)

// Signal says that a recipient's notifications changed, and never what they
// contain. A client that receives one re-reads its unread count or list from the
// store, which remains the source of truth.
type Signal struct {
	// Recipient is whose notifications changed.
	Recipient string `json:"recipient"`
	// Change is what happened.
	Change Change `json:"change"`
	// At is when it happened.
	At time.Time `json:"at"`
}

// Broadcaster carries signals from wherever a change is stored to every
// instance holding client connections.
//
// The default is [InProcessBroadcaster], which reaches only its own process. A
// deployment of more than one instance supplies one that crosses instances,
// such as notify/redis or notify/nats. Delivery is best effort: a signal lost
// on the way costs a client nothing it cannot recover by re-reading.
//
// notify/notifytest.RunBroadcasterSuite checks an implementation against this
// contract.
type Broadcaster interface {
	// Broadcast hands signals to every listener, on this instance and others.
	Broadcast(ctx context.Context, signals []Signal) error
	// Listen subscribes, calls ready once the subscription is confirmed, and
	// then calls deliver with every signal broadcast, until ctx is done, when it
	// returns ctx's error.
	//
	// Confirmed means that a signal broadcast from then on reaches deliver: an
	// implementation calls ready only once whatever its transport offers to
	// confirm a subscription has succeeded. It calls ready at most once, from
	// within Listen and before returning, and never when it returns an error
	// instead of subscribing. It holds nothing Broadcast needs while calling
	// ready, so a broadcast made from inside ready is delivered. [Hub] reports
	// itself running only after ready, so a Listen that never calls it leaves
	// the hub refusing every stream.
	//
	// deliver must not block: a slow deliver slows every broadcast. A nil
	// deliver or ready is an error matching [ErrConfiguration].
	Listen(ctx context.Context, deliver func(Signal), ready func()) error
}

// InProcessBroadcaster is the default [Broadcaster]: it delivers each broadcast
// to every listener in the same process, synchronously and in order. It does
// not reach other instances.
//
// Its zero value is ready to use, and it is safe for concurrent use.
type InProcessBroadcaster struct {
	mu        sync.RWMutex
	listeners map[*func(Signal)]struct{}
}

var _ Broadcaster = (*InProcessBroadcaster)(nil)

// NewInProcessBroadcaster returns a broadcaster with no listeners.
func NewInProcessBroadcaster() *InProcessBroadcaster { return &InProcessBroadcaster{} }

// Broadcast implements [Broadcaster]. It never fails.
func (b *InProcessBroadcaster) Broadcast(_ context.Context, signals []Signal) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for deliver := range b.listeners {
		for _, signal := range signals {
			(*deliver)(signal)
		}
	}

	return nil
}

// Listen implements [Broadcaster]. It is ready as soon as the listener is
// registered: every broadcast after that reaches it.
func (b *InProcessBroadcaster) Listen(ctx context.Context, deliver func(Signal), ready func()) error {
	switch {
	case deliver == nil:
		return &ConfigurationError{Detail: "a listener needs a deliver function"}
	case ready == nil:
		return &ConfigurationError{Detail: "a listener needs a ready function"}
	}

	b.mu.Lock()
	if b.listeners == nil {
		b.listeners = make(map[*func(Signal)]struct{})
	}

	b.listeners[&deliver] = struct{}{}
	b.mu.Unlock()

	ready()

	<-ctx.Done()

	b.mu.Lock()
	delete(b.listeners, &deliver)
	b.mu.Unlock()

	return ctx.Err()
}
