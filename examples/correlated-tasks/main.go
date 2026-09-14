// Command correlated-tasks creates tasks about invoices in the same SQLite
// database as the invoices, first in the engine's own transaction and then in
// the host's.
//
// A task is correlated to the record it is about through CorrelationData. When
// the record and the task must exist together or not at all, the host puts its
// transaction on the context and the engine joins it.
//
//	go run ./correlated-tasks
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "correlated-tasks:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, w io.Writer) (err error) {
	dir, err := os.MkdirTemp("", "correlated-tasks-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	db, err := invoicing.OpenSQLite(filepath.Join(dir, "app.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	// The engine's tables live beside the host's own. Migrate is for
	// development; a real host applies sqlcore's statements through its own
	// migration tool and calls VerifySchema at startup.
	store := sqlstore.New(db, sqlcore.SQLite)
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	if err := store.VerifySchema(ctx); err != nil {
		return fmt.Errorf("verify schema: %w", err)
	}

	invoices := invoicing.NewSQLRepository(db)
	if err := invoices.Migrate(ctx); err != nil {
		return err
	}

	fmt.Fprintln(w, "SQLite schema applied and verified")

	// An in-process event handler shows when events are dispatched.
	printEvents := hmntsk.EventHandlerFunc(func(_ context.Context, event hmntsk.Event) error {
		fmt.Fprintf(w, "event: %s for invoice %s\n", event.Type, event.Correlation.OwnerRef)

		return nil
	})

	svc, err := hmntsk.New(store,
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithEventHandlers(printEvents),
	)
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}

	if err := invoicing.Register(svc); err != nil {
		return err
	}

	demo.Default(w, "the engine commits its own transaction")

	if err := engineLed(ctx, w, svc, invoices); err != nil {
		return err
	}

	demo.Override(w, "the host's transaction, committed")

	if err := hostLed(ctx, w, db, svc, invoices, "INV-42", true); err != nil {
		return err
	}

	demo.Override(w, "the host's transaction, rolled back")

	return hostLed(ctx, w, db, svc, invoices, "INV-43", false)
}

// engineLed is the default: with no transaction on the context, the engine
// opens one, commits it and then dispatches the task's events. The invoice was
// saved separately, so a crash between the two leaves an invoice with no task.
func engineLed(ctx context.Context, w io.Writer, svc *hmntsk.Service, invoices *invoicing.SQLRepository) error {
	invoice := invoicing.Invoice{ID: "INV-41", Supplier: "Acme Paper", Amount: 420}
	if err := invoices.Save(ctx, invoice); err != nil {
		return err
	}

	fmt.Fprintf(w, "invoice %s saved by the host\n", invoice.ID)

	created, err := svc.Create(ctx, reviewRequest(invoice))
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	fmt.Fprintf(w, "review task created: %s\n", created.Task.Status)

	return nil
}

// hostLed puts the host's transaction on the context. The engine joins it, so
// the invoice and its task commit or roll back together. The engine cannot see
// the commit, so it hands the event dispatch back to the host, to run after
// committing and never before.
func hostLed(
	ctx context.Context,
	w io.Writer,
	db *sql.DB,
	svc *hmntsk.Service,
	invoices *invoicing.SQLRepository,
	invoiceID string,
	commit bool,
) error {
	invoice := invoicing.Invoice{ID: invoiceID, Supplier: "Acme Paper", Amount: 1299}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	txCtx := sqlstore.ContextWithTx(ctx, tx)

	if err := invoices.Save(txCtx, invoice); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	created, err := svc.Create(txCtx, reviewRequest(invoice))
	if err != nil {
		return errors.Join(fmt.Errorf("create: %w", err), tx.Rollback())
	}

	fmt.Fprintf(w, "invoice %s and its review task written in one transaction\n", invoice.ID)

	if commit {
		fmt.Fprintf(w, "event dispatch waits for the host's commit: %t\n", created.Pending())

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit: %w", err)
		}

		fmt.Fprintln(w, "committed")

		if err := created.Dispatch(ctx); err != nil {
			return fmt.Errorf("dispatch: %w", err)
		}
	} else {
		if err := tx.Rollback(); err != nil {
			return fmt.Errorf("rollback: %w", err)
		}

		// A rolled-back result is never dispatched: its events were never
		// committed, so nobody may hear about them.
		fmt.Fprintln(w, "rolled back")
	}

	return printOutcome(ctx, w, svc, invoices, invoice.ID)
}

func reviewRequest(invoice invoicing.Invoice) hmntsk.CreateRequest {
	return hmntsk.CreateRequest{
		Type:        invoicing.ReviewType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoice),
		Correlation: invoicing.Correlation(invoice.ID, invoicing.ActivityReview),
	}
}

// printOutcome reads back, outside any transaction, what was committed.
func printOutcome(
	ctx context.Context,
	w io.Writer,
	svc *hmntsk.Service,
	invoices *invoicing.SQLRepository,
	invoiceID string,
) error {
	_, err := invoices.Get(ctx, invoiceID)

	exists := err == nil
	if err != nil && !errors.Is(err, invoicing.ErrNotFound) {
		return err
	}

	// Correlation is filterable: every task about this invoice.
	tasks, err := svc.Count(ctx, hmntsk.Query{OwnerType: invoicing.OwnerType, OwnerRef: invoiceID})
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}

	fmt.Fprintf(w, "invoice %s exists: %t, tasks for it: %d\n", invoiceID, exists, tasks)

	return nil
}
