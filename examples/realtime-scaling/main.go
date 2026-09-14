// Command realtime-scaling carries notification change signals between
// application instances, and serves them over WebSocket.
//
// A signal is produced on the instance that stored a change, but the person it
// is for may have their stream open on any instance. The broadcaster carries the
// signal between them. The default reaches only its own process; notify/redis
// and notify/nats reach every instance listening on the same channel or subject.
//
// It needs Redis and NATS:
//
//	docker run --rm -p 6379:6379 redis:8.2.9-alpine
//	docker run --rm -p 4222:4222 nats:2.12.7-alpine
//	HMNTSK_REDIS_ADDR=127.0.0.1:6379 HMNTSK_NATS_URL=nats://127.0.0.1:4222 go run ./realtime-scaling
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	cws "github.com/coder/websocket"
	natsgo "github.com/nats-io/nats.go"
	goredis "github.com/redis/go-redis/v9"

	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/notify"
	notifynats "github.com/kartaladev/hmntsk/notify/nats"
	notifyredis "github.com/kartaladev/hmntsk/notify/redis"
	nws "github.com/kartaladev/hmntsk/notify/websocket"
)

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintln(os.Stderr, "realtime-scaling:", err)
		os.Exit(1)
	}
}

// runMain connects to the brokers named in the environment. The test provisions
// them in containers instead and calls run directly.
func runMain() error {
	redisAddr, err := demo.RequireEnv(os.Getenv, "HMNTSK_REDIS_ADDR",
		"docker run --rm -p 6379:6379 "+notifyredis.RedisImage+"   # then HMNTSK_REDIS_ADDR=127.0.0.1:6379")
	if err != nil {
		return err
	}

	natsURL, err := demo.RequireEnv(os.Getenv, "HMNTSK_NATS_URL",
		"docker run --rm -p 4222:4222 "+notifynats.NATSImage+"   # then HMNTSK_NATS_URL=nats://127.0.0.1:4222")
	if err != nil {
		return err
	}

	client := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	defer client.Close()

	conn, err := natsgo.Connect(natsURL)
	if err != nil {
		return fmt.Errorf("connect to NATS at %s: %w", natsURL, err)
	}
	defer conn.Close()

	return run(context.Background(), os.Stdout, brokers{redis: client, nats: conn})
}

// brokers are the connections the scenario needs.
type brokers struct {
	redis *goredis.Client
	nats  *natsgo.Conn
}

func run(ctx context.Context, w io.Writer, conns brokers) error {
	demo.Default(w, "the in-process broadcaster reaches only its own instance")

	if err := inProcess(ctx, w); err != nil {
		return err
	}

	demo.Override(w, "a Redis broadcaster on every instance")

	if err := redisAcrossInstances(ctx, w, conns); err != nil {
		return err
	}

	demo.Override(w, "a NATS broadcaster on every instance")

	nats, _, err := acrossInstances(ctx, w, "NATS subject "+notifynats.DefaultSubject, "INV-62",
		func() (notify.Broadcaster, error) { return notifynats.NewBroadcaster(conns.nats) })
	if err != nil {
		return err
	}

	nats.stop()

	demo.Override(w, "the WebSocket endpoint, and who may open it")

	return overWebSocket(ctx, w, conns)
}

// redisAcrossInstances also shows what a signal carries: a broadcaster tells an
// instance who changed, how and when, never a title, link or payload, so the
// client re-reads from the store, which is the truth.
func redisAcrossInstances(ctx context.Context, w io.Writer, conns brokers) error {
	p, fields, err := acrossInstances(ctx, w, "Redis channel "+notifyredis.DefaultChannel, "INV-61",
		func() (notify.Broadcaster, error) { return notifyredis.NewBroadcaster(conns.redis) })
	if err != nil {
		return err
	}
	defer p.stop()

	fmt.Fprintf(w, "the signal's data holds only: %s\n", strings.Join(fields, ", "))

	return printList(ctx, w, p.b, invoicing.Bob)
}

// inProcess wires two instances with nothing but required options. Each has its
// own in-process broadcaster, so a signal never leaves the instance that
// produced it: that is the documented single-instance limit of the default.
// The notification itself is stored, and any instance can read it.
func inProcess(ctx context.Context, w io.Writer) error {
	// One store stands in for the database every instance of an application
	// shares. The broadcaster is what instances do not share by default.
	store := notify.NewMemoryStore()

	a, err := startInstance(ctx, store, nil, nil)
	if err != nil {
		return err
	}
	defer a.stop()

	b, err := startInstance(ctx, store, nil, nil)
	if err != nil {
		return err
	}
	defer b.stop()

	fmt.Fprintln(w, "instances A and B share one notification store")

	aliceStream, err := openStream(ctx, a.base, invoicing.Alice)
	if err != nil {
		return err
	}
	defer aliceStream.close()

	bobStream, err := openStream(ctx, b.base, invoicing.Bob)
	if err != nil {
		return err
	}
	defer bobStream.close()

	fmt.Fprintln(w, "alice's stream is open on A, bob's on B")

	if _, err := a.svc.Publish(ctx, approvalNeeded(invoicing.Alice, "INV-60"), approvalNeeded(invoicing.Bob, "INV-60")); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	fmt.Fprintln(w, "notifications for alice and bob published through A")

	change, err := aliceStream.nextChange(signalWait)
	if err != nil {
		return fmt.Errorf("alice's stream: %w", err)
	}

	fmt.Fprintf(w, "alice's stream on A: unread-changed (%s)\n", change)

	// Absence can only be shown by waiting a while. A cross-instance signal
	// arrives in milliseconds, so this is long enough to mean "never".
	_, err = bobStream.nextChange(absenceWait)
	fmt.Fprintf(w, "bob's stream on B got a signal: %t\n", err == nil)

	return printCount(ctx, w, b, invoicing.Bob)
}

// acrossInstances gives both instances a cross-instance broadcaster on one
// broker. A notification published through A signals bob's stream on B. It
// returns the running pair, for the caller to look further and then stop, and
// the names of the fields the signal carried.
func acrossInstances(
	ctx context.Context,
	w io.Writer,
	where, invoice string,
	newBroadcaster func() (notify.Broadcaster, error),
) (_ *pair, fields []string, err error) {
	p, err := startPair(ctx, newBroadcaster, nil)
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		if err != nil {
			p.stop()
		}
	}()

	fmt.Fprintf(w, "instances A and B listen on %s\n", where)

	stream, err := openStream(ctx, p.b.base, invoicing.Bob)
	if err != nil {
		return nil, nil, err
	}
	defer stream.close()

	fmt.Fprintln(w, "bob's stream is open on B")

	if _, err := p.a.svc.Publish(ctx, approvalNeeded(invoicing.Bob, invoice)); err != nil {
		return nil, nil, fmt.Errorf("publish: %w", err)
	}

	fmt.Fprintln(w, "a notification for bob published through A")

	data, ok := stream.next(signalWait)
	if !ok {
		return nil, nil, errors.New("bob's stream on B: no signal arrived")
	}

	change, fields, err := decodeSignal(data)
	if err != nil {
		return nil, nil, err
	}

	fmt.Fprintf(w, "bob's stream on B: unread-changed (%s)\n", change)

	return p, fields, nil
}

// Origins for the WebSocket section. The foreign one is a page on another site
// trying to open a socket with the user's credentials.
const (
	foreignOrigin = "https://evil.example"
	appOrigin     = "https://app.example.com"
	appBasePath   = "/app"
)

// overWebSocket serves the WebSocket endpoint on instance B twice: with its
// defaults, which accept only the request's own host as a browser origin, and
// configured with WithOriginPatterns for the host's own web application, served
// from another origin. Over an open socket bob receives change signals and
// marks notifications read.
func overWebSocket(ctx context.Context, w io.Writer, conns brokers) error {
	mount := func(mux *http.ServeMux, svc *notify.Service, hub *notify.Hub) error {
		actor := nws.WithActor(demo.NotifyActor)

		strict, err := nws.NewHandler(svc, hub, actor)
		if err != nil {
			return fmt.Errorf("new socket handler: %w", err)
		}

		partner, err := nws.NewHandler(svc, hub, actor,
			nws.WithBasePath(appBasePath),
			nws.WithOriginPatterns("app.example.com"),
		)
		if err != nil {
			return fmt.Errorf("new socket handler: %w", err)
		}

		mux.Handle(strict.Pattern(), strict)
		mux.Handle(partner.Pattern(), partner)

		return nil
	}

	p, err := startPair(ctx,
		func() (notify.Broadcaster, error) { return notifyredis.NewBroadcaster(conns.redis) }, mount)
	if err != nil {
		return err
	}
	defer p.stop()

	a, b := p.a, p.b

	socketURL := "ws" + strings.TrimPrefix(b.base, "http")

	refused, err := dial(ctx, socketURL+notify.DefaultBasePath+"/notifications/socket", foreignOrigin)
	if err == nil {
		_ = refused.conn.CloseNow()

		return errors.New("a foreign origin was accepted by default")
	}

	fmt.Fprintf(w, "bob opens a socket on B from origin %s → %d\n", foreignOrigin, refused.status)

	accepted, err := dial(ctx, socketURL+appBasePath+"/notifications/socket", appOrigin)
	if err != nil {
		return fmt.Errorf("open socket: %w", err)
	}
	defer func() { _ = accepted.conn.CloseNow() }()

	fmt.Fprintf(w, "bob opens a socket on B from origin %s → %d, subprotocol %s\n",
		appOrigin, accepted.status, accepted.conn.Subprotocol())

	published, err := a.svc.Publish(ctx, approvalNeeded(invoicing.Bob, "INV-63"))
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	fmt.Fprintln(w, "a notification for bob published through A")

	signal, err := readUntil(ctx, accepted.conn, "unread-changed")
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "bob's socket on B: unread-changed (%s)\n", signal.Change)

	request, err := json.Marshal(map[string]any{
		"type": "mark-read",
		"ref":  "r1",
		"ids":  []string{published.Created[0].ID},
	})
	if err != nil {
		return fmt.Errorf("encode mark-read: %w", err)
	}

	if err := accepted.conn.Write(ctx, cws.MessageText, request); err != nil {
		return fmt.Errorf("send mark-read: %w", err)
	}

	// Marking read is itself a change, so an unread-changed signal may arrive
	// before the answer; only the answer to r1 matters here.
	marked, err := readUntil(ctx, accepted.conn, "marked")
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "bob sends mark-read over the socket → marked %d\n", marked.Marked)

	if err := accepted.conn.Close(cws.StatusNormalClosure, "done"); err != nil {
		return fmt.Errorf("close socket: %w", err)
	}

	return printCount(ctx, w, b, invoicing.Bob)
}

const (
	// signalWait bounds how long an expected signal may take.
	signalWait = 10 * time.Second
	// absenceWait is how long the scenario waits to show a signal never came.
	absenceWait = 300 * time.Millisecond
)

// approvalNeeded is a notification a host publishes directly through notify,
// with no task engine involved: notify knows nothing about invoices.
func approvalNeeded(recipient, invoice string) notify.Draft {
	return notify.Draft{
		Recipient: recipient,
		SourceID:  "approval-needed/" + invoice + "/" + recipient,
		Subject:   "invoice/" + invoice,
		Kind:      "approval-needed",
		Title:     "Approve " + invoice,
		Links:     map[string]string{"context": "/invoices/" + invoice + "/approve"},
	}
}

// instance is one application instance: a notification service, its hub and
// the HTTP endpoints clients connect to.
type instance struct {
	svc   *notify.Service
	hub   *notify.Hub
	base  string
	stops []func()
}

// startInstance starts an instance over the shared store. A nil broadcaster
// keeps the in-process default. mount, when set, adds endpoints of its own.
func startInstance(
	ctx context.Context,
	store notify.Store,
	broadcaster notify.Broadcaster,
	mount func(*http.ServeMux, *notify.Service, *notify.Hub) error,
) (_ *instance, err error) {
	var opts []notify.Option
	if broadcaster != nil {
		opts = append(opts, notify.WithBroadcaster(broadcaster))
	}

	svc, err := notify.New(store, opts...)
	if err != nil {
		return nil, fmt.Errorf("new notifier: %w", err)
	}

	hub, err := notify.NewHub(svc.Broadcaster())
	if err != nil {
		return nil, fmt.Errorf("new hub: %w", err)
	}

	in := &instance{svc: svc, hub: hub}

	defer func() {
		if err != nil {
			in.stop()
		}
	}()

	in.stops = append(in.stops, demo.Background(ctx, hub.Run))

	err = demo.WaitUntil(hub.Running, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("hub did not start: %w", err)
	}

	handler, err := notify.NewHandler(svc, hub, notify.WithActor(demo.NotifyActor))
	if err != nil {
		return nil, fmt.Errorf("new handler: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/notifications", handler)
	mux.Handle("/v1/notifications/", handler)

	if mount != nil {
		// Assigned, not declared: the deferred cleanup reads the named result.
		err = mount(mux, svc, hub)
		if err != nil {
			return nil, err
		}
	}

	base, stop, err := demo.Serve(mux)
	if err != nil {
		return nil, err
	}

	in.base = base
	in.stops = append(in.stops, stop)

	return in, nil
}

// pair is two application instances over one shared store.
type pair struct {
	a, b *instance
}

func (p *pair) stop() {
	p.b.stop()
	p.a.stop()
}

// startPair starts instances A and B, each with a broadcaster of its own on the
// same broker, and waits until a signal broadcast on A reaches B. They share one
// store, which stands in for the database every instance of an application
// shares.
func startPair(
	ctx context.Context,
	newBroadcaster func() (notify.Broadcaster, error),
	mountOnB func(*http.ServeMux, *notify.Service, *notify.Hub) error,
) (*pair, error) {
	store := notify.NewMemoryStore()

	broadcasterA, err := newBroadcaster()
	if err != nil {
		return nil, fmt.Errorf("new broadcaster: %w", err)
	}

	broadcasterB, err := newBroadcaster()
	if err != nil {
		return nil, fmt.Errorf("new broadcaster: %w", err)
	}

	a, err := startInstance(ctx, store, broadcasterA, nil)
	if err != nil {
		return nil, err
	}

	b, err := startInstance(ctx, store, broadcasterB, mountOnB)
	if err != nil {
		a.stop()

		return nil, err
	}

	p := &pair{a: a, b: b}

	if err := connected(ctx, a, b); err != nil {
		p.stop()

		return nil, err
	}

	return p, nil
}

// probeRecipient receives only the scenario's own readiness probes.
const probeRecipient = "readiness-probe"

// connected waits until a signal broadcast on one instance reaches the other.
// A hub reports running as soon as it starts, before its broadcaster has
// subscribed to the broker, so a signal sent in that moment would be lost;
// probing first keeps the scenario's output the same on every run.
func connected(ctx context.Context, from, to *instance) error {
	subscription, err := to.hub.Subscribe(probeRecipient)
	if err != nil {
		return fmt.Errorf("subscribe probe: %w", err)
	}
	defer subscription.Close()

	return demo.WaitUntil(func() bool {
		probe := []notify.Signal{{Recipient: probeRecipient, Change: notify.ChangeCreated, At: time.Now()}}
		if err := from.svc.Broadcaster().Broadcast(ctx, probe); err != nil {
			return false
		}

		select {
		case <-subscription.Ready():
			_, ok := subscription.Take()

			return ok
		case <-time.After(50 * time.Millisecond):
			return false
		}
	}, signalWait)
}

func (in *instance) stop() {
	for _, stop := range slices.Backward(in.stops) {
		stop()
	}

	in.stops = nil
}

// stream is a client of the server-sent event stream, as a browser's
// EventSource is.
type stream struct {
	cancel context.CancelFunc
	body   io.ReadCloser
	data   chan string
	done   chan struct{}
}

// openStream opens actor's stream and returns once the server has subscribed
// it, which is when it writes its opening comment.
func openStream(ctx context.Context, base, actor string) (*stream, error) {
	streamCtx, cancel := context.WithCancel(ctx)

	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, base+"/v1/notifications/stream", http.NoBody)
	if err != nil {
		cancel()

		return nil, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set(demo.ActorHeader, actor)

	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // the stream outlives this call; stream.close closes it
	if err != nil {
		cancel()

		return nil, fmt.Errorf("open stream: %w", err)
	}

	lines := bufio.NewReader(resp.Body)

	first, err := lines.ReadString('\n')
	if err != nil || resp.StatusCode != http.StatusOK || strings.TrimSpace(first) != ": connected" {
		cancel()
		_ = resp.Body.Close()

		return nil, fmt.Errorf("open stream: status %d, first line %q: %w", resp.StatusCode, first, err)
	}

	s := &stream{cancel: cancel, body: resp.Body, data: make(chan string, 8), done: make(chan struct{})}

	go func() {
		defer close(s.done)

		for {
			line, err := lines.ReadString('\n')
			if err != nil {
				return
			}

			if data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: "); ok {
				select {
				case s.data <- data:
				default:
				}
			}
		}
	}()

	return s, nil
}

// next returns the next signal's data, and false if none arrives within wait.
func (s *stream) next(wait time.Duration) (string, bool) {
	select {
	case data := <-s.data:
		return data, true
	case <-time.After(wait):
		return "", false
	}
}

func (s *stream) nextChange(wait time.Duration) (notify.Change, error) {
	data, ok := s.next(wait)
	if !ok {
		return "", errors.New("no signal arrived")
	}

	change, _, err := decodeSignal(data)

	return change, err
}

func (s *stream) close() {
	s.cancel()
	_ = s.body.Close()
	<-s.done
}

// decodeSignal reads a stream event's data and the names of its fields.
func decodeSignal(data string) (notify.Change, []string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &fields); err != nil {
		return "", nil, fmt.Errorf("decode signal %q: %w", data, err)
	}

	var change notify.Change
	if err := json.Unmarshal(fields["change"], &change); err != nil {
		return "", nil, fmt.Errorf("decode change: %w", err)
	}

	return change, slices.Sorted(maps.Keys(fields)), nil
}

type socket struct {
	conn   *cws.Conn
	status int
}

// dial opens a WebSocket as bob from a browser page at origin, offering the
// notify subprotocol. A refusal is answered before the upgrade, so its status
// comes back with the error.
func dial(ctx context.Context, url, origin string) (socket, error) {
	conn, resp, err := cws.Dial(ctx, url, &cws.DialOptions{
		HTTPHeader:   http.Header{demo.ActorHeader: {invoicing.Bob}, "Origin": {origin}},
		Subprotocols: []string{nws.Subprotocol},
	})

	status := 0
	if resp != nil {
		status = resp.StatusCode

		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}

	if err != nil {
		return socket{status: status}, fmt.Errorf("dial %s: %w", url, err)
	}

	return socket{conn: conn, status: status}, nil
}

// message is any message the server sends over the socket.
type message struct {
	Type   string        `json:"type"`
	Change notify.Change `json:"change"`
	Ref    string        `json:"ref"`
	Marked int64         `json:"marked"`
	Code   string        `json:"code"`
}

// readUntil reads messages until one of the wanted type arrives.
func readUntil(ctx context.Context, conn *cws.Conn, want string) (message, error) {
	readCtx, cancel := context.WithTimeout(ctx, signalWait)
	defer cancel()

	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			return message{}, fmt.Errorf("read socket waiting for %s: %w", want, err)
		}

		var m message
		if err := json.Unmarshal(data, &m); err != nil {
			return message{}, fmt.Errorf("decode socket message %q: %w", data, err)
		}

		if m.Type == "error" {
			return message{}, fmt.Errorf("socket error %s for %s", m.Code, m.Ref)
		}

		if m.Type == want {
			return m, nil
		}
	}
}

func printCount(ctx context.Context, w io.Writer, in *instance, actor string) error {
	var body struct {
		Count int64 `json:"count"`
	}

	status, err := get(ctx, in.base+"/v1/notifications/count", actor, &body)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%s GET /v1/notifications/count on B → %d count=%d\n", actor, status, body.Count)

	return nil
}

func printList(ctx context.Context, w io.Writer, in *instance, actor string) error {
	var body struct {
		Notifications []notify.Notification `json:"notifications"`
	}

	status, err := get(ctx, in.base+"/v1/notifications", actor, &body)
	if err != nil {
		return err
	}

	parts := make([]string, 0, len(body.Notifications))
	for _, n := range body.Notifications {
		parts = append(parts, fmt.Sprintf("%s %s %q", n.Kind, n.State, n.Title))
	}

	fmt.Fprintf(w, "%s GET /v1/notifications on B → %d %s\n", actor, status, strings.Join(parts, ", "))

	return nil
}

func get(ctx context.Context, url, actor string, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set(demo.ActorHeader, actor)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			return 0, fmt.Errorf("decode %s: %w", url, err)
		}
	}

	return resp.StatusCode, nil
}
