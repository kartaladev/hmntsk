// Command quickstart takes one invoice approval from creation to completion
// with nothing configured beyond what hmntsk requires.
//
// Every other scenario starts from this wiring and changes one thing.
//
//	go run ./quickstart
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "quickstart:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, w io.Writer) error {
	// 1. A store and a directory are the only two things hmntsk requires. The
	//    in-memory store runs anywhere; a real host passes store/sql, store/pgx
	//    or store/gorm over its own database and nothing else changes. The
	//    directory answers "who is in finance-approvers?" from whatever the
	//    host already runs.
	svc, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(invoicing.Directory()))
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}

	// 2. Task types are registered before use: a type carries the schemas a
	//    form is rendered from, and its defaults (who may act, the deadline).
	if err := invoicing.Register(svc); err != nil {
		return err
	}

	fmt.Fprintf(w, "registered: %s, %s\n", invoicing.ReviewType, invoicing.ApproveType)

	// 3. A task is created about one invoice. Correlation is how everything
	//    downstream finds the invoice again; the engine stores it and never
	//    interprets it.
	invoice := invoicing.Invoice{ID: "INV-42", Supplier: "Acme Paper", Amount: 1299}

	created, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoice),
		Correlation: invoicing.Correlation(invoice.ID, invoicing.ActivityApprove),
	})
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	task := created.Task
	fmt.Fprintf(w, "created %s for invoice %s: %s\n", task.Type, task.Correlation.OwnerRef, task.Status)

	// The type's default assignment offers the task to a group; the directory
	// says who that is.
	candidates, err := svc.ResolveCandidates(ctx, task.Candidates)
	if err != nil {
		return fmt.Errorf("resolve candidates: %w", err)
	}

	fmt.Fprintf(w, "candidates: %s\n", strings.Join(candidates, ", "))

	// 4. One candidate claims it, starts it and completes it. The actor is an
	//    input: over HTTP it is whoever the host's middleware authenticated.
	request := hmntsk.TaskRequest{TaskID: task.ID, Actor: invoicing.Alice}

	claimed, err := svc.Claim(ctx, request)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	fmt.Fprintf(w, "alice claimed it: %s\n", claimed.Task.Status)

	started, err := svc.Start(ctx, request)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	fmt.Fprintf(w, "alice started it: %s\n", started.Task.Status)

	// The output is validated in full against the type's output schema. A
	// rejected invoice is a completion with approved=false, never a failure:
	// the status says where the task got to, the payload what was decided.
	completed, err := svc.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: request,
		Output:      []byte(`{"approved":true,"reason":"within-budget"}`),
	})
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}

	fmt.Fprintf(w, "alice completed it: %s\n", completed.Task.Status)
	fmt.Fprintf(w, "output: %s\n", completed.Task.Output)

	return nil
}
