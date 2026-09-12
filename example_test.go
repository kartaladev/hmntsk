package hmntsk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// Example shows the whole wiring model and one task's life.
//
// The store is the in-memory one so that the example runs anywhere; a real host
// passes a store over its own database handle, and nothing else changes.
func Example() {
	// 1. A store. It is repository, transactor and event sink as one value, so
	//    that the three cannot be wired onto different connections.
	store := memstore.New()

	// 2. A directory. The engine ships no membership model; this one is fixed,
	//    and a real host implements the same port over whatever it already runs.
	directory := hmntsk.NewStaticAssignment(map[string][]string{
		"finance-approvers": {"alice", "bob"},
		"managers":          {"carol"},
	})

	// 3. Consumers. The engine publishes; it never calls business logic itself,
	//    so adding one of these changes nothing inside the engine.
	onCompleted := hmntsk.EventHandlerFunc(func(_ context.Context, event hmntsk.Event) error {
		if event.Type == hmntsk.EventTypeCompleted {
			fmt.Printf("consumer: %s completed for %s\n", event.TaskType, event.Correlation.OwnerRef)
		}

		return nil
	})

	svc, err := hmntsk.New(store,
		hmntsk.WithGroupResolver(directory),
		hmntsk.WithEventHandlers(onCompleted),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 4. A task type. Registration is mandatory: the schema is what an inbox
	//    renders a form from, and a typo in a type name is caught here rather
	//    than surfacing later as a task nothing can display.
	err = svc.Register(hmntsk.TypeSpec{
		Name:            "approval",
		Title:           "Spending approval",
		InputSchema:     json.RawMessage(`{"type":"object","properties":{"amount":{"type":"number"}},"required":["amount"]}`),
		OutputSchema:    json.RawMessage(`{"type":"object","properties":{"approved":{"type":"boolean"}},"required":["approved"]}`),
		DefaultDeadline: 24 * time.Hour,
		DefaultEscalation: &hmntsk.EscalationPolicy{
			Action: hmntsk.EscalationWiden, AddGroups: []string{"managers"},
		},
		DefaultAssignment: hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	created, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:  "approval",
		Actor: "purchasing-service",
		Input: json.RawMessage(`{"amount":1299}`),
		// Correlation is how a consumer routes the event back to the work that
		// asked for it. The engine never interprets these values.
		Correlation: hmntsk.CorrelationData{
			OwnerType: "purchase-order", OwnerRef: "PO-4711", ActivityKey: "approve",
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	id := created.Task.ID
	fmt.Println("created:", created.Task.Status)

	claimed, err := svc.Claim(ctx, hmntsk.TaskRequest{TaskID: id, Actor: "alice"})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("claimed:", claimed.Task.Status)

	if _, err = svc.Start(ctx, hmntsk.TaskRequest{TaskID: id, Actor: "alice"}); err != nil {
		log.Fatal(err)
	}

	// A denied approval is a completion with a negative output, never a
	// failure. Status says where the task got to; the payload says what
	// happened.
	completed, err := svc.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: "alice"},
		Output:      json.RawMessage(`{"approved":false}`),
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("completed:", completed.Task.Status)
	fmt.Println("outcome:", string(completed.Task.Output))

	// Output:
	// created: READY
	// claimed: RESERVED
	// consumer: approval completed for PO-4711
	// completed: COMPLETED
	// outcome: {"approved":false}
}

// Example_hostLedTransaction shows the engine joining a transaction the host
// began, so that a business change and a task change commit together.
//
// The engine cannot see a commit it did not perform, so it hands the event
// dispatch back: the host runs it after committing, never before.
func Example_hostLedTransaction() {
	store := memstore.New()

	svc, err := hmntsk.New(store,
		hmntsk.WithGroupResolver(hmntsk.NewStaticAssignment(map[string][]string{
			"finance-approvers": {"alice", "bob"},
		})),
		hmntsk.WithEventHandlers(hmntsk.EventHandlerFunc(
			func(_ context.Context, event hmntsk.Event) error {
				fmt.Println("consumer saw:", event.Type)

				return nil
			},
		)),
	)
	if err != nil {
		log.Fatal(err)
	}

	if err := svc.Register(hmntsk.TypeSpec{
		Name:              "approval",
		DefaultAssignment: hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
	}); err != nil {
		log.Fatal(err)
	}

	created, err := svc.Create(context.Background(), hmntsk.CreateRequest{Type: "approval"})
	if err != nil {
		log.Fatal(err)
	}

	// The host opens its own transaction. In a real adapter this is
	// sqlstore.ContextWithTx(ctx, tx) around a *sql.Tx the host began.
	ctx, done := store.ContextWithTx(context.Background())

	result, err := svc.Claim(ctx, hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: "alice"})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("dispatch pending:", result.Pending())

	// ... the host writes its own rows in the same transaction ...

	done(true)

	if err := result.Dispatch(context.Background()); err != nil {
		log.Fatal(err)
	}

	// Output:
	// consumer saw: task.created
	// dispatch pending: true
	// consumer saw: task.claimed
}

// Example_typedFacade shows the compile-time-typed path over the same engine.
func Example_typedFacade() {
	type SpendingRequest struct {
		Amount        int64  `json:"amount"`
		Justification string `json:"justification"`
	}

	type SpendingDecision struct {
		Approved bool   `json:"approved"`
		Note     string `json:"note,omitempty"`
	}

	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(hmntsk.NewStaticAssignment(map[string][]string{
			"finance-approvers": {"alice"},
		})),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Define registers the type and hands back a typed handle. The schemas are
	// derived from the Go types because this specification supplies none.
	approvals, err := hmntsk.Define[SpendingRequest, SpendingDecision](svc, hmntsk.TypeSpec{
		Name:              "approval",
		DefaultAssignment: hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	created, err := approvals.Create(ctx,
		SpendingRequest{Amount: 1299, Justification: "new laptop"},
		hmntsk.CreateRequest{Actor: "purchasing-service"},
	)
	if err != nil {
		log.Fatal(err)
	}

	id := created.Task.ID

	if _, err := svc.Start(ctx, hmntsk.TaskRequest{TaskID: id, Actor: "alice"}); err != nil {
		log.Fatal(err)
	}

	if _, err := approvals.Complete(ctx,
		SpendingDecision{Approved: true, Note: "within budget"},
		hmntsk.TaskRequest{TaskID: id, Actor: "alice"},
	); err != nil {
		log.Fatal(err)
	}

	task, err := approvals.Get(ctx, id)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("amount:", task.Input.Amount)
	fmt.Println("approved:", task.Output.Approved, task.Output.Note)

	// The untyped path still sees the same task, which is what keeps a
	// heterogeneous inbox possible.
	page, err := svc.Query(ctx, hmntsk.Query{Candidate: "alice"})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("in the inbox:", len(page.Tasks), page.Tasks[0].Type)

	// Output:
	// amount: 1299
	// approved: true within budget
	// in the inbox: 1 approval
}
