// Command event-delivery gets task events to the code that acts on them: first
// an in-process handler, then the relay delivering to a signed webhook.
//
// An in-process handler runs after the engine's commit, in the same process.
// A process that crashes after committing loses the call. The relay instead
// reads the outbox the engine wrote in the same transaction as the task, and
// keeps offering each event to each sink until the sink takes it or it is
// dead-lettered, so nothing committed is lost.
//
//	go run ./event-delivery
package main

import (
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
	"sync"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/delivery/webhook"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/relay"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "event-delivery:", err)
		os.Exit(1)
	}
}

// secret is shared by the sink, which signs, and the receiver, which verifies.
// A real host loads it from its secret store, one per receiver.
var secret = []byte("demo-secret-do-not-reuse")

var start = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

// backoff is the delay before the first retry in the sections that retry. The
// default is 30 seconds, doubled each time, with 20% jitter; jitter is switched
// off here only so that the printed schedule is the same on every run.
const backoff = time.Minute

func run(ctx context.Context, w io.Writer) error {
	demo.Default(w, "an in-process event handler")

	if err := inProcess(ctx, w); err != nil {
		return err
	}

	receiver, err := newReceiver()
	if err != nil {
		return err
	}

	base, stop, err := demo.Serve(receiver)
	if err != nil {
		return err
	}
	defer stop()

	// One service and store for both of these sections: the relay reads the
	// outbox the store keeps.
	svc, err := newService(demo.NewClock(start), nil)
	if err != nil {
		return err
	}

	callback := base + "/hooks/invoices"

	demo.Override(w, "the relay and a signed webhook, default destination policy")

	if err := refusedByDefault(ctx, w, svc, receiver, callback); err != nil {
		return err
	}

	demo.Override(w, "the same webhook, allowed to reach loopback")

	if err := allowedToLoopback(ctx, w, svc, receiver, callback); err != nil {
		return err
	}

	// A receiver that answers from a script, so the sections below can show
	// what the relay does with each kind of answer.
	script := newScripted(map[string][]int{
		"/hooks/flaky": {http.StatusServiceUnavailable, http.StatusNoContent},
		"/hooks/busy":  {http.StatusTooManyRequests},
		"/hooks/gone":  {http.StatusGone},
		"/hooks/ok":    {http.StatusNoContent},
	})

	scriptBase, stopScript, err := demo.Serve(script)
	if err != nil {
		return err
	}
	defer stopScript()

	demo.Override(w, "a receiver that is down, retried with backoff")

	if err := retriedWithBackoff(ctx, w, script, scriptBase); err != nil {
		return err
	}

	demo.Override(w, "two sinks, each accepting on its own")

	if err := independentSinks(ctx, w, script, scriptBase); err != nil {
		return err
	}

	demo.Override(w, "what an event says about its audience")

	return audience(ctx, w)
}

// inProcess is the default: handlers the engine calls after committing.
func inProcess(ctx context.Context, w io.Writer) error {
	printEvent := hmntsk.EventHandlerFunc(func(_ context.Context, event hmntsk.Event) error {
		line := fmt.Sprintf("handler: %s %s", event.Type, event.Correlation.OwnerRef)
		if event.Type == hmntsk.EventTypeCompleted {
			line += " " + string(event.Output)
		}

		fmt.Fprintln(w, line)

		return nil
	})

	svc, err := newService(demo.NewClock(start), []hmntsk.EventHandler{printEvent})
	if err != nil {
		return err
	}

	return approveThrough(ctx, svc, "INV-42", "")
}

// refusedByDefault delivers to a callback address on 127.0.0.1. The webhook's
// default destination policy refuses loopback, private and link-local
// addresses, because the address is chosen by whoever created the task, and
// following it blindly makes the host a proxy into its own network. A refusal
// cannot be fixed by retrying, so the event is dead-lettered at once.
func refusedByDefault(ctx context.Context, w io.Writer, svc *hmntsk.Service, receiver *receiver, callback string) error {
	sink, err := webhook.New(secret)
	if err != nil {
		return fmt.Errorf("new sink: %w", err)
	}

	if _, err := svc.Create(ctx, createRequest("INV-43", callback)); err != nil {
		return fmt.Errorf("create: %w", err)
	}

	fmt.Fprintln(w, "approval for INV-43 created with a callback to the receiver on 127.0.0.1")

	var (
		mu      sync.Mutex
		refusal *webhook.DestinationError
	)

	// A refusal is recorded on the outbox row and also reported to the host's
	// error handler, which receives the sink's typed error to match on.
	r, err := relay.NewRelay(svc,
		relay.WithSinks(sink),
		relay.WithRelayErrorHandler(func(_ context.Context, err error) {
			mu.Lock()
			defer mu.Unlock()

			errors.As(err, &refusal)
		}),
	)
	if err != nil {
		return fmt.Errorf("new relay: %w", err)
	}

	if err := pass(ctx, w, r); err != nil {
		return err
	}

	mu.Lock()
	loopback := refusal != nil && refusal.IP.IsLoopback()
	mu.Unlock()

	fmt.Fprintf(w, "the receiver got nothing; the recorded error names loopback: %t\n",
		len(receiver.deliveries()) == 0 && loopback)

	return nil
}

// allowedToLoopback replaces the destination policy. AllowLoopback permits
// loopback and nothing else; a host whose receivers live on its own network
// writes a policy that names them.
func allowedToLoopback(ctx context.Context, w io.Writer, svc *hmntsk.Service, receiver *receiver, callback string) error {
	sink, err := loopbackSink()
	if err != nil {
		return err
	}

	if err := approveThrough(ctx, svc, "INV-44", callback); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-44 created, claimed, started and completed")

	// A host runs relay.Run on an interval; one pass is enough here.
	if err := relayOnce(ctx, w, svc, sink); err != nil {
		return err
	}

	deliveries := receiver.deliveries()

	types := make([]string, 0, len(deliveries))
	for _, d := range deliveries {
		types = append(types, string(d.payload.Event.Type))
	}

	fmt.Fprintf(w, "receiver verified: %s\n", strings.Join(types, ", "))

	if len(deliveries) == 0 {
		return nil
	}

	last := deliveries[len(deliveries)-1]
	fmt.Fprintf(w, "receiver saw correlation %s and reference parameters %s\n",
		last.payload.Correlation.OwnerRef, last.payload.ReferenceParameters)

	// The signature covers the timestamp and the exact body, so any change to
	// either is detected.
	tampered := []byte(strings.Replace(string(last.body), `"approved":true`, `"approved":false`, 1))
	fmt.Fprintf(w, "a tampered body fails verification: %t\n", receiver.verifier.Verify(last.header, tampered) != nil)

	return nil
}

// retriedWithBackoff shows how the relay treats a receiver's answer. A 5xx, a
// 408 or a 429 is the receiver having a bad moment: the event is scheduled for
// another attempt after a delay that doubles each time. Any other 4xx says the
// request itself is wrong, and the same request will not do better: it is
// dead-lettered.
func retriedWithBackoff(ctx context.Context, w io.Writer, script *scripted, base string) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock, nil)
	if err != nil {
		return err
	}

	sink, err := loopbackSink()
	if err != nil {
		return err
	}

	// The relay measures "due" with the engine's clock, which is how this
	// scenario moves time forward.
	var (
		mu     sync.Mutex
		status *webhook.StatusError
	)

	r, err := relay.NewRelay(svc,
		relay.WithSinks(sink),
		relay.WithBackoff(backoff, time.Hour),
		relay.WithJitter(0),
		// The handler receives the sink's typed error, so the host can tell a
		// receiver's answer from a refusal without reading error text.
		relay.WithRelayErrorHandler(func(_ context.Context, err error) {
			mu.Lock()
			defer mu.Unlock()

			errors.As(err, &status)
		}),
	)
	if err != nil {
		return fmt.Errorf("new relay: %w", err)
	}

	created, err := svc.Create(ctx, createRequest("INV-45", base+"/hooks/flaky"))
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	fmt.Fprintln(w, "approval for INV-45 created; its receiver answers 503, then 204")

	if err := pass(ctx, w, r); err != nil {
		return err
	}

	entry, err := svc.OutboxEntry(ctx, created.Events[0].ID)
	if err != nil {
		return fmt.Errorf("outbox entry: %w", err)
	}

	next := "never"
	if entry.NextAttemptAt != nil {
		next = entry.NextAttemptAt.Sub(clock.Now()).String()
	}

	mu.Lock()
	answered503 := status != nil && status.StatusCode == http.StatusServiceUnavailable
	mu.Unlock()

	fmt.Fprintf(w, "outbox: attempts %d, next attempt in %s, last error names 503: %t\n",
		entry.Attempts, next, answered503)

	// Not due yet: a pass before the backoff has passed claims nothing.
	if err := pass(ctx, w, r); err != nil {
		return err
	}

	clock.Advance(backoff)
	fmt.Fprintf(w, "%s later\n", backoff)

	if err := pass(ctx, w, r); err != nil {
		return err
	}

	fmt.Fprintf(w, "the receiver answered INV-45 with: %s\n", script.answers("/hooks/flaky"))

	if _, err := svc.Create(ctx, createRequest("INV-46", base+"/hooks/busy")); err != nil {
		return fmt.Errorf("create: %w", err)
	}

	if _, err := svc.Create(ctx, createRequest("INV-47", base+"/hooks/gone")); err != nil {
		return fmt.Errorf("create: %w", err)
	}

	fmt.Fprintln(w, "approvals for INV-46 (receiver answers 429) and INV-47 (receiver answers 410) created")

	return pass(ctx, w, r)
}

// independentSinks offers every event to two sinks. Acceptance is recorded per
// sink, so when one fails and the other succeeds, the retry goes only to the
// one that failed: the webhook's receiver is not called twice for another
// sink's trouble.
func independentSinks(ctx context.Context, w io.Writer, script *scripted, base string) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock, nil)
	if err != nil {
		return err
	}

	sink, err := loopbackSink()
	if err != nil {
		return err
	}

	var (
		mu     sync.Mutex
		failed []error
	)

	r, err := relay.NewRelay(svc,
		relay.WithSinks(sink, &ledger{}),
		relay.WithBackoff(backoff, time.Hour),
		relay.WithJitter(0),
		// The default handler does nothing. A host supplies one that logs: a
		// failure is otherwise visible only in the outbox row.
		relay.WithRelayErrorHandler(func(_ context.Context, err error) {
			mu.Lock()
			defer mu.Unlock()

			failed = append(failed, err)
		}),
	)
	if err != nil {
		return fmt.Errorf("new relay: %w", err)
	}

	created, err := svc.Create(ctx, createRequest("INV-48", base+"/hooks/ok"))
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	fmt.Fprintln(w, "approval for INV-48 created; the ledger sink fails its first attempt")

	if err := pass(ctx, w, r); err != nil {
		return err
	}

	entry, err := svc.OutboxEntry(ctx, created.Events[0].ID)
	if err != nil {
		return fmt.Errorf("outbox entry: %w", err)
	}

	fmt.Fprintf(w, "accepted so far: %s\n", strings.Join(entry.Accepted, ", "))

	mu.Lock()
	sawLedger := slices.ContainsFunc(failed, func(err error) bool {
		return errors.Is(err, errLedgerDown)
	})
	mu.Unlock()

	fmt.Fprintf(w, "the error handler saw the ledger's failure: %t\n", sawLedger)

	clock.Advance(backoff)
	fmt.Fprintf(w, "%s later\n", backoff)

	if err := pass(ctx, w, r); err != nil {
		return err
	}

	fmt.Fprintf(w, "the webhook receiver got INV-48 once: %t\n", script.count("/hooks/ok") == 1)

	return nil
}

// audience prints what every event carries about who is involved, as it stood
// right after the transition: the candidate pool, the holder, the holder a
// release or delegation replaced, and the creator. A consumer can decide whom
// to tell without reading the task back.
func audience(ctx context.Context, w io.Writer) error {
	svc, err := newService(demo.NewClock(start), nil)
	if err != nil {
		return err
	}

	receiver, err := newReceiver()
	if err != nil {
		return err
	}

	base, stop, err := demo.Serve(receiver)
	if err != nil {
		return err
	}
	defer stop()

	created, err := svc.Create(ctx, createRequest("INV-49", base+"/hooks/audit"))
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	request := hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: invoicing.Alice}

	if _, err := svc.Claim(ctx, request); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	if _, err := svc.Release(ctx, request); err != nil {
		return fmt.Errorf("release: %w", err)
	}

	fmt.Fprintln(w, "approval for INV-49 created, then claimed and released by alice")

	sink, err := loopbackSink()
	if err != nil {
		return err
	}

	if err := relayOnce(ctx, w, svc, sink); err != nil {
		return err
	}

	for _, d := range receiver.deliveries() {
		event := d.payload.Event

		parts := []string{"created by " + event.CreatedBy}
		if event.Assignee != "" {
			parts = append(parts, "assignee "+event.Assignee)
		}

		parts = append(parts, fmt.Sprintf("candidates %v", event.Candidates.Groups))

		if event.PreviousAssignee != "" {
			parts = append(parts, "previous assignee "+event.PreviousAssignee)
		}

		fmt.Fprintf(w, "%s: %s\n", event.Type, strings.Join(parts, ", "))
	}

	return nil
}

func loopbackSink() (*webhook.Sink, error) {
	sink, err := webhook.New(secret, webhook.WithDestinationPolicy(webhook.AllowLoopback()))
	if err != nil {
		return nil, fmt.Errorf("new sink: %w", err)
	}

	return sink, nil
}

// relayOnce runs a relay with its defaults over one sink, once.
func relayOnce(ctx context.Context, w io.Writer, svc *hmntsk.Service, sink *webhook.Sink) error {
	r, err := relay.NewRelay(svc, relay.WithSinks(sink))
	if err != nil {
		return fmt.Errorf("new relay: %w", err)
	}

	return pass(ctx, w, r)
}

// pass runs one relay pass, as relay.Run does on each tick, and prints it.
func pass(ctx context.Context, w io.Writer, r *relay.Relay) error {
	result, err := r.Relay(ctx)
	if err != nil {
		return fmt.Errorf("relay: %w", err)
	}

	fmt.Fprintf(w, "relay pass: %s\n", describePass(result))

	return nil
}

// describePass summarises a pass and, per sink in name order, what it did.
func describePass(result relay.Result) string {
	sinks := make([]string, 0, len(result.Sinks))

	for _, name := range slices.Sorted(maps.Keys(result.Sinks)) {
		tally := result.Sinks[name]

		var parts []string

		for _, count := range []struct {
			label string
			n     int
		}{
			{"delivered", tally.Delivered},
			{"retryable", tally.Retryable},
			{"permanent", tally.Permanent},
			{"skipped", tally.Skipped},
		} {
			if count.n > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", count.label, count.n))
			}
		}

		if len(parts) == 0 {
			parts = append(parts, "nothing offered")
		}

		sinks = append(sinks, name+": "+strings.Join(parts, ", "))
	}

	return fmt.Sprintf("claimed %d, delivered %d, retried %d, dead-lettered %d (%s)",
		result.Claimed, result.Delivered, result.Retried, result.DeadLettered, strings.Join(sinks, "; "))
}

func newService(clock hmntsk.Clock, handlers []hmntsk.EventHandler) (*hmntsk.Service, error) {
	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithClock(clock),
		hmntsk.WithEventHandlers(handlers...),
	)
	if err != nil {
		return nil, fmt.Errorf("new service: %w", err)
	}

	if err := invoicing.Register(svc); err != nil {
		return nil, err
	}

	return svc, nil
}

// createRequest creates an approval. A callback address is where the creator
// wants this task's events delivered, and its reference parameters are echoed
// back with every delivery so the receiver can route them.
func createRequest(invoice, callback string) hmntsk.CreateRequest {
	req := hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: invoice, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoice, invoicing.ActivityApprove),
	}

	if callback != "" {
		req.Callback = &hmntsk.CallbackTarget{
			Address:             callback,
			ReferenceParameters: json.RawMessage(fmt.Sprintf(`{"invoice":%q}`, invoice)),
		}
	}

	return req
}

// approveThrough creates an approval and takes it to completion as alice.
func approveThrough(ctx context.Context, svc *hmntsk.Service, invoice, callback string) error {
	created, err := svc.Create(ctx, createRequest(invoice, callback))
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

// errLedgerDown is the ledger sink's failure on its first attempt.
var errLedgerDown = errors.New("ledger: unavailable")

// ledger is a second sink, standing for any destination of the host's own: an
// audit table, a message bus, a search index. It fails its first delivery and
// accepts every one after that.
type ledger struct {
	mu       sync.Mutex
	attempts int
}

// Name is the name the relay records this sink's acceptance under. It must not
// change, or every event is delivered to it again.
func (l *ledger) Name() string { return "ledger" }

// Deliver refuses the first attempt as retryable and accepts the rest.
func (l *ledger) Deliver(_ context.Context, _ relay.Attempt) relay.Outcome {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.attempts++
	if l.attempts == 1 {
		return relay.Retryable(errLedgerDown)
	}

	return relay.Delivered()
}

// receiver is the host's webhook endpoint: it verifies every request before
// trusting a byte of it, and answers 2xx only for what it has taken.
type receiver struct {
	verifier *webhook.Verifier

	mu       sync.Mutex
	received []delivery
}

type delivery struct {
	header  http.Header
	body    []byte
	payload webhook.Payload
}

func newReceiver() (*receiver, error) {
	// The receiver's clock is the real one: the sink signs with the time of the
	// attempt, and the verifier rejects signatures outside its tolerance.
	verifier, err := webhook.NewVerifier(secret)
	if err != nil {
		return nil, fmt.Errorf("new verifier: %w", err)
	}

	return &receiver{verifier: verifier}, nil
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)

		return
	}

	if err := rc.verifier.Verify(r.Header, body); err != nil {
		// 4xx is permanent to the sink: a bad signature will not get better.
		http.Error(w, "signature rejected", http.StatusUnauthorized)

		return
	}

	var payload webhook.Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "malformed payload", http.StatusBadRequest)

		return
	}

	rc.mu.Lock()
	rc.received = append(rc.received, delivery{header: r.Header.Clone(), body: body, payload: payload})
	rc.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (rc *receiver) deliveries() []delivery {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	return append([]delivery(nil), rc.received...)
}

// scripted answers each path with the next status in its script, repeating the
// last one once the script runs out. It stands for receivers having good and
// bad days; it does not verify signatures, which the receiver above shows.
type scripted struct {
	mu       sync.Mutex
	script   map[string][]int
	answered map[string][]int
}

func newScripted(script map[string][]int) *scripted {
	return &scripted{script: script, answered: make(map[string][]int)}
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)

	s.mu.Lock()

	status := http.StatusNotFound
	if plan := s.script[r.URL.Path]; len(plan) > 0 {
		status = plan[min(len(s.answered[r.URL.Path]), len(plan)-1)]
	}

	s.answered[r.URL.Path] = append(s.answered[r.URL.Path], status)
	s.mu.Unlock()

	w.WriteHeader(status)
}

// answers lists the statuses a path was answered with, in order.
func (s *scripted) answers(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	parts := make([]string, 0, len(s.answered[path]))
	for _, status := range s.answered[path] {
		parts = append(parts, fmt.Sprint(status))
	}

	return strings.Join(parts, ", ")
}

// count is how many requests a path received.
func (s *scripted) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.answered[path])
}
