package notify

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// The hub defaults.
const (
	// DefaultHeartbeat is how often an idle stream is written to, so that
	// proxies and load balancers do not close it.
	DefaultHeartbeat = 25 * time.Second
	// DefaultWriteTimeout is how long a stream waits for a client to accept a
	// write before closing the stream.
	DefaultWriteTimeout = 10 * time.Second
	// DefaultMaxStreamsPerRecipient is how many streams one recipient may hold
	// open on one instance, across every transport.
	DefaultMaxStreamsPerRecipient = 8
)

// Hub routes signals from a [Broadcaster] to the subscriptions of the recipient
// each signal is for. Every transport, SSE and WebSocket alike, subscribes
// through it, so one per-recipient cap counts all of a recipient's streams.
//
// Nothing runs on its own: signals are received only while the host runs
// [Hub.Run]. A Hub is safe for concurrent use.
type Hub struct {
	broadcaster  Broadcaster
	heartbeat    time.Duration
	writeTimeout time.Duration
	maxStreams   int

	running atomic.Bool

	mu            sync.Mutex
	subscriptions map[string]map[*Subscription]struct{}
}

// HubOption configures a [Hub].
type HubOption func(*hubConfig)

// hubConfig is what the options set. The values are pointers because an explicit
// zero is a wiring mistake to report, while an absent value is the default.
type hubConfig struct {
	heartbeat    *time.Duration
	writeTimeout *time.Duration
	maxStreams   *int
}

// WithHeartbeat replaces [DefaultHeartbeat]. It must be positive.
func WithHeartbeat(interval time.Duration) HubOption {
	return func(c *hubConfig) { c.heartbeat = &interval }
}

// WithWriteTimeout replaces [DefaultWriteTimeout]. It must be positive.
func WithWriteTimeout(timeout time.Duration) HubOption {
	return func(c *hubConfig) { c.writeTimeout = &timeout }
}

// WithMaxStreamsPerRecipient replaces [DefaultMaxStreamsPerRecipient]. It must be
// at least one.
func WithMaxStreamsPerRecipient(n int) HubOption {
	return func(c *hubConfig) { c.maxStreams = &n }
}

// NewHub builds a hub over a broadcaster, normally the service's
// [Service.Broadcaster]. A nil broadcaster, or a heartbeat, write timeout or
// stream cap that is not positive, is a [ConfigurationError].
func NewHub(broadcaster Broadcaster, opts ...HubOption) (*Hub, error) {
	if broadcaster == nil {
		return nil, &ConfigurationError{Detail: "a hub needs a broadcaster; pass the service's Broadcaster()"}
	}

	var cfg hubConfig

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	hub := &Hub{
		broadcaster:   broadcaster,
		heartbeat:     DefaultHeartbeat,
		writeTimeout:  DefaultWriteTimeout,
		maxStreams:    DefaultMaxStreamsPerRecipient,
		subscriptions: make(map[string]map[*Subscription]struct{}),
	}

	switch {
	case cfg.heartbeat != nil && *cfg.heartbeat <= 0:
		return nil, &ConfigurationError{Detail: "a hub heartbeat must be positive"}
	case cfg.writeTimeout != nil && *cfg.writeTimeout <= 0:
		return nil, &ConfigurationError{Detail: "a hub write timeout must be positive"}
	case cfg.maxStreams != nil && *cfg.maxStreams < 1:
		return nil, &ConfigurationError{Detail: "a hub must allow at least one stream per recipient"}
	}

	if cfg.heartbeat != nil {
		hub.heartbeat = *cfg.heartbeat
	}

	if cfg.writeTimeout != nil {
		hub.writeTimeout = *cfg.writeTimeout
	}

	if cfg.maxStreams != nil {
		hub.maxStreams = *cfg.maxStreams
	}

	return hub, nil
}

// Run receives signals from the broadcaster until ctx is cancelled, and returns
// ctx's error. While it runs, [Hub.Running] reports true.
//
// It blocks, and a hub runs once at a time: a second Run while one is running is
// a [ConfigurationError].
func (h *Hub) Run(ctx context.Context) error {
	if !h.running.CompareAndSwap(false, true) {
		return &ConfigurationError{Detail: "the hub is already running; run it once"}
	}

	defer h.running.Store(false)

	return h.broadcaster.Listen(ctx, h.deliver)
}

// Running reports whether the hub is receiving signals.
func (h *Hub) Running() bool { return h.running.Load() }

// Heartbeat is how often a transport writes to an idle stream.
func (h *Hub) Heartbeat() time.Duration { return h.heartbeat }

// WriteTimeout is how long a transport waits for a client to accept a write.
func (h *Hub) WriteTimeout() time.Duration { return h.writeTimeout }

// deliver offers a signal to every subscription of its recipient. It never
// waits on a client.
func (h *Hub) deliver(signal Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for subscription := range h.subscriptions[signal.Recipient] {
		subscription.offer(signal)
	}
}

// Subscribe opens a subscription to a recipient's signals.
//
// It does not authorize: a transport asks its [SubscriptionAuthorizer] first.
// A hub that is not running refuses with an error matching [ErrUnavailable],
// and a recipient already holding the maximum number of subscriptions on this
// instance is refused with one matching [ErrTooManyStreams]. Every subscription
// must be closed.
func (h *Hub) Subscribe(recipient string) (*Subscription, error) {
	if !h.Running() {
		return nil, fmt.Errorf("%w: the hub is not receiving signals", ErrUnavailable)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	held := h.subscriptions[recipient]
	if len(held) >= h.maxStreams {
		return nil, fmt.Errorf("%w: %d streams are already open for this recipient", ErrTooManyStreams, h.maxStreams)
	}

	if held == nil {
		held = make(map[*Subscription]struct{})
		h.subscriptions[recipient] = held
	}

	subscription := &Subscription{hub: h, recipient: recipient, ready: make(chan struct{}, 1)}
	held[subscription] = struct{}{}

	return subscription, nil
}

// Subscription is one open stream's view of a recipient's signals.
//
// It holds at most one pending signal: signals arriving before the stream takes
// one replace it. A signal carries no content, so the latest says everything
// the ones it replaced did, and a client that stops reading costs no memory and
// never slows a publisher.
type Subscription struct {
	hub       *Hub
	recipient string
	ready     chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	pending Signal
	has     bool
}

// offer makes a signal the pending one and wakes the reader, without waiting.
func (s *Subscription) offer(signal Signal) {
	s.mu.Lock()
	s.pending = signal
	s.has = true
	s.mu.Unlock()

	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Ready receives when a signal may be pending. A receive can find nothing to
// take, which is harmless; [Subscription.Take] says for certain.
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// Take returns the pending signal and clears it, reporting false when none is
// pending.
func (s *Subscription) Take() (Signal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.has {
		return Signal{}, false
	}

	signal := s.pending
	s.pending, s.has = Signal{}, false

	return signal, true
}

// Close ends the subscription and frees its slot in the recipient's cap. It is
// safe to call more than once.
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()

		held := s.hub.subscriptions[s.recipient]
		delete(held, s)

		if len(held) == 0 {
			delete(s.hub.subscriptions, s.recipient)
		}
	})
}
