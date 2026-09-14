// Command escalation escalates overdue invoice tasks with the sweeper, first
// under the task type's default policy and then under a policy one task
// carries itself.
//
// Nothing escalates on its own. A host constructs a sweeper and runs it,
// usually with Sweeper.Run on an interval; this scenario calls Sweep once so
// that its output is the same on every run.
//
//	go run ./escalation
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "escalation:", err)
		os.Exit(1)
	}
}

// start is when every section begins. Each section has a service and a clock of
// its own, so that nothing one section escalates is swept again by another.
var start = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

func run(ctx context.Context, w io.Writer) error {
	for i, section := range []struct {
		title string
		run   func(context.Context, io.Writer) error
	}{
		{"the type's escalation policy", typeDefault},
		{"a per-task policy, and a sweeper for approvals only", perTask},
		{"escalating directly, without the sweeper", direct},
		{"a policy that exempts work already started", exemptStarted},
		{"a cap on how often one task escalates", capped},
		{"a policy that supersedes an overdue task", supersede},
	} {
		if i == 0 {
			demo.Default(w, section.title)
		} else {
			demo.Override(w, section.title)
		}

		if err := section.run(ctx, w); err != nil {
			return err
		}
	}

	return nil
}

// typeDefault relies on invoice.approve's DefaultEscalation: an overdue approval
// widens to the finance managers, so carol, who could not claim it before, can.
func typeDefault(ctx context.Context, w io.Writer) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock)
	if err != nil {
		return err
	}

	approval, err := create(ctx, svc, invoicing.ApproveType, "INV-42", nil)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "approval for INV-42 created, due %s\n", approval.DueAt.Format("2006-01-02 15:04 MST"))

	if err := printEligible(ctx, w, svc, approval, invoicing.Carol); err != nil {
		return err
	}

	advance(w, clock, invoicing.ApproveDeadline+time.Minute)

	sweeper, err := hmntsk.NewSweeper(svc)
	if err != nil {
		return fmt.Errorf("new sweeper: %w", err)
	}

	result, err := sweep(ctx, w, sweeper)
	if err != nil {
		return err
	}

	// The escalation is an event like any other. The engine notifies nobody;
	// an event handler, the relay or tasknotify tells people.
	for _, event := range result.Events {
		fmt.Fprintf(w, "event: %s\n", event.Type)
	}

	escalated, err := svc.Get(ctx, approval.ID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	printCandidates(w, "candidates now", escalated)

	return printEligible(ctx, w, svc, escalated, invoicing.Carol)
}

// perTask gives one approval its own policy, which replaces the type's, and
// restricts the sweeper to approvals so an overdue review is left alone.
func perTask(ctx context.Context, w io.Writer) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock)
	if err != nil {
		return err
	}

	approval, err := create(ctx, svc, invoicing.ApproveType, "INV-43", &hmntsk.EscalationPolicy{
		Action:   hmntsk.EscalationWiden,
		AddUsers: []string{invoicing.Carol},
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-43 created with its own policy: widen to user carol")

	review, err := create(ctx, svc, invoicing.ReviewType, "INV-43", nil)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "review for INV-43 created, due %s\n", review.DueAt.Format("2006-01-02 15:04 MST"))

	advance(w, clock, invoicing.ReviewDeadline+time.Minute)

	// A host may run one sweeper per type, each with its own lease and
	// interval. This one only ever claims approvals.
	sweeper, err := hmntsk.NewSweeper(svc, hmntsk.WithSweepTypes(invoicing.ApproveType))
	if err != nil {
		return fmt.Errorf("new sweeper: %w", err)
	}

	if _, err := sweep(ctx, w, sweeper); err != nil {
		return err
	}

	escalated, err := svc.Get(ctx, approval.ID)
	if err != nil {
		return fmt.Errorf("get approval: %w", err)
	}

	printCandidates(w, "approval candidates now", escalated)

	untouched, err := svc.Get(ctx, review.ID)
	if err != nil {
		return fmt.Errorf("get review: %w", err)
	}

	fmt.Fprintf(w, "review escalations: %d\n", untouched.EscalationCount)

	return nil
}

// direct escalates a task that is not overdue at all. Escalate is an ordinary
// lifecycle operation: an operator's call and the sweep take the same path,
// with the same validation, history and event.
func direct(ctx context.Context, w io.Writer) error {
	svc, err := newService(demo.NewClock(start))
	if err != nil {
		return err
	}

	approval, err := create(ctx, svc, invoicing.ApproveType, "INV-44", nil)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "approval for INV-44 created, due %s, not yet overdue\n", approval.DueAt.Format("2006-01-02 15:04 MST"))

	escalated, err := svc.Escalate(ctx, hmntsk.TaskRequest{
		TaskID:  approval.ID,
		Actor:   "operations-desk",
		Comment: "supplier chased payment",
	})
	if err != nil {
		return fmt.Errorf("escalate: %w", err)
	}

	fmt.Fprintf(w, "an operator escalates it: %s\n", eventTypes(escalated.Events))
	printCandidates(w, "candidates now", escalated.Task)

	return nil
}

// exemptStarted leaves an overdue task alone once its assignee has started
// work, on the grounds that somebody is already on it. The sweep still claims
// it, and reports it as exempted.
func exemptStarted(ctx context.Context, w io.Writer) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock)
	if err != nil {
		return err
	}

	approval, err := create(ctx, svc, invoicing.ApproveType, "INV-45", &hmntsk.EscalationPolicy{
		Action:           hmntsk.EscalationWiden,
		AddGroups:        []string{invoicing.GroupManagers},
		ExemptInProgress: true,
	})
	if err != nil {
		return err
	}

	request := hmntsk.TaskRequest{TaskID: approval.ID, Actor: invoicing.Alice}

	if _, err := svc.Claim(ctx, request); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	if _, err := svc.Start(ctx, request); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	fmt.Fprintln(w, "approval for INV-45 started by alice")

	advance(w, clock, invoicing.ApproveDeadline+time.Minute)

	sweeper, err := hmntsk.NewSweeper(svc)
	if err != nil {
		return fmt.Errorf("new sweeper: %w", err)
	}

	if _, err := sweep(ctx, w, sweeper); err != nil {
		return err
	}

	after, err := svc.Get(ctx, approval.ID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	fmt.Fprintf(w, "escalations: %d, status: %s\n", after.EscalationCount, after.Status)

	return nil
}

// capped shows why a cap exists. Widening does not move the deadline, so an
// escalated task is still overdue; once the sweeper's lease on it expires, the
// next sweep escalates it again. MaxEscalations stops that.
func capped(ctx context.Context, w io.Writer) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock)
	if err != nil {
		return err
	}

	uncapped, err := create(ctx, svc, invoicing.ApproveType, "INV-46", nil)
	if err != nil {
		return err
	}

	once, err := create(ctx, svc, invoicing.ApproveType, "INV-47", &hmntsk.EscalationPolicy{
		Action:         hmntsk.EscalationWiden,
		AddGroups:      []string{invoicing.GroupManagers},
		MaxEscalations: 1,
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "approvals for INV-46 (the type's policy) and INV-47 (at most once) created")

	advance(w, clock, invoicing.ApproveDeadline+time.Minute)

	sweeper, err := hmntsk.NewSweeper(svc)
	if err != nil {
		return fmt.Errorf("new sweeper: %w", err)
	}

	if _, err := sweep(ctx, w, sweeper); err != nil {
		return err
	}

	// The lease is the back-off between escalations of one task.
	pause := hmntsk.DefaultLeaseDuration + time.Minute
	clock.Advance(pause)
	fmt.Fprintf(w, "%s later, both leases have expired and both tasks are still overdue\n", pause)

	if _, err := sweep(ctx, w, sweeper); err != nil {
		return err
	}

	first, err := svc.Get(ctx, uncapped.ID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	second, err := svc.Get(ctx, once.ID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	fmt.Fprintf(w, "escalations: INV-46 %d, INV-47 %d\n", first.EscalationCount, second.EscalationCount)

	return nil
}

// supersede closes an overdue task instead of widening it, because the work has
// been replaced rather than reassigned. The task becomes OBSOLETE and the event
// is an obsolescence, not an escalation.
func supersede(ctx context.Context, w io.Writer) error {
	clock := demo.NewClock(start)

	svc, err := newService(clock)
	if err != nil {
		return err
	}

	approval, err := create(ctx, svc, invoicing.ApproveType, "INV-48", &hmntsk.EscalationPolicy{
		Action: hmntsk.EscalationSupersede,
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "approval for INV-48 created with a policy that supersedes it")

	advance(w, clock, invoicing.ApproveDeadline+time.Minute)

	sweeper, err := hmntsk.NewSweeper(svc)
	if err != nil {
		return fmt.Errorf("new sweeper: %w", err)
	}

	result, err := sweep(ctx, w, sweeper)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "event: %s\n", eventTypes(result.Events))

	after, err := svc.Get(ctx, approval.ID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	fmt.Fprintf(w, "status now: %s\n", after.Status)

	return nil
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

func create(
	ctx context.Context,
	svc *hmntsk.Service,
	taskType, invoiceID string,
	escalation *hmntsk.EscalationPolicy,
) (hmntsk.Task, error) {
	created, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:        taskType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: invoiceID, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoiceID, invoicing.ActivityOf(taskType)),
		Escalation:  escalation,
	})
	if err != nil {
		return hmntsk.Task{}, fmt.Errorf("create %s for %s: %w", taskType, invoiceID, err)
	}

	return created.Task, nil
}

func advance(w io.Writer, clock *demo.Clock, d time.Duration) {
	clock.Advance(d)
	fmt.Fprintf(w, "%s later\n", d)
}

func sweep(ctx context.Context, w io.Writer, sweeper *hmntsk.Sweeper) (hmntsk.SweepResult, error) {
	result, err := sweeper.Sweep(ctx)
	if err != nil {
		return hmntsk.SweepResult{}, fmt.Errorf("sweep: %w", err)
	}

	fmt.Fprintf(w, "sweep: claimed %d, escalated %d, exempted %d\n", result.Claimed, result.Escalated, result.Exempted)

	return result, nil
}

func eventTypes(events []hmntsk.Event) string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, string(event.Type))
	}

	return strings.Join(types, ", ")
}

func printEligible(ctx context.Context, w io.Writer, svc *hmntsk.Service, task hmntsk.Task, actor string) error {
	eligible, err := svc.Eligible(ctx, task, actor)
	if err != nil {
		return fmt.Errorf("eligible: %w", err)
	}

	fmt.Fprintf(w, "%s may claim it: %t\n", actor, eligible)

	return nil
}

func printCandidates(w io.Writer, label string, task hmntsk.Task) {
	fmt.Fprintf(w, "%s: groups %v, users %v\n", label, task.Candidates.Groups, task.Candidates.Users)
}
