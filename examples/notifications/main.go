// Command notifications tells people about invoice tasks: task events become
// notifications a person lists, counts, reads and is told about as they happen.
//
// The pieces: tasknotify's projector is a relay sink that turns each task
// event into notifications; notify stores them, serves them over HTTP and
// streams "unread changed" signals; a hub fans signals out to open streams.
// Nothing runs on its own: the host starts the relay, the hub, and optionally
// a pruner and an email dispatcher.
//
//	go run ./notifications
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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
	notifysql "github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/relay"
	"github.com/kartaladev/hmntsk/sqlkit"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
	"github.com/kartaladev/hmntsk/tasknotify"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "notifications:", err)
		os.Exit(1)
	}
}

var start = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

// emailDelay is how far the scenarios move the clock before an email pass: past
// the dispatcher's default five-minute grace delay.
const emailDelay = 6 * time.Minute

func run(ctx context.Context, w io.Writer) error {
	demo.Default(w, "task events become notifications")

	if err := defaults(ctx, w); err != nil {
		return err
	}

	demo.Override(w, "SQLite, the host's links and titles, a supervisor's stream, retention and email")

	if err := overrides(ctx, w); err != nil {
		return err
	}

	for _, section := range []struct {
		title string
		run   func(context.Context, io.Writer) error
	}{
		{"releasing and delegating, under the default rules", releaseAndDelegate},
		{"a widening escalation offers the task to the newly eligible only", widening},
		{"host rules, and which statuses close notifications", hostRules},
		{"email for offers only, and a recipient with no address", emailOffersOnly},
		{"retention by age, and the default strategy under a count bound", retentionDefaults},
	} {
		demo.Override(w, section.title)

		if err := section.run(ctx, w); err != nil {
			return err
		}
	}

	return nil
}

// defaults wires everything with nothing but required options: an in-memory
// notification store, the default rules, links and titles, and the self-only
// stream policy.
func defaults(ctx context.Context, w io.Writer) error {
	a, err := newApp(ctx, notify.NewMemoryStore(), nil)
	if err != nil {
		return err
	}
	defer a.close()

	stream, err := a.openStream(ctx, invoicing.Bob, invoicing.Bob, a.base)
	if err != nil {
		return err
	}
	defer stream.close()

	if err := stream.expectConnected(); err != nil {
		return err
	}

	fmt.Fprintln(w, "bob's stream: connected")

	approval, err := a.createApproval(ctx, "INV-42")
	if err != nil {
		return err
	}

	a.names.Name(string(approval), "approval-42")

	if err := a.relayPass(ctx, w, "approval for INV-42 created"); err != nil {
		return err
	}

	change, err := stream.nextChange()
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "bob's stream: unread-changed (%s)\n", change)

	offers, err := a.list(ctx, w, invoicing.Alice)
	if err != nil {
		return err
	}

	printLinks(w, a.names, "alice", offers[0])

	if err := a.count(ctx, w, invoicing.Alice); err != nil {
		return err
	}

	if _, err := a.engine.Claim(ctx, hmntsk.TaskRequest{TaskID: approval, Actor: invoicing.Bob}); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	if err := a.relayPass(ctx, w, "bob claimed it"); err != nil {
		return err
	}

	after, err := a.list(ctx, w, invoicing.Alice)
	if err != nil {
		return err
	}

	// The claimant is never told about their own action: bob has nothing open.
	if err := a.count(ctx, w, invoicing.Bob); err != nil {
		return err
	}

	taken := slices.IndexFunc(after, func(n notify.Notification) bool { return n.Kind == tasknotify.KindTaken })
	if taken < 0 {
		return errors.New("alice was not told her offer was taken")
	}

	a.names.Name(after[taken].ID, "taken-42")

	if err := a.post(ctx, w, invoicing.Alice, "/v1/notifications/"+after[taken].ID+"/read"); err != nil {
		return err
	}

	return a.count(ctx, w, invoicing.Alice)
}

// overrides replaces one default at a time, each where a host would.
func overrides(ctx context.Context, w io.Writer) error {
	dir, err := os.MkdirTemp("", "notifications-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	db, err := invoicing.OpenSQLite(filepath.Join(dir, "notify.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	// Storage: notify/sqlstore over a sqlkit executor. Migrate is for
	// development; a host applies Schema() and EmailSchema() through its own
	// migration tool and verifies at startup.
	executor, err := stdsqlexec.New(db, sqlkit.SQLite)
	if err != nil {
		return fmt.Errorf("new executor: %w", err)
	}

	store, err := notifysql.New(executor)
	if err != nil {
		return fmt.Errorf("new notification store: %w", err)
	}

	for _, step := range []func(context.Context) error{
		store.Migrate, store.MigrateEmail, store.VerifySchema, store.VerifyEmailSchema,
	} {
		if err := step(ctx); err != nil {
			return fmt.Errorf("notification schema: %w", err)
		}
	}

	fmt.Fprintln(w, "notification schema, email table included, applied and verified on SQLite")

	invoices := invoicing.NewMemoryRepository()
	for _, id := range []string{"INV-43", "INV-44"} {
		if err := invoices.Save(ctx, invoicing.Invoice{ID: id, Supplier: "Acme Paper", Amount: 1299}); err != nil {
			return err
		}
	}

	// Links and titles: the task link points at the host's own web application,
	// and the title reads the invoice from the host's records.
	a, err := newApp(ctx, store, []tasknotify.Option{
		tasknotify.WithTaskLinkTemplate("/app/tasks/{task.id}"),
		tasknotify.WithTitles(func(ctx context.Context, in tasknotify.DraftInput) (string, error) {
			invoice, err := invoices.Get(ctx, in.Event.Correlation.OwnerRef)
			if err != nil {
				return "", err
			}

			return fmt.Sprintf("Approve %s from %s (%d)", invoice.ID, invoice.Supplier, invoice.Amount), nil
		}),
	})
	if err != nil {
		return err
	}
	defer a.close()

	approval, err := a.createApproval(ctx, "INV-43")
	if err != nil {
		return err
	}

	a.names.Name(string(approval), "approval-43")

	if err := a.relayPass(ctx, w, "approval for INV-43 created"); err != nil {
		return err
	}

	offers, err := a.list(ctx, w, invoicing.Alice)
	if err != nil {
		return err
	}

	printLinks(w, a.names, "alice", offers[0])

	if err := a.supervisorStream(ctx, w); err != nil {
		return err
	}

	if err := a.email(ctx, w); err != nil {
		return err
	}

	if err := a.retention(ctx, w); err != nil {
		return err
	}

	return a.count(ctx, w, invoicing.Alice)
}

// releaseAndDelegate follows one task through the default rules for a release
// and two delegations. A release retires the "taken" notices and offers the task
// again to everyone but the releaser; a delegation tells the new holder, and
// closes the previous holder's assignment as reassigned.
func releaseAndDelegate(ctx context.Context, w io.Writer) error {
	a, err := newApp(ctx, notify.NewMemoryStore(), nil)
	if err != nil {
		return err
	}
	defer a.close()

	id, err := a.createApproval(ctx, "INV-50")
	if err != nil {
		return err
	}

	if err := a.settle(ctx); err != nil {
		return err
	}

	claim := func(actor string) operation {
		return func(ctx context.Context) (hmntsk.Result, error) {
			return a.engine.Claim(ctx, hmntsk.TaskRequest{TaskID: id, Actor: actor})
		}
	}

	delegate := func(from, to string) operation {
		return func(ctx context.Context) (hmntsk.Result, error) {
			return a.engine.Delegate(ctx, hmntsk.DelegateRequest{
				TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: from},
				Target:      to,
			})
		}
	}

	if err := a.then(ctx, claim(invoicing.Alice), func(ctx context.Context) (hmntsk.Result, error) {
		return a.engine.Release(ctx, hmntsk.TaskRequest{TaskID: id, Actor: invoicing.Alice})
	}); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-50: alice claimed it, then released it")

	if err := a.printInbox(ctx, w, notify.ListQuery{Recipient: invoicing.Bob}); err != nil {
		return err
	}

	if err := a.then(ctx,
		claim(invoicing.Bob),
		delegate(invoicing.Bob, invoicing.Alice),
		delegate(invoicing.Alice, invoicing.Bob),
	); err != nil {
		return err
	}

	fmt.Fprintln(w, "bob claimed it and delegated it to alice; alice delegated it back to bob")

	for _, recipient := range []string{invoicing.Alice, invoicing.Bob} {
		if err := a.printInbox(ctx, w, notify.ListQuery{Recipient: recipient}); err != nil {
			return err
		}
	}

	return nil
}

// widening lets the sweeper widen an overdue approval to the finance managers.
// The default rules offer it again with coalescing drafts, so alice and bob,
// who already hold an open offer, are not offered it twice; carol is new.
func widening(ctx context.Context, w io.Writer) error {
	a, err := newApp(ctx, notify.NewMemoryStore(), nil)
	if err != nil {
		return err
	}
	defer a.close()

	if _, err := a.createApproval(ctx, "INV-51"); err != nil {
		return err
	}

	if err := a.settle(ctx); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-51 created; alice and bob were offered it")

	later := invoicing.ApproveDeadline + time.Minute
	a.clock.Advance(later)

	sweeper, err := hmntsk.NewSweeper(a.engine)
	if err != nil {
		return fmt.Errorf("new sweeper: %w", err)
	}

	if _, err := sweeper.Sweep(ctx); err != nil {
		return fmt.Errorf("sweep: %w", err)
	}

	if err := a.settle(ctx); err != nil {
		return err
	}

	fmt.Fprintf(w, "%s later, the sweeper widened it to %s\n", later, invoicing.GroupManagers)

	parts := make([]string, 0, 3)

	for _, recipient := range []string{invoicing.Alice, invoicing.Bob, invoicing.Carol} {
		page, err := a.notifier.List(ctx, notify.ListQuery{
			Recipient: recipient,
			States:    []notify.State{notify.StateActive},
			Kinds:     []string{tasknotify.KindOffer},
		})
		if err != nil {
			return fmt.Errorf("list %s: %w", recipient, err)
		}

		parts = append(parts, fmt.Sprintf("%s %d", recipient, len(page.Notifications)))
	}

	fmt.Fprintf(w, "open offers: %s\n", strings.Join(parts, ", "))

	return nil
}

// kindDone is a kind of the host's own, which the default rules never publish.
const kindDone = "done"

// hostRules extends the default rules and narrows the closing statuses.
//
// The rules tell whoever created a task when it is completed: a host derives
// its rules from DefaultRules.Plan and appends steps. The closing statuses leave
// out FAILED, so a failed approval's notifications stay open for someone to
// pick the work up again.
func hostRules(ctx context.Context, w io.Writer) error {
	rules := tasknotify.RulesFunc(func(ctx context.Context, in tasknotify.Input) (tasknotify.Plan, error) {
		plan, err := tasknotify.DefaultRules.Plan(ctx, in)
		if err != nil || in.Event.Type != hmntsk.EventTypeCompleted {
			return plan, err
		}

		draft, err := in.Draft(ctx, in.Event.CreatedBy, kindDone)
		if err != nil {
			return tasknotify.Plan{}, err
		}

		draft.Title = in.Event.Correlation.OwnerRef + " is done"
		plan.Steps = append(plan.Steps, tasknotify.Step{Publish: []notify.Draft{draft}})

		return plan, nil
	})

	a, err := newApp(ctx, notify.NewMemoryStore(), []tasknotify.Option{
		tasknotify.WithRules(rules),
		tasknotify.WithClosingStatuses(hmntsk.StatusCompleted, hmntsk.StatusExited),
	})
	if err != nil {
		return err
	}
	defer a.close()

	completed, err := a.createApproval(ctx, "INV-52")
	if err != nil {
		return err
	}

	if err := a.settle(ctx); err != nil {
		return err
	}

	if err := a.then(ctx, a.work(completed, invoicing.Alice, func(ctx context.Context, req hmntsk.TaskRequest) (hmntsk.Result, error) {
		return a.engine.Complete(ctx, hmntsk.CompleteRequest{
			TaskRequest: req,
			Output:      []byte(`{"approved":true,"reason":"within-budget"}`),
		})
	})...); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-52 completed by alice")

	if err := a.printInbox(ctx, w, notify.ListQuery{Recipient: "billing-service"}); err != nil {
		return err
	}

	failed, err := a.createApproval(ctx, "INV-53")
	if err != nil {
		return err
	}

	if err := a.settle(ctx); err != nil {
		return err
	}

	if err := a.then(ctx, a.work(failed, invoicing.Alice, a.engine.Fail)...); err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-53 started by alice, then failed")

	return a.printInbox(ctx, w, notify.ListQuery{
		Recipient: invoicing.Bob,
		Subject:   string(failed),
		States:    []notify.State{notify.StateActive},
	})
}

// emailOffersOnly emails offers and nothing else, and has no address for bob.
// Neither is an error: the dispatcher records both as skipped and reports them.
func emailOffersOnly(ctx context.Context, w io.Writer) error {
	a, err := newApp(ctx, notify.NewMemoryStore(), nil)
	if err != nil {
		return err
	}
	defer a.close()

	if _, err := a.createApproval(ctx, "INV-54"); err != nil {
		return err
	}

	taken, err := a.createApproval(ctx, "INV-55")
	if err != nil {
		return err
	}

	if err := a.settle(ctx); err != nil {
		return err
	}

	// bob claims INV-55, which tells alice it was taken: a notification that is
	// not an offer.
	if err := a.then(ctx, func(ctx context.Context) (hmntsk.Result, error) {
		return a.engine.Claim(ctx, hmntsk.TaskRequest{TaskID: taken, Actor: invoicing.Bob})
	}); err != nil {
		return err
	}

	book := notify.AddressBookFunc(func(_ context.Context, recipient string) (string, bool, error) {
		if recipient == invoicing.Alice {
			return "alice@example.test", true, nil
		}

		return "", false, nil
	})

	result, box, err := a.sendEmail(ctx, book, notify.WithEmailKinds(tasknotify.KindOffer))
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%s later, email pass: messages %d, sent %d, skipped for no address %d, skipped by filter %d\n",
		emailDelay, result.Messages, result.Sent, result.SkippedNoAddress, result.SkippedFiltered)

	box.print(w)

	return nil
}

// retentionDefaults runs a pruner with no options, whose age bound deletes only
// what was read or closed long enough ago, and then a count bound under the
// default strategy, EvictOldestActive, which does evict unread notifications.
func retentionDefaults(ctx context.Context, w io.Writer) error {
	a, err := newApp(ctx, notify.NewMemoryStore(), nil)
	if err != nil {
		return err
	}
	defer a.close()

	read, err := a.createApproval(ctx, "INV-56")
	if err != nil {
		return err
	}

	if _, err := a.createApproval(ctx, "INV-57"); err != nil {
		return err
	}

	if err := a.settle(ctx); err != nil {
		return err
	}

	if err := a.markRead(ctx, notify.ListQuery{Recipient: invoicing.Alice, Subject: string(read)}); err != nil {
		return err
	}

	fmt.Fprintln(w, "alice read her INV-56 offer; INV-57 was offered too")

	a.clock.Advance(91 * 24 * time.Hour)

	defaults, err := notify.NewPruner(a.notifier)
	if err != nil {
		return fmt.Errorf("new pruner: %w", err)
	}

	aged, err := defaults.Prune(ctx)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}

	fmt.Fprintf(w, "91 days later, prune with the defaults: inactive deleted for age %d, active evicted %d\n",
		aged.DeletedForAge, aged.EvictedActive)

	bounded, err := notify.NewPruner(a.notifier, notify.WithMaxPerRecipient(1))
	if err != nil {
		return fmt.Errorf("new pruner: %w", err)
	}

	counted, err := bounded.Prune(ctx)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}

	fmt.Fprintf(w, "prune, at most 1 per recipient, default strategy: inactive deleted %d, active evicted %d %v\n",
		counted.DeletedForAge+counted.DeletedForCount, counted.EvictedActive, counted.Recipients)

	return nil
}

// app is one engine, one notification service, and everything between them.
type app struct {
	engine   *hmntsk.Service
	notifier *notify.Service
	relay    *relay.Relay
	hub      *notify.Hub
	clock    *demo.Clock
	names    *demo.Names
	base     string
	closers  []func()
}

func newApp(ctx context.Context, store notify.Store, projectorOpts []tasknotify.Option) (_ *app, err error) {
	a := &app{clock: demo.NewClock(start), names: demo.NewNames()}

	engine, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithClock(a.clock),
	)
	if err != nil {
		return nil, fmt.Errorf("new engine: %w", err)
	}

	if err := invoicing.Register(engine); err != nil {
		return nil, err
	}

	// The engine and the notifier share a clock, as a host passes its engine
	// clock unchanged.
	notifier, err := notify.New(store, notify.WithClock(a.clock))
	if err != nil {
		return nil, fmt.Errorf("new notifier: %w", err)
	}

	projector, err := tasknotify.New(engine, notifier, projectorOpts...)
	if err != nil {
		return nil, fmt.Errorf("new projector: %w", err)
	}

	// The projector rides the relay, so a notification an event should produce
	// survives a crash: the event stays in the outbox until the projector
	// accepts it.
	r, err := relay.NewRelay(engine, relay.WithSinks(projector))
	if err != nil {
		return nil, fmt.Errorf("new relay: %w", err)
	}

	// The in-process broadcaster reaches only this process. More than one
	// instance needs notify/redis or notify/nats.
	hub, err := notify.NewHub(notifier.Broadcaster())
	if err != nil {
		return nil, fmt.Errorf("new hub: %w", err)
	}

	a.engine, a.notifier, a.relay, a.hub = engine, notifier, r, hub

	// From here on something is running, so a failure stops it again.
	defer func() {
		if err != nil {
			a.close()
		}
	}()

	// Assigned, not declared: the deferred cleanup reads the named result.
	err = a.runHub(ctx)
	if err != nil {
		return nil, err
	}

	handler, err := a.handler()
	if err != nil {
		return nil, err
	}

	base, stop, err := demo.Serve(handler)
	if err != nil {
		return nil, err
	}

	a.base = base
	a.closers = append(a.closers, stop)

	return a, nil
}

// runHub starts the hub and waits until streams can subscribe.
func (a *app) runHub(ctx context.Context) error {
	stop, err := demo.RunHub(ctx, a.hub, 5*time.Second)
	if err != nil {
		return fmt.Errorf("start hub: %w", err)
	}

	a.closers = append(a.closers, stop)

	return nil
}

// handler serves the notification contract. WithActor is required: notify
// authenticates nobody. The header stands for your authentication middleware.
func (a *app) handler(opts ...notify.HandlerOption) (http.Handler, error) {
	opts = append([]notify.HandlerOption{notify.WithActor(demo.NotifyActor)}, opts...)

	handler, err := notify.NewHandler(a.notifier, a.hub, opts...)
	if err != nil {
		return nil, fmt.Errorf("new handler: %w", err)
	}

	return handler, nil
}

// close stops everything newApp started, last started first.
func (a *app) close() {
	for _, closer := range slices.Backward(a.closers) {
		closer()
	}

	a.closers = nil
}

func (a *app) createApproval(ctx context.Context, invoice string) (hmntsk.TaskID, error) {
	created, err := a.engine.Create(ctx, hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: invoice, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoice, invoicing.ActivityApprove),
	})
	if err != nil {
		return "", fmt.Errorf("create %s: %w", invoice, err)
	}

	return created.Task.ID, nil
}

// relayPass runs the relay once, as relay.Run does on each tick.
func (a *app) relayPass(ctx context.Context, w io.Writer, what string) error {
	result, err := a.relay.Relay(ctx)
	if err != nil {
		return fmt.Errorf("relay: %w", err)
	}

	fmt.Fprintf(w, "%s; relay pass: delivered %d\n", what, result.Delivered)

	return nil
}

// settle runs one relay pass without printing it.
func (a *app) settle(ctx context.Context) error {
	if _, err := a.relay.Relay(ctx); err != nil {
		return fmt.Errorf("relay: %w", err)
	}

	return nil
}

// operation is one engine call on a task.
type operation func(context.Context) (hmntsk.Result, error)

// then runs each operation, a second apart, and projects its events after it,
// as the relay would within a second.
func (a *app) then(ctx context.Context, ops ...operation) error {
	for _, op := range ops {
		if _, err := op(ctx); err != nil {
			return err
		}

		a.clock.Advance(time.Second)

		if err := a.settle(ctx); err != nil {
			return err
		}
	}

	return nil
}

// work is claiming, starting and then finishing a task as one actor.
func (a *app) work(
	id hmntsk.TaskID,
	actor string,
	finish func(context.Context, hmntsk.TaskRequest) (hmntsk.Result, error),
) []operation {
	req := hmntsk.TaskRequest{TaskID: id, Actor: actor}

	return []operation{
		func(ctx context.Context) (hmntsk.Result, error) { return a.engine.Claim(ctx, req) },
		func(ctx context.Context) (hmntsk.Result, error) { return a.engine.Start(ctx, req) },
		func(ctx context.Context) (hmntsk.Result, error) { return finish(ctx, req) },
	}
}

// supervisorStream shows the stream's default policy, self-only, and a host
// policy that lets a team lead follow the members of their team.
func (a *app) supervisorStream(ctx context.Context, w io.Writer) error {
	team := map[string][]string{invoicing.Carol: {invoicing.Alice, invoicing.Bob}}

	supervisors := notify.SubscriptionAuthorizerFunc(func(ctx context.Context, actor, recipient string) error {
		if slices.Contains(team[actor], recipient) {
			return nil
		}

		return notify.SelfOnly.AuthorizeSubscription(ctx, actor, recipient)
	})

	handler, err := a.handler(notify.WithSubscriptionAuthorizer(supervisors))
	if err != nil {
		return err
	}

	supervised, stop, err := demo.Serve(handler)
	if err != nil {
		return err
	}
	defer stop()

	for _, attempt := range []struct{ base, policy string }{
		{a.base, "under the default policy"},
		{supervised, "under a supervisor policy"},
	} {
		stream, err := a.openStream(ctx, invoicing.Carol, invoicing.Bob, attempt.base)
		if err != nil {
			return err
		}

		fmt.Fprintf(w, "carol GET /v1/notifications/stream?recipient=bob → %d %s\n", stream.status, attempt.policy)
		stream.close()
	}

	return nil
}

// email sends each recipient one message about notifications they have not
// read. Addresses, rendering and sending are the host's; the dispatcher's
// defaults, a five-minute grace delay among them, are kept.
func (a *app) email(ctx context.Context, w io.Writer) error {
	addresses := map[string]string{
		invoicing.Alice: "alice@example.test",
		invoicing.Bob:   "bob@example.test",
	}

	book := notify.AddressBookFunc(func(_ context.Context, recipient string) (string, bool, error) {
		address, ok := addresses[recipient]

		return address, ok, nil
	})

	result, box, err := a.sendEmail(ctx, book)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%s later, email pass: messages %d, notifications sent %d\n", emailDelay, result.Messages, result.Sent)

	box.print(w)

	return nil
}

// sendEmail runs one email pass once the grace delay has passed, with the
// invoice template and a mailbox that keeps what it is sent. A host runs
// EmailDispatcher.Run on an interval, with a mailer that really sends.
func (a *app) sendEmail(
	ctx context.Context,
	book notify.AddressBook,
	opts ...notify.EmailOption,
) (notify.DispatchResult, *mailbox, error) {
	box := &mailbox{}

	dispatcher, err := notify.NewEmailDispatcher(a.notifier, box.mailer(), book, invoiceEmailTemplate(), opts...)
	if err != nil {
		return notify.DispatchResult{}, nil, fmt.Errorf("new dispatcher: %w", err)
	}

	a.clock.Advance(emailDelay)

	result, err := dispatcher.Dispatch(ctx)
	if err != nil {
		return notify.DispatchResult{}, nil, fmt.Errorf("dispatch: %w", err)
	}

	return result, box, nil
}

// retention bounds how many notifications each person keeps. RetainActive
// never deletes an unread one; the default, EvictOldestActive, would have
// evicted alice's oldest unread offer here.
func (a *app) retention(ctx context.Context, w io.Writer) error {
	if err := a.markRead(ctx, notify.ListQuery{
		Recipient: invoicing.Bob,
		States:    []notify.State{notify.StateActive},
	}); err != nil {
		return err
	}

	if _, err := a.createApproval(ctx, "INV-44"); err != nil {
		return err
	}

	if err := a.relayPass(ctx, w, "bob read his offer; approval for INV-44 created"); err != nil {
		return err
	}

	pruner, err := notify.NewPruner(a.notifier,
		notify.WithMaxPerRecipient(1),
		notify.WithRetentionStrategy(notify.RetainActive),
	)
	if err != nil {
		return fmt.Errorf("new pruner: %w", err)
	}

	// A host runs pruner.Run on an interval; one pass is enough here.
	result, err := pruner.Prune(ctx)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}

	fmt.Fprintf(w, "prune, RetainActive, at most 1 per recipient: inactive deleted %d, active evicted %d\n",
		result.DeletedForCount+result.DeletedForAge, result.EvictedActive)

	return nil
}

// markRead marks read, as its recipient, every notification a query lists.
func (a *app) markRead(ctx context.Context, q notify.ListQuery) error {
	page, err := a.notifier.List(ctx, q)
	if err != nil {
		return fmt.Errorf("list %s: %w", q.Recipient, err)
	}

	for _, n := range page.Notifications {
		if _, err := a.notifier.MarkRead(ctx, q.Recipient, n.ID); err != nil {
			return fmt.Errorf("mark read: %w", err)
		}
	}

	return nil
}

// printInbox prints what a query lists through the Go API, newest first.
func (a *app) printInbox(ctx context.Context, w io.Writer, q notify.ListQuery) error {
	page, err := a.notifier.List(ctx, q)
	if err != nil {
		return fmt.Errorf("list %s: %w", q.Recipient, err)
	}

	fmt.Fprintf(w, "%s: %s\n", q.Recipient, describeAll(page.Notifications))

	return nil
}

// list prints a person's notifications over HTTP, newest first.
func (a *app) list(ctx context.Context, w io.Writer, actor string) ([]notify.Notification, error) {
	var body struct {
		Notifications []notify.Notification `json:"notifications"`
	}

	status, err := a.do(ctx, http.MethodGet, actor, "/v1/notifications", &body)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(w, "%s GET /v1/notifications → %d %s\n", actor, status, describeAll(body.Notifications))

	if len(body.Notifications) == 0 {
		return nil, errors.New("no notifications listed")
	}

	return body.Notifications, nil
}

func (a *app) count(ctx context.Context, w io.Writer, actor string) error {
	var body struct {
		Count int64 `json:"count"`
	}

	status, err := a.do(ctx, http.MethodGet, actor, "/v1/notifications/count", &body)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%s GET /v1/notifications/count → %d count=%d\n", actor, status, body.Count)

	return nil
}

func (a *app) post(ctx context.Context, w io.Writer, actor, path string) error {
	status, err := a.do(ctx, http.MethodPost, actor, path, nil)
	if err != nil {
		return err
	}

	fmt.Fprintln(w, a.names.Mask(fmt.Sprintf("%s POST %s → %d", actor, path, status)))

	return nil
}

func (a *app) do(ctx context.Context, method, actor, path string, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set(demo.ActorHeader, actor)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			return 0, fmt.Errorf("decode %s: %w", path, err)
		}
	}

	return resp.StatusCode, nil
}

// describeAll describes notifications in the order given.
func describeAll(notifications []notify.Notification) string {
	parts := make([]string, 0, len(notifications))
	for _, n := range notifications {
		parts = append(parts, describe(n))
	}

	return strings.Join(parts, ", ")
}

// describe names a notification: a closed one by why it closed, any other by
// its title.
func describe(n notify.Notification) string {
	if n.State == notify.StateClosed {
		return fmt.Sprintf("%s %s %s", n.Kind, n.State, n.ClosedReason)
	}

	return fmt.Sprintf("%s %s %q", n.Kind, n.State, n.Title)
}

func printLinks(w io.Writer, names *demo.Names, who string, n notify.Notification) {
	relations := slices.Sorted(maps.Keys(n.Links))

	parts := make([]string, 0, len(relations))
	for _, relation := range relations {
		parts = append(parts, relation+" "+n.Links[relation])
	}

	fmt.Fprintln(w, names.Mask(fmt.Sprintf("%s's links: %s", who, strings.Join(parts, ", "))))
}

// invoiceEmailTemplate renders one recipient's batch: a count in the subject,
// a title per line in the body.
func invoiceEmailTemplate() notify.EmailTemplate {
	return notify.EmailTemplateFunc(func(_ context.Context, batch notify.EmailBatch) (notify.EmailContent, error) {
		noun := "tasks"
		if len(batch.Notifications) == 1 {
			noun = "task"
		}

		titles := make([]string, 0, len(batch.Notifications))
		for _, n := range batch.Notifications {
			titles = append(titles, n.Title)
		}

		return notify.EmailContent{
			Subject:  fmt.Sprintf("%d invoice %s waiting", len(batch.Notifications), noun),
			TextBody: strings.Join(titles, "\n"),
		}, nil
	})
}

// mailbox is a mailer that keeps what it is given. A real mailer calls an SMTP
// server or a mail API.
type mailbox struct {
	mu   sync.Mutex
	sent []notify.EmailMessage
}

func (m *mailbox) mailer() notify.Mailer {
	return notify.MailerFunc(func(_ context.Context, message notify.EmailMessage) error {
		m.mu.Lock()
		defer m.mu.Unlock()

		m.sent = append(m.sent, message)

		return nil
	})
}

// print prints the messages sent, ordered by address.
func (m *mailbox) print(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	slices.SortFunc(m.sent, func(x, y notify.EmailMessage) int { return strings.Compare(x.To, y.To) })

	for _, message := range m.sent {
		fmt.Fprintf(w, "mail to %s: %q %s\n", message.To, message.Subject, message.TextBody)
	}
}

// stream is a client of the server-sent event stream, as a browser's
// EventSource is. It reacts to a signal by re-reading, never by trusting the
// signal's content: signals are best effort, the store is the truth.
type stream struct {
	status int
	resp   *http.Response
	lines  *bufio.Reader
	cancel context.CancelFunc
}

func (a *app) openStream(ctx context.Context, actor, recipient, base string) (*stream, error) {
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)

	url := base + "/v1/notifications/stream"
	if recipient != actor {
		url += "?recipient=" + recipient
	}

	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, url, http.NoBody)
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

	return &stream{status: resp.StatusCode, resp: resp, lines: bufio.NewReader(resp.Body), cancel: cancel}, nil
}

func (s *stream) expectConnected() error {
	line, err := s.lines.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read stream: %w", err)
	}

	if strings.TrimSpace(line) != ": connected" {
		return fmt.Errorf("stream opened with %q", line)
	}

	return nil
}

// nextChange skips heartbeats and returns the next signal's change.
func (s *stream) nextChange() (notify.Change, error) {
	for {
		line, err := s.lines.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("read stream: %w", err)
		}

		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}

		var signal struct {
			Change notify.Change `json:"change"`
		}

		if err := json.Unmarshal([]byte(data), &signal); err != nil {
			return "", fmt.Errorf("decode signal: %w", err)
		}

		return signal.Change, nil
	}
}

func (s *stream) close() {
	s.cancel()
	_ = s.resp.Body.Close()
}
