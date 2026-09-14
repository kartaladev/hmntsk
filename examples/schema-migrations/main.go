// Command schema-migrations applies the engine's published schema the way a
// host's migration step would, verifies it, shows transaction scopes nesting,
// and embeds the engine under a table prefix.
//
// The engine never runs DDL for you in normal operation. It publishes the
// statements per dialect; your migration tool applies them; VerifySchema, at
// startup, tells you everything that does not match before traffic finds it.
//
//	go run ./schema-migrations
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/sqlkit"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "schema-migrations:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, w io.Writer) error {
	dir, err := os.MkdirTemp("", "schema-migrations-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	db, err := invoicing.OpenSQLite(filepath.Join(dir, "app.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	demo.Default(w, "the published schema, applied by the host's migration step")

	store := sqlstore.New(db, sqlcore.SQLite)
	if err := migrate(ctx, w, store); err != nil {
		return err
	}

	fmt.Fprintln(w, "applied the published statements; schema verified")

	svc, err := newService(store)
	if err != nil {
		return err
	}

	demo.Default(w, "nested scopes join one transaction")

	if err := nested(ctx, w, store, svc); err != nil {
		return err
	}

	demo.Override(w, "a table prefix, for a database that already uses those names")

	prefixed := sqlstore.New(db, sqlcore.SQLite, sqlstore.WithTablePrefix("acme_"))
	if err := migrate(ctx, w, prefixed); err != nil {
		return err
	}

	acme, err := newService(prefixed)
	if err != nil {
		return err
	}

	if err := create(ctx, acme, invoicing.ApproveType, "INV-53"); err != nil {
		return err
	}

	created, err := countFor(ctx, acme, "INV-53")
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "applied and verified; a task created on the prefixed tables: tasks for INV-53: %d\n", created)

	demo.Override(w, "verification reports every discrepancy")

	return mismatch(ctx, w, prefixed)
}

// migrate does what a host's migration pipeline does with the engine's schema:
// takes the published statements for its dialect and table prefix, applies
// them, and verifies the result before serving traffic.
//
// The statements come from the store's own builder so that they carry the
// store's prefix. A real pipeline writes them into its migration files once,
// rather than applying them at startup.
func migrate(ctx context.Context, w io.Writer, store *sqlstore.Store) error {
	builder := store.Builder()

	fmt.Fprintf(w, "tables: %s\n", strings.Join(builder.Tables(), ", "))

	statements, err := builder.Migrations()
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	if err := sqlkit.ApplySchema(ctx, store, builder.Dialect(), statements); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	if err := store.VerifySchema(ctx); err != nil {
		return fmt.Errorf("verify schema: %w", err)
	}

	return nil
}

// nested runs engine operations inside one transactional scope. An operation
// invoked inside another shares its transaction: no second transaction and no
// savepoint, so everything commits together, and a failure anywhere inside
// aborts the whole scope.
func nested(ctx context.Context, w io.Writer, store *sqlstore.Store, svc *hmntsk.Service) error {
	err := store.Do(ctx, func(ctx context.Context) error {
		fmt.Fprintf(w, "inside the scope, the engine sees a transaction: %t\n", store.InTransaction(ctx))

		if err := create(ctx, svc, invoicing.ReviewType, "INV-51"); err != nil {
			return err
		}

		return create(ctx, svc, invoicing.ApproveType, "INV-51")
	})
	if err != nil {
		return fmt.Errorf("scope: %w", err)
	}

	committed, err := countFor(ctx, svc, "INV-51")
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "two creates in one scope committed together: tasks for INV-51: %d\n", committed)

	err = store.Do(ctx, func(ctx context.Context) error {
		if err := create(ctx, svc, invoicing.ReviewType, "INV-52"); err != nil {
			return err
		}

		// A type nobody registered: this create fails, and so does the scope.
		return create(ctx, svc, "invoice.unknown", "INV-52")
	})
	if err == nil {
		return errors.New("a scope with a failing operation committed")
	}

	rolledBack, err := countFor(ctx, svc, "INV-52")
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "a failure inside the scope rolled back everything: tasks for INV-52: %d\n", rolledBack)

	return nil
}

// mismatch removes one index the engine's queries rely on, as a hand-edited
// or half-applied migration might, and shows what verification reports.
func mismatch(ctx context.Context, w io.Writer, store *sqlstore.Store) error {
	index := store.Builder().TableName("tasks_urgency_idx")

	if err := store.ExecStatement(ctx, "DROP INDEX "+index); err != nil {
		return fmt.Errorf("drop index: %w", err)
	}

	fmt.Fprintf(w, "dropped index %s\n", index)

	err := store.VerifySchema(ctx)

	var schemaErr *sqlkit.SchemaError
	if !errors.As(err, &schemaErr) {
		return fmt.Errorf("verification did not report a schema error: %w", err)
	}

	tables := make([]string, 0, len(schemaErr.Issues))
	for _, issue := range schemaErr.Issues {
		if !slices.Contains(tables, issue.Table) {
			tables = append(tables, issue.Table)
		}
	}

	fmt.Fprintf(w, "schema mismatch: %t; issues: %d, on table %s\n",
		errors.Is(err, sqlkit.ErrSchemaMismatch), len(schemaErr.Issues), strings.Join(tables, ", "))

	return nil
}

func newService(store *sqlstore.Store) (*hmntsk.Service, error) {
	svc, err := hmntsk.New(store, hmntsk.WithGroupResolver(invoicing.Directory()))
	if err != nil {
		return nil, fmt.Errorf("new service: %w", err)
	}

	if err := invoicing.Register(svc); err != nil {
		return nil, err
	}

	return svc, nil
}

func create(ctx context.Context, svc *hmntsk.Service, taskType, invoice string) error {
	_, err := svc.Create(ctx, hmntsk.CreateRequest{
		Type:        taskType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: invoice, Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation(invoice, invoicing.ActivityOf(taskType)),
	})
	if err != nil {
		return fmt.Errorf("create %s for %s: %w", taskType, invoice, err)
	}

	return nil
}

func countFor(ctx context.Context, svc *hmntsk.Service, invoice string) (int64, error) {
	n, err := svc.Count(ctx, hmntsk.Query{OwnerType: invoicing.OwnerType, OwnerRef: invoice})
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}

	return n, nil
}
