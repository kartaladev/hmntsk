// Command lifecycle-operations walks invoice approvals through every lifecycle
// operation hmntsk has, including the ones it refuses, and then shows how a
// candidate pool chosen for one task changes who gets it.
//
//	go run ./lifecycle-operations
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "lifecycle-operations:", err)
		os.Exit(1)
	}
}

const creator = "billing-service"

func run(ctx context.Context, w io.Writer) error {
	svc, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(invoicing.Directory()))
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}

	if err := invoicing.Register(svc); err != nil {
		return err
	}

	demo.Default(w, "every lifecycle operation, on a task the type offers to a group")

	if err := operations(ctx, w, svc); err != nil {
		return err
	}

	demo.Override(w, "a candidate pool chosen for each task")

	return pools(ctx, w, svc)
}

// operations takes one approval through claim, release, delegation, suspension,
// a stale write, the implicit start and failure, and cancels another.
func operations(ctx context.Context, w io.Writer, svc *hmntsk.Service) error {
	created, err := create(ctx, svc, "INV-1", nil)
	if err != nil {
		return err
	}

	task := created.Task
	fmt.Fprintf(w, "approval for INV-1 created: %s, offered to %s\n", task.Status, strings.Join(task.Candidates.Groups, ", "))

	as := func(actor string) hmntsk.TaskRequest { return hmntsk.TaskRequest{TaskID: task.ID, Actor: actor} }

	// Claim reserves the task; only the one holding it may release it.
	step(w, "alice claims it")(svc.Claim(ctx, as(invoicing.Alice)))
	step(w, "bob releases it")(svc.Release(ctx, as(invoicing.Bob)))
	step(w, "alice releases it")(svc.Release(ctx, as(invoicing.Alice)))
	step(w, "alice claims it again")(svc.Claim(ctx, as(invoicing.Alice)))

	// Delegation hands over the task, work included, to someone else eligible.
	step(w, "alice delegates it to carol")(svc.Delegate(ctx, hmntsk.DelegateRequest{TaskRequest: as(invoicing.Alice), Target: invoicing.Carol}))
	step(w, "alice delegates it to bob")(svc.Delegate(ctx, hmntsk.DelegateRequest{TaskRequest: as(invoicing.Alice), Target: invoicing.Bob}))

	// Only legal moves are accepted: a reserved task must be started first.
	step(w, "bob completes it before starting")(svc.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: as(invoicing.Bob),
		Output:      []byte(`{"approved":true,"reason":"within-budget"}`),
	}))

	// Suspension takes the task out of circulation and remembers where it was.
	suspended, err := svc.Suspend(ctx, as(invoicing.Bob))
	step(w, "bob suspends it")(suspended, err)
	step(w, "alice claims it while suspended")(svc.Claim(ctx, as(invoicing.Alice)))
	step(w, "bob resumes it")(svc.Resume(ctx, as(invoicing.Bob)))

	// Every write can be made conditional on the version the caller last read.
	// Bob read the task while it was suspended; it has moved on since.
	stale := as(invoicing.Bob)
	stale.Version = &suspended.Task.Version
	step(w, "bob saves progress with a stale version")(svc.SaveProgress(ctx, hmntsk.SaveProgressRequest{
		TaskRequest: stale,
		Patch:       []byte(`[{"op":"add","path":"/approved","value":true}]`),
	}))

	// The first progress save on a reserved task starts it, and produces no
	// event: nobody is waiting on a draft.
	saved, err := svc.SaveProgress(ctx, hmntsk.SaveProgressRequest{
		TaskRequest: as(invoicing.Bob),
		Patch:       []byte(`[{"op":"add","path":"/approved","value":true}]`),
	})
	if err != nil {
		return fmt.Errorf("save progress: %w", err)
	}

	fmt.Fprintf(w, "bob saves progress: %s, the first save started it, events %d\n", saved.Task.Status, len(saved.Events))

	// Failing says the work could not be done. A rejected invoice is not a
	// failure: that is a completion with approved=false.
	failing := as(invoicing.Bob)
	failing.Comment = "supplier unreachable"
	step(w, "bob fails it")(svc.Fail(ctx, failing))

	other, err := create(ctx, svc, "INV-2", nil)
	if err != nil {
		return err
	}

	cancel := hmntsk.TaskRequest{TaskID: other.Task.ID, Actor: creator, Comment: "duplicate invoice"}
	step(w, "approval for INV-2 cancelled")(svc.Cancel(ctx, cancel))
	step(w, "cancelling it again")(svc.Cancel(ctx, cancel))

	return nil
}

// pools replaces the type's default assignment for single tasks.
func pools(ctx context.Context, w io.Writer, svc *hmntsk.Service) error {
	for _, p := range []struct {
		label string
		pool  hmntsk.CandidatePool
	}{
		// Exactly one eligible actor: the task is reserved for them at once.
		{"pool of finance-managers, carol only", hmntsk.CandidatePool{Groups: []string{invoicing.GroupManagers}}},
		// Nobody eligible: the task is in error rather than silently unclaimable.
		{"pool of a group with no members", hmntsk.CandidatePool{Groups: []string{"finance-interns"}}},
		// Exclusion wins over group membership, leaving alice the only candidate.
		{"pool of finance-approvers excluding bob", hmntsk.CandidatePool{
			Groups:   []string{invoicing.GroupApprovers},
			Excluded: []string{invoicing.Bob},
		}},
	} {
		created, err := create(ctx, svc, "INV-3", &p.pool)
		if err != nil {
			return err
		}

		task := created.Task

		if task.Status == hmntsk.StatusReserved {
			fmt.Fprintf(w, "%s: %s for %s at creation\n", p.label, task.Status, task.Assignee)
		} else {
			fmt.Fprintf(w, "%s: %s, reason %q\n", p.label, task.Status, task.Reason)
		}

		if len(p.pool.Excluded) > 0 {
			eligible, err := svc.Eligible(ctx, task, invoicing.Bob)
			if err != nil {
				return fmt.Errorf("eligible: %w", err)
			}

			fmt.Fprintf(w, "bob may claim it: %t\n", eligible)
		}
	}

	return nil
}

func create(ctx context.Context, svc *hmntsk.Service, invoice string, pool *hmntsk.CandidatePool) (hmntsk.Result, error) {
	created, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       creator,
		Input:       invoicing.Input(invoicing.Invoice{ID: invoice, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoice, invoicing.ActivityApprove),
		Candidates:  pool,
	})
	if err != nil {
		return hmntsk.Result{}, fmt.Errorf("create %s: %w", invoice, err)
	}

	return created, nil
}

// step returns a printer for one operation's outcome, so that an operation's
// (Result, error) can be passed to it directly: what it did to the task, or why
// it was refused.
func step(w io.Writer, label string) func(hmntsk.Result, error) {
	return func(result hmntsk.Result, err error) {
		if err != nil {
			fmt.Fprintf(w, "%s: refused, %s\n", label, refusal(err))

			return
		}

		task := result.Task

		switch task.Status {
		case hmntsk.StatusReserved, hmntsk.StatusInProgress:
			fmt.Fprintf(w, "%s: %s, held by %s\n", label, task.Status, task.Assignee)
		case hmntsk.StatusSuspended:
			fmt.Fprintf(w, "%s: %s, will resume to %s\n", label, task.Status, task.SuspendedFrom)
		case hmntsk.StatusFailed, hmntsk.StatusExited:
			fmt.Fprintf(w, "%s: %s, reason %q\n", label, task.Status, task.Reason)
		default:
			fmt.Fprintf(w, "%s: %s\n", label, task.Status)
		}
	}
}

// refusal classifies an error by the library error it matches, never by its
// message.
func refusal(err error) string {
	var (
		conflict   *hmntsk.ConflictError
		transition *hmntsk.TransitionError
	)

	switch {
	case errors.As(err, &conflict):
		return fmt.Sprintf("conflict (expected version %d, current version %d)", conflict.Expected, conflict.Current)
	case errors.As(err, &transition):
		return fmt.Sprintf("illegal transition from %s", transition.From)
	case errors.Is(err, hmntsk.ErrUnauthorized):
		return "not authorised"
	default:
		return err.Error()
	}
}
