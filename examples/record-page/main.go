// Command record-page shows every task about one invoice, as an invoice's own
// page would, and who may read a single task over HTTP.
//
// A record page lists by correlation. The host has already decided the viewer
// may see the invoice, so it lists server-side through the Go API; over HTTP
// the default query policy only serves a caller their own inbox. Reading one
// task over HTTP is allowed to the people taking part in it, unless the host
// says otherwise.
//
//	go run ./record-page
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
	httptransport "github.com/kartaladev/hmntsk/transport/http"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "record-page:", err)
		os.Exit(1)
	}
}

const creator = "billing-service"

func run(ctx context.Context, w io.Writer) error {
	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithClock(demo.NewClock(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC))),
	)
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}

	if err := invoicing.Register(svc); err != nil {
		return err
	}

	names := demo.NewNames()

	approval, err := seed(ctx, w, svc, names)
	if err != nil {
		return err
	}

	demo.Default(w, "every task for one invoice, over the Go API")

	for _, invoice := range []string{"INV-42", "INV-43"} {
		page, err := svc.Query(ctx, hmntsk.Query{OwnerType: invoicing.OwnerType, OwnerRef: invoice})
		if err != nil {
			return fmt.Errorf("query %s: %w", invoice, err)
		}

		fmt.Fprintf(w, "%s: %s\n", invoice, describe(page.Tasks, true))
	}

	task := "/v1/tasks/" + string(approval)

	demo.Default(w, "participants-only reads over HTTP")

	if err := overHTTP(ctx, w, svc, names, nil, []request{
		// A list must name the caller's own inbox; a bare correlation filter is
		// everybody's tasks.
		{invoicing.Alice, "/v1/tasks?ownerType=invoice&ownerRef=INV-42"},
		{invoicing.Alice, "/v1/tasks?candidate=me&status=READY&ownerType=invoice&ownerRef=INV-42"},
		{invoicing.Alice, task}, // eligible: a member of the candidate group
		{creator, task},         // created it
		{invoicing.Dave, task},  // takes no part in it
		{invoicing.Dave, task + "/history"},
		{"", task}, // no acting user: refused before the lookup
		{invoicing.Alice, "/v1/tasks/no-such-task"},
	}); err != nil {
		return err
	}

	demo.Override(w, "auditors may read any task")

	auditors, err := svc.ResolveCandidates(ctx, hmntsk.CandidatePool{Groups: []string{invoicing.GroupAuditors}})
	if err != nil {
		return fmt.Errorf("resolve auditors: %w", err)
	}

	// The policy replaces the default wholesale, so it hands everyone who is not
	// an auditor back to the default.
	auditorsMayRead := transportcore.TaskReadAuthorizerFunc(
		func(ctx context.Context, read transportcore.TaskRead) error {
			if slices.Contains(auditors, read.Actor) {
				return nil
			}

			return transportcore.ParticipantsOnly.AuthorizeRead(ctx, read)
		})

	return overHTTP(ctx, w, svc, names, []transportcore.Option{transportcore.WithTaskReadAuthorizer(auditorsMayRead)}, []request{
		{invoicing.Dave, task},
		{invoicing.Dave, task + "/history"},
		{invoicing.Carol, task},
		{"", task}, // never served without an acting user, whatever the policy
	})
}

// seed gives INV-42 a finished review and a waiting approval, and INV-43 a
// waiting approval. It returns INV-42's approval.
func seed(ctx context.Context, w io.Writer, svc *hmntsk.Service, names *demo.Names) (hmntsk.TaskID, error) {
	review, err := create(ctx, svc, invoicing.ReviewType, invoicing.ActivityReview, "INV-42")
	if err != nil {
		return "", err
	}

	request := hmntsk.TaskRequest{TaskID: review, Actor: invoicing.Bob}

	if _, err := svc.Claim(ctx, request); err != nil {
		return "", fmt.Errorf("claim: %w", err)
	}

	if _, err := svc.Start(ctx, request); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}

	if _, err := svc.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: request,
		Output:      []byte(`{"matchesOrder":true}`),
	}); err != nil {
		return "", fmt.Errorf("complete: %w", err)
	}

	approval, err := create(ctx, svc, invoicing.ApproveType, invoicing.ActivityApprove, "INV-42")
	if err != nil {
		return "", err
	}

	names.Name(string(approval), "approval-42")

	if _, err := create(ctx, svc, invoicing.ApproveType, invoicing.ActivityApprove, "INV-43"); err != nil {
		return "", err
	}

	fmt.Fprintln(w, "seeded INV-42 (review completed by bob, approval waiting) and INV-43 (approval waiting)")

	return approval, nil
}

func create(ctx context.Context, svc *hmntsk.Service, taskType, activity, invoice string) (hmntsk.TaskID, error) {
	created, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:        taskType,
		Actor:       creator,
		Input:       invoicing.Input(invoicing.Invoice{ID: invoice, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoice, activity),
	})
	if err != nil {
		return "", fmt.Errorf("create %s for %s: %w", taskType, invoice, err)
	}

	return created.Task.ID, nil
}

type request struct {
	actor string // empty: no acting user
	path  string
}

func overHTTP(
	ctx context.Context,
	w io.Writer,
	svc *hmntsk.Service,
	names *demo.Names,
	opts []transportcore.Option,
	requests []request,
) error {
	api, err := transportcore.New(svc, opts...)
	if err != nil {
		return fmt.Errorf("new api: %w", err)
	}

	// The header stands for your authentication middleware.
	handler, err := httptransport.Handler(api, httptransport.WithActorFunc(demo.Actor))
	if err != nil {
		return fmt.Errorf("new handler: %w", err)
	}

	base, stop, err := demo.Serve(handler)
	if err != nil {
		return err
	}
	defer stop()

	for _, r := range requests {
		line, err := get(ctx, base, r)
		if err != nil {
			return err
		}

		fmt.Fprintln(w, names.Mask(line))
	}

	return nil
}

func get(ctx context.Context, base string, r request) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+r.path, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}

	actor := "nobody"
	if r.actor != "" {
		actor = r.actor
		req.Header.Set(demo.ActorHeader, r.actor)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", r.path, err)
	}
	defer resp.Body.Close()

	line := fmt.Sprintf("%s GET %s → %d", actor, r.path, resp.StatusCode)

	if resp.StatusCode != http.StatusOK || strings.HasSuffix(r.path, "/history") {
		return line, nil
	}

	var tasks []hmntsk.Task

	if strings.HasPrefix(r.path, "/v1/tasks?") {
		var page transportcore.PageResponse
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			return "", fmt.Errorf("decode page: %w", err)
		}

		tasks = page.Tasks
	} else {
		var task transportcore.TaskResponse
		if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
			return "", fmt.Errorf("decode task: %w", err)
		}

		tasks = []hmntsk.Task{task}
	}

	return line + " " + describe(tasks, false), nil
}

// describe prints each task's type and status, and optionally who holds it.
func describe(tasks []hmntsk.Task, withAssignee bool) string {
	out := make([]string, 0, len(tasks))

	for _, task := range tasks {
		s := fmt.Sprintf("%s %s", task.Type, task.Status)
		if withAssignee && task.Assignee != "" {
			s += " " + task.Assignee
		}

		out = append(out, s)
	}

	return strings.Join(out, ", ")
}
