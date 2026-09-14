// Command schema-form does the work of an invoice approval the way a generic
// form does: from the schemas the task type serves, over HTTP.
//
// A client that knows nothing about invoices can render the form, save partial
// work, and submit, because the type carries its schemas and the engine
// validates against them. A Go host that does know its payloads can use the
// typed facade instead, and derive the schemas from its own types.
//
//	go run ./schema-form
package main

import (
	"bytes"
	"context"
	"encoding/json"
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
		fmt.Fprintln(os.Stderr, "schema-form:", err)
		os.Exit(1)
	}
}

// start is when both sections' clocks begin.
var start = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)

func run(ctx context.Context, w io.Writer) error {
	demo.Default(w, "a form built from the served schemas")

	if err := genericForm(ctx, w); err != nil {
		return err
	}

	demo.Override(w, "the typed facade, schemas derived from Go types")

	return typedFacade(ctx, w)
}

// genericForm is what a browser client does, written in Go so it can be run
// and tested: read the schema, render fields, save progress, submit.
func genericForm(ctx context.Context, w io.Writer) error {
	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithClock(demo.NewClock(start)),
	)
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}

	if err := invoicing.Register(svc); err != nil {
		return err
	}

	created, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: "INV-42", Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation("INV-42", invoicing.ActivityApprove),
	})
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	names := demo.NewNames()
	names.Name(string(created.Task.ID), "approval")

	api, err := transportcore.New(svc)
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

	client := &client{base: base, actor: invoicing.Alice, w: w, names: names}

	var spec transportcore.TaskTypeResponse
	if err := client.call(ctx, http.MethodGet, "/v1/task-types/"+invoicing.ApproveType, "", &spec); err != nil {
		return err
	}

	fields, err := formFields(spec.OutputSchema)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "form title: %s\n", spec.Title)
	fmt.Fprintf(w, "form fields: %s\n", strings.Join(fields, ", "))

	task := "/v1/tasks/" + string(created.Task.ID)

	for _, step := range []struct{ path, body string }{
		{task + "/claim", ""},
		{task + "/start", ""},
		// Progress is partial: it is patched, not replaced, and never checked
		// for completeness, so a half-filled form survives a reload.
		{task + "/progress", `{"patch":[{"op":"add","path":"/approved","value":true}]}`},
		// Completion is checked in full against the output schema.
		{task + "/complete", `{"output":{"approved":true}}`},
		{task + "/complete", `{"output":{"approved":true,"reason":"within-budget"}}`},
	} {
		if err := client.call(ctx, http.MethodPost, step.path, step.body, nil); err != nil {
			return err
		}
	}

	return nil
}

// formFields reads the top-level properties of a JSON Schema, as a minimal form
// renderer would. A real renderer handles nesting, arrays and formats.
func formFields(schema json.RawMessage) ([]string, error) {
	var doc struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}

	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}

	names := slices.Sorted(maps.Keys(doc.Properties))
	fields := make([]string, 0, len(names))

	for _, name := range names {
		property := doc.Properties[name]

		detail := property.Type
		if len(property.Enum) > 0 {
			detail += ": " + strings.Join(property.Enum, ", ")
		}

		if slices.Contains(doc.Required, name) {
			detail += "; required"
		}

		fields = append(fields, fmt.Sprintf("%s (%s)", name, detail))
	}

	return fields, nil
}

// client makes requests as one actor and prints each one.
type client struct {
	base  string
	actor string
	w     io.Writer
	names *demo.Names
}

// call prints "actor METHOD path [body] → status [summary]". A GET decodes its
// body into into; an operation prints the task's status, or the error code.
func (c *client) call(ctx context.Context, method, path, body string, into any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}

	req.Header.Set(demo.ActorHeader, c.actor)

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	line := fmt.Sprintf("%s %s %s", c.actor, method, path)
	if body != "" {
		line += " " + body
	}

	line += fmt.Sprintf(" → %d", resp.StatusCode)

	summary, err := summarise(method, path, resp.StatusCode, raw, into)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}

	if summary != "" {
		line += " " + summary
	}

	fmt.Fprintln(c.w, c.names.Mask(line))

	return nil
}

func summarise(method, path string, status int, raw []byte, into any) (string, error) {
	if status != http.StatusOK {
		var failure transportcore.ErrorResponse
		if err := json.Unmarshal(raw, &failure); err != nil {
			return "", fmt.Errorf("decode error: %w", err)
		}

		return string(failure.Error.Code), nil
	}

	if method == http.MethodGet {
		return "", json.Unmarshal(raw, into)
	}

	var task transportcore.TaskResponse
	if err := json.Unmarshal(raw, &task); err != nil {
		return "", fmt.Errorf("decode task: %w", err)
	}

	if strings.HasSuffix(path, "/progress") {
		var compact bytes.Buffer
		if err := json.Compact(&compact, task.Progress); err != nil {
			return "", fmt.Errorf("compact progress: %w", err)
		}

		return "progress " + compact.String(), nil
	}

	return string(task.Status), nil
}

// InvoiceApproval is the approval's input, as the host's own Go type.
type InvoiceApproval struct {
	InvoiceID string `json:"invoiceId"`
	Supplier  string `json:"supplier"`
	Amount    int64  `json:"amount"`
}

// ApprovalDecision is the approval's output. Note is optional because it is
// tagged omitempty; the derived schema requires every other field.
type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
	Note     string `json:"note,omitempty"`
}

const typedApproveType = "invoice.approve.typed"

// typedFacade registers a type from Go types. hmntsk derives the schemas, so
// HTTP clients and the generic form above still work on it, while Go code gets
// compile-time checked payloads.
func typedFacade(ctx context.Context, w io.Writer) error {
	// A typed completion handler is an ordinary event handler; the facade adds no
	// second delivery path. It is made from the typed handle, and the handle from
	// the service, so it is registered through WithEventHandlerFactory, which
	// hands the service to the factory before New returns: Define and
	// OnCompleted both happen inside it.
	var approvals hmntsk.Kind[InvoiceApproval, ApprovalDecision]

	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithClock(demo.NewClock(start)),
		hmntsk.WithEventHandlerFactory(func(svc *hmntsk.Service) ([]hmntsk.EventHandler, error) {
			var err error

			approvals, err = hmntsk.Define[InvoiceApproval, ApprovalDecision](svc, hmntsk.TypeSpec{
				Name:              typedApproveType,
				Title:             "Approve invoice (typed)",
				DefaultAssignment: hmntsk.CandidatePool{Groups: []string{invoicing.GroupApprovers}},
			})
			if err != nil {
				return nil, fmt.Errorf("define: %w", err)
			}

			return []hmntsk.EventHandler{
				approvals.OnCompleted(func(_ context.Context, event hmntsk.Event, decision ApprovalDecision) error {
					fmt.Fprintf(w, "typed handler: %s completed, approved=%t reason=%s\n",
						event.Correlation.OwnerRef, decision.Approved, decision.Reason)

					return nil
				}),
			}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}

	fmt.Fprintf(w, "registered %s with schemas derived from Go types\n", approvals.Name())

	spec, err := svc.Registry().Lookup(typedApproveType)
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}

	inputRequired, err := required(spec.InputSchema)
	if err != nil {
		return err
	}

	outputRequired, err := required(spec.OutputSchema)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "derived input requires %v, output requires %v\n", inputRequired, outputRequired)

	input := InvoiceApproval{InvoiceID: "INV-43", Supplier: "Acme Paper", Amount: 1299}

	created, err := approvals.Create(ctx, input, hmntsk.CreateRequest{
		Actor:       "billing-service",
		Correlation: invoicing.Correlation(input.InvoiceID, invoicing.ActivityApprove),
	})
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	fmt.Fprintf(w, "created from %+v\n", input)

	request := hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: invoicing.Bob}

	if _, err := svc.Claim(ctx, request); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	if _, err := svc.Start(ctx, request); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	decision := ApprovalDecision{Approved: false, Reason: "duplicate"}

	if _, err := approvals.Complete(ctx, decision, request); err != nil {
		return fmt.Errorf("complete: %w", err)
	}

	fmt.Fprintf(w, "completed with %+v\n", decision)

	task, err := approvals.Get(ctx, created.Task.ID)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	fmt.Fprintf(w, "read back typed: approved=%t reason=%s, amount=%d\n",
		task.Output.Approved, task.Output.Reason, task.Input.Amount)

	return nil
}

// required lists a JSON Schema's required properties, sorted.
func required(schema json.RawMessage) ([]string, error) {
	var doc struct {
		Required []string `json:"required"`
	}

	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}

	slices.Sort(doc.Required)

	return doc.Required, nil
}
