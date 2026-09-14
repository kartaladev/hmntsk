// Command inbox-buckets builds an inbox from queries: orderings, exact paging,
// bucket counts, and who may run which query over HTTP.
//
// A bucket ("mine", "available", "overdue", a team's queue) is not something
// hmntsk stores. It is a query the host names.
//
//	go run ./inbox-buckets
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
		fmt.Fprintln(os.Stderr, "inbox-buckets:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, w io.Writer) error {
	clock := demo.NewClock(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC))

	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithClock(clock),
	)
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}

	// These approvals take their due dates from the invoice, not from the type,
	// so the type has no default deadline and one of them has none at all.
	spec := invoicing.ApproveSpec()
	spec.DefaultDeadline = 0

	if err := svc.Register(spec); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	if err := seed(ctx, w, svc, clock); err != nil {
		return err
	}

	demo.Default(w, "orderings over the Go API")

	if err := orderings(ctx, w, svc); err != nil {
		return err
	}

	if err := buckets(ctx, w, svc, clock.Now()); err != nil {
		return err
	}

	demo.Default(w, "self-only queries over HTTP")

	if err := overHTTP(ctx, w, svc, nil, []request{
		{invoicing.Alice, "/v1/tasks?candidate=me&status=READY&orderBy=urgency"},
		{invoicing.Alice, "/v1/tasks/count?assignee=me"},
		{invoicing.Alice, "/v1/tasks?candidate=bob"},
		{invoicing.Carol, "/v1/tasks?group=finance-approvers"},
		{invoicing.Alice, "/v1/tasks?candidate=me&order=desc"},
	}); err != nil {
		return err
	}

	demo.Override(w, "a supervisor may read the team queue")

	return overHTTP(ctx, w, svc, []transportcore.Option{transportcore.WithQueryAuthorizer(supervisors)}, []request{
		{invoicing.Carol, "/v1/tasks?group=finance-approvers&status=READY&orderBy=urgency"},
		{invoicing.Carol, "/v1/tasks/count?group=finance-approvers"},
		{invoicing.Alice, "/v1/tasks?group=finance-approvers"},
		{invoicing.Alice, "/v1/tasks?candidate=me&status=READY"},
	})
}

// seed creates five approvals, a minute apart, with the priorities and due
// dates that make every ordering come out differently.
func seed(ctx context.Context, w io.Writer, svc *hmntsk.Service, clock *demo.Clock) error {
	type approval struct {
		invoice  string
		priority hmntsk.Priority
		due      time.Duration // zero: no deadline
	}

	approvals := []approval{
		{"INV-1", 1, 0},
		{"INV-2", 1, 24 * time.Hour},
		{"INV-3", 0, 7 * 24 * time.Hour},
		{"INV-4", 5, 2 * time.Hour},
		{"INV-5", 3, time.Hour},
	}

	ids := make(map[string]hmntsk.TaskID, len(approvals))

	for _, a := range approvals {
		req := hmntsk.CreateRequest{
			Type:        invoicing.ApproveType,
			Actor:       "billing-service",
			Input:       invoicing.Input(invoicing.Invoice{ID: a.invoice, Supplier: "Acme Paper", Amount: 100}),
			Correlation: invoicing.Correlation(a.invoice, invoicing.ActivityApprove),
			Priority:    &a.priority,
		}

		if a.due > 0 {
			due := clock.Now().Add(a.due)
			req.DueAt = &due
		}

		created, err := svc.Create(ctx, req)
		if err != nil {
			return fmt.Errorf("create %s: %w", a.invoice, err)
		}

		ids[a.invoice] = created.Task.ID

		clock.Advance(time.Minute)
	}

	if _, err := svc.Claim(ctx, hmntsk.TaskRequest{TaskID: ids["INV-4"], Actor: invoicing.Alice}); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	later := 90 * time.Minute
	clock.Advance(later)

	fmt.Fprintf(w, "seeded approvals INV-1 to INV-5 for %s; alice claimed INV-4; %s later\n",
		invoicing.GroupApprovers, later)

	return nil
}

// orderings runs the same query under each ordering, then pages through one.
func orderings(ctx context.Context, w io.Writer, svc *hmntsk.Service) error {
	for _, o := range []struct {
		label string
		query hmntsk.Query
	}{
		{"created", hmntsk.Query{}}, // the zero value: creation order
		{"priority", hmntsk.Query{OrderBy: hmntsk.OrderPriority}},
		{"due", hmntsk.Query{OrderBy: hmntsk.OrderDue}}, // no deadline sorts last
		{"urgency", hmntsk.Query{OrderBy: hmntsk.OrderUrgency}},
		{"priority descending", hmntsk.Query{OrderBy: hmntsk.OrderPriority, Descending: true}},
	} {
		page, err := svc.Query(ctx, o.query)
		if err != nil {
			return fmt.Errorf("query %s: %w", o.label, err)
		}

		fmt.Fprintf(w, "%-21s%s\n", o.label+":", refs(page.Tasks))
	}

	// Paging is exact under every ordering: each task exactly once, even while
	// tasks are being created between pages.
	query := hmntsk.Query{OrderBy: hmntsk.OrderUrgency, Limit: 2}

	var (
		pages     []string
		firstPage string
	)

	for {
		page, err := svc.Query(ctx, query)
		if err != nil {
			return fmt.Errorf("page: %w", err)
		}

		pages = append(pages, "["+refs(page.Tasks)+"]")

		if firstPage == "" {
			firstPage = page.NextCursor
		}

		if page.NextCursor == "" {
			break
		}

		query.Cursor = page.NextCursor
	}

	fmt.Fprintf(w, "urgency, 2 per page: %s\n", strings.Join(pages, " "))

	// A cursor is bound to the ordering that issued it.
	_, err := svc.Query(ctx, hmntsk.Query{OrderBy: hmntsk.OrderPriority, Limit: 2, Cursor: firstPage})
	if err == nil {
		return errors.New("a cursor from another ordering was accepted")
	}

	fmt.Fprintf(w, "an urgency cursor continued under priority: refused (%d)\n", transportcore.StatusFor(err))

	return nil
}

// buckets counts the host's named buckets for one person in a single call.
func buckets(ctx context.Context, w io.Writer, svc *hmntsk.Service, now time.Time) error {
	counts, err := svc.CountBuckets(ctx, map[string]hmntsk.Query{
		"mine":      {Assignee: invoicing.Alice, Statuses: []hmntsk.Status{hmntsk.StatusReserved, hmntsk.StatusInProgress}},
		"available": {Candidate: invoicing.Alice, Statuses: []hmntsk.Status{hmntsk.StatusReady}},
		"overdue":   {Candidate: invoicing.Alice, DueBefore: &now},
	})
	if err != nil {
		return fmt.Errorf("count buckets: %w", err)
	}

	names := slices.Sorted(maps.Keys(counts))

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
	}

	fmt.Fprintf(w, "alice's buckets: %s\n", strings.Join(parts, " "))

	return nil
}

// supervises is the host's own knowledge of who leads which team.
var supervises = map[string]string{invoicing.Carol: invoicing.GroupApprovers}

// supervisors lets a team lead read their team's queue, and leaves every other
// query to the default. A policy replaces the default wholesale, so one that
// extends it calls it.
var supervisors = transportcore.QueryAuthorizerFunc(
	func(ctx context.Context, actor string, q hmntsk.Query) error {
		if q.Group != "" && q.Candidate == "" && q.Assignee == "" && supervises[actor] == q.Group {
			return nil
		}

		return transportcore.SelfOnly.AuthorizeQuery(ctx, actor, q)
	})

type request struct {
	actor string
	path  string
}

// overHTTP serves the task API on a loopback listener and makes each request
// as its actor.
func overHTTP(
	ctx context.Context,
	w io.Writer,
	svc *hmntsk.Service,
	opts []transportcore.Option,
	requests []request,
) error {
	api, err := transportcore.New(svc, opts...)
	if err != nil {
		return fmt.Errorf("new api: %w", err)
	}

	// The demo reads the actor from a header. It stands for your authentication
	// middleware: hmntsk authenticates nobody and trusts what you establish.
	handler, err := httptransport.Handler(api, httptransport.WithActorFunc(func(r *http.Request) string {
		return r.Header.Get(demo.ActorHeader)
	}))
	if err != nil {
		return fmt.Errorf("new handler: %w", err)
	}

	base, stop, err := demo.Serve(handler)
	if err != nil {
		return err
	}
	defer stop()

	for _, r := range requests {
		if err := get(ctx, w, base, r); err != nil {
			return err
		}
	}

	return nil
}

func get(ctx context.Context, w io.Writer, base string, r request) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+r.path, http.NoBody)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}

	req.Header.Set(demo.ActorHeader, r.actor)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", r.path, err)
	}
	defer resp.Body.Close()

	line := fmt.Sprintf("%s GET %s → %d", r.actor, r.path, resp.StatusCode)

	if resp.StatusCode == http.StatusOK {
		summary, err := summarise(r.path, resp.Body)
		if err != nil {
			return fmt.Errorf("GET %s: %w", r.path, err)
		}

		line += " " + summary
	}

	fmt.Fprintln(w, line)

	return nil
}

func summarise(path string, body io.Reader) (string, error) {
	if strings.HasPrefix(path, "/v1/tasks/count") {
		var count transportcore.CountResponse
		if err := json.NewDecoder(body).Decode(&count); err != nil {
			return "", fmt.Errorf("decode count: %w", err)
		}

		return fmt.Sprintf("count=%d", count.Count), nil
	}

	var page transportcore.PageResponse
	if err := json.NewDecoder(body).Decode(&page); err != nil {
		return "", fmt.Errorf("decode page: %w", err)
	}

	return refs(page.Tasks), nil
}

// refs names tasks by the invoice they are about.
func refs(tasks []hmntsk.Task) string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.Correlation.OwnerRef)
	}

	return strings.Join(out, " ")
}
