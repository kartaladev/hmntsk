// Command event-bus publishes task events to message brokers for internal
// consumers: a Redis stream, plain NATS subjects and JetStream.
//
// Every sink is driven by the relay, so an event stays in the engine's outbox
// until each broker has taken it, and a broker that is down or misconfigured
// is retried rather than skipped. hmntsk only produces: consumer groups,
// acknowledgements and de-duplication belong to the consumers.
//
//	HMNTSK_REDIS_ADDR=127.0.0.1:6379 HMNTSK_NATS_URL=nats://127.0.0.1:4222 go run ./event-bus
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	goredis "github.com/redis/go-redis/v9"

	"github.com/kartaladev/hmntsk"
	hmntsknats "github.com/kartaladev/hmntsk/delivery/nats"
	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/relay"
)

// brokers are the servers the scenario publishes to. The program connects to
// the ones named in the environment; the test starts its own in containers.
type brokers struct {
	redis *goredis.Client
	nats  *nats.Conn
}

func main() {
	if err := connectAndRun(); err != nil {
		fmt.Fprintln(os.Stderr, "event-bus:", err)
		os.Exit(1)
	}
}

func connectAndRun() error {
	ctx := context.Background()

	addr, err := demo.RequireEnv(os.Getenv, "HMNTSK_REDIS_ADDR",
		"docker run --rm -p 6379:6379 "+hmntskredis.RedisImage)
	if err != nil {
		return err
	}

	url, err := demo.RequireEnv(os.Getenv, "HMNTSK_NATS_URL",
		"docker run --rm -p 4222:4222 "+hmntsknats.NATSImage+" -js")
	if err != nil {
		return err
	}

	client := goredis.NewClient(&goredis.Options{Addr: addr})
	defer client.Close()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to Redis at %s: %w", addr, err)
	}

	conn, err := nats.Connect(url, nats.Name("hmntsk-event-bus-example"))
	if err != nil {
		return fmt.Errorf("connect to NATS at %s: %w", url, err)
	}
	defer conn.Close()

	return run(ctx, os.Stdout, brokers{redis: client, nats: conn})
}

// Everything the scenario creates on the brokers, removed before it starts so
// that every run prints the same thing.
const (
	boundedStream = "acme.invoice-events"
	auditedStream = "acme.audited-events"
	eventsStream  = "HMNTSK_EVENTS"
	invoiceStream = "ACME_INVOICES"

	// invoicePrefix is the subject prefix the overrides' JetStream sinks
	// publish under, which ACME_INVOICES captures.
	invoicePrefix = "acme.invoices"
	// wrongStream is a stream the misdirected JetStream sink expects, which does
	// not capture its subjects.
	wrongStream = "ACME_ELSEWHERE"
)

// allEvents selects every event the default NATS sinks publish: event types are
// dot-separated, so ">" after the prefix matches them all.
var allEvents = hmntsknats.DefaultSubjectPrefix + ".>"

var start = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

// retryAfter is the relay's delay before a retry. Jitter is switched off only so
// that the printed schedule is the same on every run.
const retryAfter = time.Minute

func run(ctx context.Context, w io.Writer, b brokers) error {
	js, err := jetstream.New(b.nats)
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}

	if err := reset(ctx, b, js); err != nil {
		return err
	}

	demo.Default(w, "a Redis stream, with no bound")

	if err := redisDefault(ctx, w, b); err != nil {
		return err
	}

	demo.Default(w, "NATS subjects, where delivered means the server received it")

	if err := natsDefault(ctx, w, b); err != nil {
		return err
	}

	demo.Default(w, "JetStream, where delivered means a stream stored it")

	if err := jetStreamDefault(ctx, w, js); err != nil {
		return err
	}

	demo.Override(w, "bounded Redis streams, subject prefixes, an expected stream, and four sinks in one relay")

	return overrides(ctx, w, b, js)
}

// redisDefault appends every event to the default stream. With no bound
// configured nothing is ever trimmed.
func redisDefault(ctx context.Context, w io.Writer, b brokers) error {
	svc, err := newService(demo.NewClock(start))
	if err != nil {
		return err
	}

	if err := approveThrough(ctx, svc, "INV-42"); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-42 created, claimed, started and completed")

	sink, err := hmntskredis.New(b.redis)
	if err != nil {
		return fmt.Errorf("new redis sink: %w", err)
	}

	if _, err := pass(ctx, w, newRelay(svc, sink)); err != nil {
		return err
	}

	entries, err := b.redis.XRange(ctx, hmntskredis.DefaultStream, "-", "+").Result()
	if err != nil {
		return fmt.Errorf("read the stream: %w", err)
	}

	fmt.Fprintf(w, "stream %s holds %d entries:\n", hmntskredis.DefaultStream, len(entries))

	for _, entry := range entries {
		fmt.Fprintf(w, "  %s %s schema %s\n",
			entry.Values[hmntskredis.FieldEventType],
			entry.Values[hmntskredis.FieldOwnerRef],
			entry.Values[hmntskredis.FieldSchema])
	}

	return nil
}

// natsDefault publishes to plain subjects. The server confirms it received each
// message; it keeps no history, so a subscriber that was not listening misses
// the event for good.
func natsDefault(ctx context.Context, w io.Writer, b brokers) error {
	svc, err := newService(demo.NewClock(start))
	if err != nil {
		return err
	}

	sink, err := hmntsknats.NewSink(b.nats)
	if err != nil {
		return fmt.Errorf("new nats sink: %w", err)
	}

	r := newRelay(svc, sink)

	sub, err := subscribe(b.nats, allEvents)
	if err != nil {
		return err
	}

	if err := approveThrough(ctx, svc, "INV-43"); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-43 created, claimed, started and completed")

	if _, err := pass(ctx, w, r); err != nil {
		return err
	}

	fmt.Fprintf(w, "a subscriber on %s received:\n", allEvents)

	for range 4 {
		msg, err := sub.NextMsg(5 * time.Second)
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}

		// The headers are routing hints; the body is the whole event as JSON.
		fmt.Fprintf(w, "  %s (event type %s, invoice %s, schema %s)\n", msg.Subject,
			msg.Header.Get(hmntsknats.HeaderEventType),
			msg.Header.Get(hmntsknats.HeaderOwnerRef),
			msg.Header.Get(hmntsknats.HeaderSchema))
	}

	if err := sub.Unsubscribe(); err != nil {
		return fmt.Errorf("unsubscribe: %w", err)
	}

	if _, err := svc.Create(ctx, approvalRequest("INV-44")); err != nil {
		return fmt.Errorf("create: %w", err)
	}

	fmt.Fprintln(w, "approval for INV-44 created while nobody subscribed")

	if _, err := pass(ctx, w, r); err != nil {
		return err
	}

	late, err := subscribe(b.nats, allEvents)
	if err != nil {
		return err
	}
	defer func() { _ = late.Unsubscribe() }()

	received := "nothing"
	if msg, err := late.NextMsg(300 * time.Millisecond); err == nil {
		received = msg.Subject
	} else if !errors.Is(err, nats.ErrTimeout) {
		return fmt.Errorf("receive: %w", err)
	}

	fmt.Fprintf(w, "a subscriber that arrives afterwards receives: %s\n", received)

	return nil
}

// jetStreamDefault publishes to JetStream. The sink never creates a stream: until
// the host does, every attempt fails and is retried, and nothing is lost.
func jetStreamDefault(ctx context.Context, w io.Writer, js jetstream.JetStream) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock)
	if err != nil {
		return err
	}

	sink, err := hmntsknats.NewJetStreamSink(js)
	if err != nil {
		return fmt.Errorf("new jetstream sink: %w", err)
	}

	if err := approveThrough(ctx, svc, "INV-45"); err != nil {
		return err
	}

	fmt.Fprintf(w, "approval for INV-45 created, claimed, started and completed; no stream captures %s yet\n", allEvents)

	r := newRelay(svc, sink)

	if _, err := pass(ctx, w, r); err != nil {
		return err
	}

	// Subjects, retention, replicas and the duplicate window are the host's.
	duplicates := 2 * time.Minute

	stream, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:       eventsStream,
		Subjects:   []string{allEvents},
		Duplicates: duplicates,
	})
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}

	fmt.Fprintf(w, "the host creates stream %s for %s, discarding duplicates for %s\n", eventsStream, allEvents, duplicates)

	clock.Advance(retryAfter)
	fmt.Fprintf(w, "%s later\n", retryAfter)

	if _, err := pass(ctx, w, r); err != nil {
		return err
	}

	if err := printStored(ctx, w, stream, ""); err != nil {
		return err
	}

	// Every publication carries the event ID as its Nats-Msg-Id, so the stream
	// keeps one copy of an event however many times it is published inside the
	// duplicate window. A second sink on the same stream shows it.
	second, err := newService(demo.NewClock(start))
	if err != nil {
		return err
	}

	copySink, err := hmntsknats.NewJetStreamSink(js, hmntsknats.WithName("jetstream-copy"))
	if err != nil {
		return fmt.Errorf("new jetstream sink: %w", err)
	}

	if err := approveThrough(ctx, second, "INV-46"); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-46 created, claimed, started and completed, published by two JetStream sinks")

	if _, err := pass(ctx, w, newRelay(second, sink, copySink)); err != nil {
		return err
	}

	return printStored(ctx, w, stream, ": the second sink's copies were discarded as duplicates")
}

// overrides replaces each sink's defaults, and drives all four sinks with one
// relay. Each sink's acceptance is recorded on its own: a retry offers an event
// only to the sinks that have not taken it yet.
func overrides(ctx context.Context, w io.Writer, b brokers, js jetstream.JetStream) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock)
	if err != nil {
		return err
	}

	// A group that has read nothing yet, so the acknowledged-only trim mode has
	// something to protect.
	if err := b.redis.XGroupCreateMkStream(ctx, auditedStream, "auditors", "$").Err(); err != nil {
		return fmt.Errorf("create consumer group: %w", err)
	}

	stream, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: invoiceStream, Subjects: []string{invoicePrefix + ".>"}})
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}

	// Trimming is approximate by default: Redis removes only whole stream nodes,
	// so a bound would not visibly trim a handful of events. WithExactTrim trims
	// to the bound itself, on any server, which is what makes the printed
	// lengths exact; it costs the broker more work on every publish.
	bounded, err := hmntskredis.New(b.redis,
		hmntskredis.WithStream(boundedStream),
		hmntskredis.WithMaxLen(2),
		hmntskredis.WithExactTrim(),
	)
	if err != nil {
		return fmt.Errorf("new redis sink: %w", err)
	}

	audited, err := hmntskredis.New(b.redis,
		hmntskredis.WithName("redis-audited"),
		hmntskredis.WithStream(auditedStream),
		hmntskredis.WithMaxLen(2),
		hmntskredis.WithTrimMode(hmntskredis.TrimAcked),
		hmntskredis.WithExactTrim(),
	)
	if err != nil {
		return fmt.Errorf("new redis sink: %w", err)
	}

	live, err := hmntsknats.NewSink(b.nats, hmntsknats.WithSubjectPrefix("acme.live"))
	if err != nil {
		return fmt.Errorf("new nats sink: %w", err)
	}

	// The host names a stream the publications must land in. ACME_INVOICES is
	// the stream that captures them, so this expectation is wrong.
	misdirected, err := hmntsknats.NewJetStreamSink(js,
		hmntsknats.WithSubjectPrefix(invoicePrefix),
		hmntsknats.WithExpectStream(wrongStream),
	)
	if err != nil {
		return fmt.Errorf("new jetstream sink: %w", err)
	}

	completed := "acme.live." + string(hmntsk.EventTypeCompleted)

	sub, err := subscribe(b.nats, completed)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := approveThrough(ctx, svc, "INV-47"); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-47 created, claimed, started and completed")

	result, err := pass(ctx, w, newRelay(svc, misdirected, live, bounded, audited))
	if err != nil {
		return err
	}

	for _, s := range []struct{ stream, what string }{
		{boundedStream, "at most about 2 entries"},
		{auditedStream, "at most about 2 entries, trimming only what groups acknowledged"},
	} {
		length, err := b.redis.XLen(ctx, s.stream).Result()
		if err != nil {
			return fmt.Errorf("stream length: %w", err)
		}

		fmt.Fprintf(w, "redis stream %s, %s: holds %d\n", s.stream, s.what, length)
	}

	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		return fmt.Errorf("receive: %w", err)
	}

	fmt.Fprintf(w, "a subscriber on %s received: %s (invoice %s)\n", completed, msg.Subject,
		msg.Header.Get(hmntsknats.HeaderOwnerRef))

	if result.Sinks[hmntsknats.DefaultJetStreamName].Retryable == 0 {
		return errors.New("the misdirected JetStream sink was not refused")
	}

	fmt.Fprintf(w, "the JetStream sink expected stream %s, but %s captures %s.>, so it was refused\n",
		wrongStream, invoiceStream, invoicePrefix)

	corrected, err := hmntsknats.NewJetStreamSink(js,
		hmntsknats.WithSubjectPrefix(invoicePrefix),
		hmntsknats.WithExpectStream(invoiceStream),
	)
	if err != nil {
		return fmt.Errorf("new jetstream sink: %w", err)
	}

	clock.Advance(retryAfter)
	fmt.Fprintf(w, "the host corrects the expected stream to %s; %s later\n", invoiceStream, retryAfter)

	// The same sink names, so the relay knows which sinks already took each
	// event.
	if _, err := pass(ctx, w, newRelay(svc, corrected, live, bounded, audited)); err != nil {
		return err
	}

	return printStored(ctx, w, stream, "")
}

func newService(clock hmntsk.Clock) (*hmntsk.Service, error) {
	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithClock(clock),
	)
	if err != nil {
		return nil, fmt.Errorf("new service: %w", err)
	}

	if err := invoicing.Register(svc); err != nil {
		return nil, err
	}

	return svc, nil
}

func newRelay(svc *hmntsk.Service, sinks ...relay.Sink) *relay.Relay {
	// NewRelay refuses only options that cannot be wrong here.
	r, _ := relay.NewRelay(svc,
		relay.WithSinks(sinks...),
		relay.WithBackoff(retryAfter, time.Hour),
		relay.WithJitter(0),
	)

	return r
}

func approvalRequest(invoice string) hmntsk.CreateRequest {
	return hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: invoice, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoice, invoicing.ActivityApprove),
	}
}

// approveThrough creates an approval and takes it to completion as alice: four
// events.
func approveThrough(ctx context.Context, svc *hmntsk.Service, invoice string) error {
	created, err := svc.Create(ctx, approvalRequest(invoice))
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	request := hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: invoicing.Alice}

	if _, err := svc.Claim(ctx, request); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	if _, err := svc.Start(ctx, request); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	if _, err := svc.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: request,
		Output:      []byte(`{"approved":true,"reason":"within-budget"}`),
	}); err != nil {
		return fmt.Errorf("complete: %w", err)
	}

	return nil
}

// pass runs one relay pass, as relay.Run does on each tick, and prints what each
// sink did.
func pass(ctx context.Context, w io.Writer, r *relay.Relay) (relay.Result, error) {
	result, err := r.Relay(ctx)
	if err != nil {
		return relay.Result{}, fmt.Errorf("relay: %w", err)
	}

	sinks := make([]string, 0, len(result.Sinks))

	for _, name := range slices.Sorted(maps.Keys(result.Sinks)) {
		s := result.Sinks[name]

		var parts []string

		for _, c := range []struct {
			label string
			n     int
		}{
			{"delivered", s.Delivered},
			{"retryable", s.Retryable},
			{"permanent", s.Permanent},
			{"unclassified", s.Unclassified},
			{"skipped", s.Skipped},
		} {
			if c.n > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", c.label, c.n))
			}
		}

		if len(parts) == 0 {
			parts = append(parts, "nothing offered")
		}

		sinks = append(sinks, name+": "+strings.Join(parts, ", "))
	}

	fmt.Fprintf(w, "relay pass: claimed %d, delivered %d, retried %d (%s)\n",
		result.Claimed, result.Delivered, result.Retried, strings.Join(sinks, "; "))

	return result, nil
}

func subscribe(conn *nats.Conn, subject string) (*nats.Subscription, error) {
	sub, err := conn.SubscribeSync(subject)
	if err != nil {
		return nil, fmt.Errorf("subscribe to %s: %w", subject, err)
	}

	// Make sure the server has the subscription before anything is published.
	if err := conn.Flush(); err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}

	return sub, nil
}

func printStored(ctx context.Context, w io.Writer, stream jetstream.Stream, note string) error {
	info, err := stream.Info(ctx)
	if err != nil {
		return fmt.Errorf("stream info: %w", err)
	}

	fmt.Fprintf(w, "stream %s stores %d messages%s\n", info.Config.Name, info.State.Msgs, note)

	return nil
}

// reset removes what an earlier run left behind.
func reset(ctx context.Context, b brokers, js jetstream.JetStream) error {
	if err := b.redis.Del(ctx, hmntskredis.DefaultStream, boundedStream, auditedStream).Err(); err != nil {
		return fmt.Errorf("reset redis: %w", err)
	}

	for _, name := range []string{eventsStream, invoiceStream} {
		if err := js.DeleteStream(ctx, name); err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("reset stream %s: %w", name, err)
		}
	}

	return nil
}
