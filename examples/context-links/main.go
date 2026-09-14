// Command context-links links each task to the page where its work is done:
// the invoice's approval screen, not a generic task page.
//
// The link comes from the task type's metadata and the task's correlation.
// hmntsk stores the metadata and expands the template on request; it never
// decides what a link means.
//
//	go run ./context-links
package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "context-links:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, w io.Writer) error {
	names := demo.NewNames()

	demo.Default(w, "the type's hmntsk.route, expanded for each task")

	if err := wellKnownKeys(ctx, w, names); err != nil {
		return err
	}

	demo.Override(w, "host metadata keys and a host link resolver")

	return hostKeys(ctx, w, names)
}

// wellKnownKeys uses only the library's documented conventions: hmntsk.route
// for the link, hmntsk.formKey for the form, and ExpandRoute to fill the link in.
func wellKnownKeys(ctx context.Context, w io.Writer, names *demo.Names) error {
	svc, err := newService()
	if err != nil {
		return err
	}

	if err := invoicing.Register(svc); err != nil {
		return err
	}

	spec, err := svc.Registry().Lookup(invoicing.ApproveType)
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}

	template := spec.Metadata[hmntsk.MetadataRoute]
	fmt.Fprintf(w, "template: %s\n", template)
	fmt.Fprintf(w, "form key: %s\n", spec.Metadata[hmntsk.MetadataFormKey])

	plain, err := createApproval(ctx, svc, "INV-42")
	if err != nil {
		return err
	}

	names.Name(string(plain.ID), "approval-42")
	fmt.Fprintf(w, "INV-42: %s\n", names.Mask(hmntsk.ExpandRoute(template, plain)))

	// Correlation values are the host's own, and may need escaping. ExpandRoute
	// inserts them raw, because only the host knows whether the template is a
	// path, a query or something else entirely.
	awkward, err := createApproval(ctx, svc, "INV 7/B")
	if err != nil {
		return err
	}

	names.Name(string(awkward.ID), "approval-7b")
	fmt.Fprintf(w, "INV 7/B, raw: %s\n", names.Mask(hmntsk.ExpandRoute(template, awkward)))
	fmt.Fprintf(w, "INV 7/B, escaped by the host: %s\n", names.Mask(expandEscaped(template, awkward)))

	// A placeholder hmntsk does not recognise is left for the host's own pass.
	fmt.Fprintf(w, "unknown placeholders are kept: %s\n",
		hmntsk.ExpandRoute("/{tenant}/invoices/{correlation.ownerRef}", plain))

	return nil
}

// expandEscaped escapes the correlation values for the path segments they fill
// and the task ID for the query, then expands. This template puts correlation
// in the path and the task ID in the query; another template needs other
// escaping, which is why it is the host's job.
func expandEscaped(template string, task hmntsk.Task) string {
	escaped := task
	escaped.ID = hmntsk.TaskID(url.QueryEscape(string(task.ID)))
	escaped.Correlation.OwnerRef = url.PathEscape(task.Correlation.OwnerRef)
	escaped.Correlation.ActivityKey = url.PathEscape(task.Correlation.ActivityKey)

	return hmntsk.ExpandRoute(template, escaped)
}

// Host metadata keys. Anything outside the "hmntsk." prefix belongs to the host:
// the engine stores it with the type and returns it unchanged.
const (
	keyIcon     = "acme.icon"
	keyDoneLink = "acme.doneRoute"
)

// hostKeys adds the host's own metadata to the type, and replaces ExpandRoute's
// single link with a resolver that picks a link by the task's status and fills
// in a placeholder of the host's own.
func hostKeys(ctx context.Context, w io.Writer, names *demo.Names) error {
	svc, err := newService()
	if err != nil {
		return err
	}

	spec := invoicing.ApproveSpec()
	spec.Metadata[keyIcon] = "stamp"
	spec.Metadata[keyDoneLink] = "/{tenant}/invoices/{correlation.ownerRef}"

	// Metadata is part of a type's identity: registering the same type again
	// with different metadata is a conflict, so the keys go in at registration.
	if err := svc.Register(spec); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	stored, err := svc.Registry().Lookup(invoicing.ApproveType)
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}

	fmt.Fprintf(w, "%s: %s\n", keyIcon, stored.Metadata[keyIcon])

	task, err := createApproval(ctx, svc, "INV-42")
	if err != nil {
		return err
	}

	names.Name(string(task.ID), "approval-42")
	fmt.Fprintf(w, "INV-42 while %s: %s\n", task.Status, names.Mask(resolveLink(stored, task, "acme")))

	request := hmntsk.TaskRequest{TaskID: task.ID, Actor: invoicing.Alice}
	if _, err := svc.Claim(ctx, request); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	if _, err := svc.Start(ctx, request); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	completed, err := svc.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: request,
		Output:      []byte(`{"approved":true,"reason":"within-budget"}`),
	})
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}

	done := completed.Task
	fmt.Fprintf(w, "INV-42 once %s: %s\n", done.Status, names.Mask(resolveLink(stored, done, "acme")))

	return nil
}

// resolveLink is the host's link policy: open work links to its form, finished
// work links to the invoice, and {tenant} is the host's placeholder, filled in
// after ExpandRoute has left it untouched.
func resolveLink(spec hmntsk.TypeSpec, task hmntsk.Task, tenant string) string {
	template := spec.Metadata[hmntsk.MetadataRoute]
	if task.Status == hmntsk.StatusCompleted {
		template = spec.Metadata[keyDoneLink]
	}

	return strings.ReplaceAll(hmntsk.ExpandRoute(template, task), "{tenant}", url.PathEscape(tenant))
}

func newService() (*hmntsk.Service, error) {
	svc, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(invoicing.Directory()))
	if err != nil {
		return nil, fmt.Errorf("new service: %w", err)
	}

	return svc, nil
}

func createApproval(ctx context.Context, svc *hmntsk.Service, invoiceID string) (hmntsk.Task, error) {
	created, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: invoiceID, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoiceID, invoicing.ActivityApprove),
	})
	if err != nil {
		return hmntsk.Task{}, fmt.Errorf("create approval for %s: %w", invoiceID, err)
	}

	return created.Task, nil
}
