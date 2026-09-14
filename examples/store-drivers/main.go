// Command store-drivers runs the same invoice-task wiring on every database
// driver hmntsk supports, and joins the host's own transaction in each driver's
// transaction type.
//
// Nothing above the store changes between drivers: the engine, the task types,
// the lifecycle and the queries are the same code. What a host chooses is the
// store it passes to hmntsk.New, and the transaction type it puts on the
// context when a task change must commit with its own rows.
//
// It needs a real PostgreSQL and MySQL; see README.md for the docker commands.
//
//	HMNTSK_POSTGRES_DSN=... HMNTSK_MYSQL_DSN=... go run ./store-drivers
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers "mysql" for database/sql
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" for database/sql
	gormmysql "gorm.io/driver/mysql"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	gormstore "github.com/kartaladev/hmntsk/store/gorm"
	pgxstore "github.com/kartaladev/hmntsk/store/pgx"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
	"github.com/kartaladev/hmntsk/store/sqlcore"
)

// The commands README.md gives for starting each database, repeated here so a
// reader who runs the program without them is told what to do.
const (
	startPostgres = "docker run --rm -d --name hmntsk-postgres -p 5432:5432 " +
		"-e POSTGRES_USER=hmntsk -e POSTGRES_PASSWORD=hmntsk -e POSTGRES_DB=hmntsk postgres:17.6-alpine\n" +
		"  export HMNTSK_POSTGRES_DSN='postgres://hmntsk:hmntsk@127.0.0.1:5432/hmntsk?sslmode=disable'"
	startMySQL = "docker run --rm -d --name hmntsk-mysql -p 3306:3306 " +
		"-e MYSQL_ROOT_PASSWORD=hmntsk -e MYSQL_USER=hmntsk -e MYSQL_PASSWORD=hmntsk -e MYSQL_DATABASE=hmntsk mysql:8.4.6\n" +
		"  export HMNTSK_MYSQL_DSN='hmntsk:hmntsk@tcp(127.0.0.1:3306)/hmntsk?parseTime=true&loc=UTC&clientFoundRows=true&multiStatements=true'"
)

// databases says where the two servers are. The test starts them in containers;
// a reader starts them with the commands above.
type databases struct {
	Postgres string
	MySQL    string
}

func main() {
	if err := runFromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "store-drivers:", err)
		os.Exit(1)
	}
}

func runFromEnv() error {
	postgres, pgErr := demo.RequireEnv(os.Getenv, "HMNTSK_POSTGRES_DSN", startPostgres)
	mysql, myErr := demo.RequireEnv(os.Getenv, "HMNTSK_MYSQL_DSN", startMySQL)

	if err := errors.Join(pgErr, myErr); err != nil {
		return err
	}

	return run(context.Background(), os.Stdout, databases{Postgres: postgres, MySQL: mysql})
}

func run(ctx context.Context, w io.Writer, dbs databases) error {
	// A fresh prefix per run keeps the combinations sharing one server apart,
	// and keeps a second run against the same database from seeing the first.
	run, err := randomHex(3)
	if err != nil {
		return err
	}

	var backends []*backend

	defer func() {
		for _, b := range slices.Backward(backends) {
			b.close(context.WithoutCancel(ctx))
		}
	}()

	for _, opener := range []struct {
		tag  string
		open func(ctx context.Context, dbs databases, prefix string) (*backend, error)
	}{
		{"sp", openSQLPostgres},
		{"px", openPgx},
		{"sm", openSQLMySQL},
		{"gp", openGORMPostgres},
		{"gm", openGORMMySQL},
	} {
		b, err := opener.open(ctx, dbs, "sd"+run+"_"+opener.tag+"_")
		if err != nil {
			return err
		}

		backends = append(backends, b)
	}

	demo.Default(w, "the same wiring on every driver and database")

	if err := section(ctx, w, backends, lifecycle); err != nil {
		return err
	}

	demo.Override(w, "the host's own transaction, in each driver's transaction type")

	return section(ctx, w, backends, hostTransaction)
}

// section runs one step on every backend, prints each result, and says whether
// they all came out the same.
func section(ctx context.Context, w io.Writer, backends []*backend, step func(context.Context, *backend) (string, error)) error {
	var results []string

	for _, b := range backends {
		result, err := step(ctx, b)
		if err != nil {
			return fmt.Errorf("%s: %w", b.label, err)
		}

		fmt.Fprintf(w, "%s: %s\n", b.label, result)

		results = append(results, result)
	}

	identical := len(slices.Compact(slices.Clone(results))) == 1
	fmt.Fprintf(w, "identical on every driver: %t\n", identical)

	return nil
}

// lifecycle is the default: the engine opens and commits its own transactions,
// exactly as it does on the in-memory store.
func lifecycle(ctx context.Context, b *backend) (string, error) {
	// A host applies sqlcore's published statements through its own migration
	// tool; Migrate stands in for that here. VerifySchema is what a host calls
	// at startup either way.
	if err := b.store.Migrate(ctx); err != nil {
		return "", fmt.Errorf("migrate: %w", err)
	}

	if err := b.store.VerifySchema(ctx); err != nil {
		return "", fmt.Errorf("verify schema: %w", err)
	}

	if err := b.store.ExecStatement(ctx, "CREATE TABLE IF NOT EXISTS "+b.invoices+
		" (id VARCHAR(64) PRIMARY KEY, supplier VARCHAR(200) NOT NULL, amount BIGINT NOT NULL)"); err != nil {
		return "", fmt.Errorf("create the host's invoices table: %w", err)
	}

	created, err := b.engine.Create(ctx, approval(newInvoice("INV-42")))
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}

	request := hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: invoicing.Alice}

	claimed, err := b.engine.Claim(ctx, request)
	if err != nil {
		return "", fmt.Errorf("claim: %w", err)
	}

	started, err := b.engine.Start(ctx, request)
	if err != nil {
		return "", fmt.Errorf("start: %w", err)
	}

	completed, err := b.engine.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: request,
		Output:      []byte(`{"approved":true,"reason":"within-budget"}`),
	})
	if err != nil {
		return "", fmt.Errorf("complete: %w", err)
	}

	statuses := []string{
		string(created.Task.Status), string(claimed.Task.Status),
		string(started.Task.Status), string(completed.Task.Status),
	}

	tasks, err := b.tasksFor(ctx, "INV-42")
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("schema verified; INV-42 %s; tasks for INV-42: %d", strings.Join(statuses, ", "), tasks), nil
}

// hostTransaction is the override: the host begins a transaction in its
// driver's own type, writes its invoice, and creates the task inside it. The
// engine joins; whoever begins, commits.
func hostTransaction(ctx context.Context, b *backend) (string, error) {
	committed, err := b.writeInOneTransaction(ctx, "INV-43", true)
	if err != nil {
		return "", err
	}

	rolledBack, err := b.writeInOneTransaction(ctx, "INV-44", false)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("committed %s; rolled back %s", committed, rolledBack), nil
}

// hostTx is a transaction the host began, in whatever type its driver uses.
type hostTx struct {
	// ctx carries the transaction, so the engine joins it.
	ctx context.Context
	// insert writes the host's own invoice row inside the transaction.
	insert func(invoice invoicing.Invoice) error
	// commit and rollback end the transaction.
	commit   func() error
	rollback func() error
}

// backend is one driver on one database, and the few things that differ
// between drivers: how a transaction is begun and how the host writes its row.
type backend struct {
	label    string
	store    store
	engine   *hmntsk.Service
	events   *collector
	invoices string

	begin         func(ctx context.Context) (hostTx, error)
	countInvoices func(ctx context.Context, id string) (int64, error)
	closeDB       func()
}

// store is what every driver's store has in common, beyond hmntsk.Store.
type store interface {
	hmntsk.Store
	Migrate(ctx context.Context) error
	VerifySchema(ctx context.Context) error
	ExecStatement(ctx context.Context, sql string, args ...any) error
	Builder() *sqlcore.Builder
}

func (b *backend) writeInOneTransaction(ctx context.Context, invoiceID string, commit bool) (string, error) {
	// Forget events from earlier work, so that what follows is only this
	// transaction's.
	b.events.take()

	tx, err := b.begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}

	invoice := newInvoice(invoiceID)

	if err := tx.insert(invoice); err != nil {
		return "", errors.Join(fmt.Errorf("insert invoice: %w", err), tx.rollback())
	}

	created, err := b.engine.Create(tx.ctx, approval(invoice))
	if err != nil {
		return "", errors.Join(fmt.Errorf("create: %w", err), tx.rollback())
	}

	// Nothing may hear about an event before the host's commit makes it true.
	waited := created.Pending() && len(b.events.take()) == 0

	finish := tx.rollback
	if commit {
		finish = tx.commit
	}

	if err := finish(); err != nil {
		return "", fmt.Errorf("finish: %w", err)
	}

	var dispatched string

	if commit {
		if err := created.Dispatch(ctx); err != nil {
			return "", fmt.Errorf("dispatch: %w", err)
		}

		dispatched = fmt.Sprintf(", dispatch waited for commit: %t, then %s", waited, strings.Join(b.events.take(), ", "))
	}

	rows, err := b.countInvoices(ctx, invoiceID)
	if err != nil {
		return "", fmt.Errorf("count invoices: %w", err)
	}

	tasks, err := b.tasksFor(ctx, invoiceID)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s (invoice rows %d, tasks %d%s)", invoiceID, rows, tasks, dispatched), nil
}

func (b *backend) tasksFor(ctx context.Context, invoiceID string) (int64, error) {
	n, err := b.engine.Count(ctx, hmntsk.Query{OwnerType: invoicing.OwnerType, OwnerRef: invoiceID})
	if err != nil {
		return 0, fmt.Errorf("count tasks: %w", err)
	}

	return n, nil
}

// close removes the tables this run created, then closes the connection.
func (b *backend) close(ctx context.Context) {
	_ = b.store.ExecStatement(ctx, "DROP TABLE IF EXISTS "+b.invoices)
	_ = b.store.Builder().Drop(ctx, b.store)

	b.closeDB()
}

// newBackend wires the engine over a store, identically for every driver.
func newBackend(label, prefix string, s store, closeDB func()) (*backend, error) {
	events := &collector{}

	engine, err := hmntsk.New(s,
		hmntsk.WithGroupResolver(invoicing.Directory()),
		hmntsk.WithEventHandlers(events),
	)
	if err != nil {
		closeDB()

		return nil, fmt.Errorf("%s: new engine: %w", label, err)
	}

	if err := invoicing.Register(engine); err != nil {
		closeDB()

		return nil, fmt.Errorf("%s: %w", label, err)
	}

	return &backend{label: label, store: s, engine: engine, events: events, invoices: prefix + "invoices", closeDB: closeDB}, nil
}

// openSQLPostgres is store/sql over pgx's database/sql driver.
func openSQLPostgres(ctx context.Context, dbs databases, prefix string) (*backend, error) {
	numbered := func(n int) string { return "$" + strconv.Itoa(n) }

	return openSQL(ctx, "database/sql on PostgreSQL", "pgx", dbs.Postgres, sqlcore.PostgreSQL, prefix, numbered)
}

// openSQLMySQL is store/sql over the go-sql-driver MySQL driver.
func openSQLMySQL(ctx context.Context, dbs databases, prefix string) (*backend, error) {
	positional := func(int) string { return "?" }

	return openSQL(ctx, "database/sql on MySQL", "mysql", dbs.MySQL, sqlcore.MySQL, prefix, positional)
}

// openSQL opens store/sql. database/sql does not translate placeholders, so the
// host's own statements use the driver's: placeholder(n) is the nth.
func openSQL(
	ctx context.Context,
	label, driver, dsn string,
	dialect sqlcore.Dialect,
	prefix string,
	placeholder func(n int) string,
) (*backend, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("%s: open: %w", label, err)
	}

	if err := waitFor(ctx, db.PingContext); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("%s: reach the database: %w", label, err)
	}

	b, err := newBackend(label, prefix, sqlstore.New(db, dialect, sqlstore.WithTablePrefix(prefix)), func() { _ = db.Close() })
	if err != nil {
		return nil, err
	}

	insert := fmt.Sprintf("INSERT INTO %s (id, supplier, amount) VALUES (%s, %s, %s)",
		b.invoices, placeholder(1), placeholder(2), placeholder(3))
	count := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = %s", b.invoices, placeholder(1))

	b.begin = func(ctx context.Context) (hostTx, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return hostTx{}, err
		}

		return hostTx{
			ctx: sqlstore.ContextWithTx(ctx, tx),
			insert: func(inv invoicing.Invoice) error {
				_, err := tx.ExecContext(ctx, insert, inv.ID, inv.Supplier, inv.Amount)

				return err
			},
			commit:   tx.Commit,
			rollback: tx.Rollback,
		}, nil
	}

	b.countInvoices = func(ctx context.Context, id string) (n int64, err error) {
		err = db.QueryRowContext(ctx, count, id).Scan(&n)

		return n, err
	}

	return b, nil
}

// openPgx is store/pgx over a pgxpool.Pool, with pgx's own transaction type.
func openPgx(ctx context.Context, dbs databases, prefix string) (*backend, error) {
	const label = "pgx on PostgreSQL"

	pool, err := pgxpool.New(ctx, dbs.Postgres)
	if err != nil {
		return nil, fmt.Errorf("%s: open: %w", label, err)
	}

	if err := waitFor(ctx, pool.Ping); err != nil {
		pool.Close()

		return nil, fmt.Errorf("%s: reach the database: %w", label, err)
	}

	b, err := newBackend(label, prefix, pgxstore.New(pool, pgxstore.WithTablePrefix(prefix)), pool.Close)
	if err != nil {
		return nil, err
	}

	b.begin = func(ctx context.Context) (hostTx, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return hostTx{}, err
		}

		return hostTx{
			ctx: pgxstore.ContextWithTx(ctx, tx),
			insert: func(inv invoicing.Invoice) error {
				_, err := tx.Exec(ctx, "INSERT INTO "+b.invoices+" (id, supplier, amount) VALUES ($1, $2, $3)",
					inv.ID, inv.Supplier, inv.Amount)

				return err
			},
			commit:   func() error { return tx.Commit(ctx) },
			rollback: func() error { return tx.Rollback(ctx) },
		}, nil
	}

	b.countInvoices = func(ctx context.Context, id string) (n int64, err error) {
		err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+b.invoices+" WHERE id = $1", id).Scan(&n)

		return n, err
	}

	return b, nil
}

// openGORMPostgres is store/gorm over GORM's PostgreSQL driver.
func openGORMPostgres(ctx context.Context, dbs databases, prefix string) (*backend, error) {
	return openGORM(ctx, "GORM on PostgreSQL", gormpostgres.Open(dbs.Postgres), sqlcore.PostgreSQL, prefix)
}

// openGORMMySQL is store/gorm over GORM's MySQL driver.
func openGORMMySQL(ctx context.Context, dbs databases, prefix string) (*backend, error) {
	return openGORM(ctx, "GORM on MySQL", gormmysql.Open(dbs.MySQL), sqlcore.MySQL, prefix)
}

func openGORM(ctx context.Context, label string, dialector gorm.Dialector, dialect sqlcore.Dialect, prefix string) (*backend, error) {
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
		// The engine owns its transactions; GORM must not wrap each statement in
		// one of its own.
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: open: %w", label, err)
	}

	pool, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("%s: pool: %w", label, err)
	}

	if err := waitFor(ctx, pool.PingContext); err != nil {
		_ = pool.Close()

		return nil, fmt.Errorf("%s: reach the database: %w", label, err)
	}

	b, err := newBackend(label, prefix, gormstore.New(db, dialect, gormstore.WithTablePrefix(prefix)), func() { _ = pool.Close() })
	if err != nil {
		return nil, err
	}

	// GORM rewrites ? for the dialect it is connected to.
	b.begin = func(ctx context.Context) (hostTx, error) {
		tx := db.WithContext(ctx).Begin()
		if tx.Error != nil {
			return hostTx{}, tx.Error
		}

		return hostTx{
			ctx: gormstore.ContextWithTx(ctx, tx),
			insert: func(inv invoicing.Invoice) error {
				return tx.Exec("INSERT INTO "+b.invoices+" (id, supplier, amount) VALUES (?, ?, ?)",
					inv.ID, inv.Supplier, inv.Amount).Error
			},
			commit:   func() error { return tx.Commit().Error },
			rollback: func() error { return tx.Rollback().Error },
		}, nil
	}

	b.countInvoices = func(ctx context.Context, id string) (n int64, err error) {
		err = db.WithContext(ctx).Raw("SELECT COUNT(*) FROM "+b.invoices+" WHERE id = ?", id).Scan(&n).Error

		return n, err
	}

	return b, nil
}

// newInvoice is the invoice every step in this scenario is about.
func newInvoice(id string) invoicing.Invoice {
	return invoicing.Invoice{ID: id, Supplier: "Acme Paper", Amount: 1299}
}

func approval(invoice invoicing.Invoice) hmntsk.CreateRequest {
	return hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoice),
		Correlation: invoicing.Correlation(invoice.ID, invoicing.ActivityApprove),
	}
}

// collector records the types of events dispatched to in-process handlers.
type collector struct {
	mu    sync.Mutex
	types []string
}

func (c *collector) HandleEvent(_ context.Context, event hmntsk.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.types = append(c.types, string(event.Type))

	return nil
}

// take returns the types recorded since the last take.
func (c *collector) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	taken := c.types
	c.types = nil

	return taken
}

// waitFor retries the first contact with a database for a while: a server that
// has just started can refuse a connection for a moment after it says it is
// ready.
func waitFor(ctx context.Context, ping func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	for {
		err := ping(ctx)
		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random prefix: %w", err)
	}

	return hex.EncodeToString(b), nil
}
