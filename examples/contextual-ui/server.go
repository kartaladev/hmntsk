package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/notify"
	notifysql "github.com/kartaladev/hmntsk/notify/sqlstore"
	"github.com/kartaladev/hmntsk/relay"
	"github.com/kartaladev/hmntsk/sqlkit"
	stdsqlexec "github.com/kartaladev/hmntsk/sqlkit/stdsql"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
	"github.com/kartaladev/hmntsk/store/sqlcore"
	"github.com/kartaladev/hmntsk/tasknotify"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
	httptransport "github.com/kartaladev/hmntsk/transport/http"
)

// dist is the built page, committed so that `go run` needs no Node toolchain.
// `make ui-build` regenerates it from web/.
//
//go:embed all:dist
var dist embed.FS

type config struct {
	// DataDir holds the SQLite database, so that a reader can open it.
	DataDir string
	// Clock is the engine's and the notifier's clock; nil is the system clock.
	Clock hmntsk.Clock
	// InvoiceDelay is how long a supplier takes to send its invoice once it has
	// the purchase order; nil is a random 5 to 20 seconds.
	InvoiceDelay func() time.Duration
}

// randomInvoiceDelay keeps the demo's suppliers quick, and unpredictable
// enough that invoices arrive while the viewer is doing something else.
func randomInvoiceDelay() time.Duration {
	return 5*time.Second + rand.N(15*time.Second)
}

// server is the demo's back end: the task engine, the purchasing records, and
// notifications on one SQLite database, and the HTTP contracts a browser
// client consumes.
type server struct {
	db           *sql.DB
	engine       *hmntsk.Service
	notifier     *notify.Service
	hub          *notify.Hub
	relay        *relay.Relay
	records      *store
	workflow     *workflowSink
	invoiceDelay func() time.Duration
	handler      http.Handler

	mu         sync.Mutex
	nextNumber int
	closers    []func()
}

func newServer(ctx context.Context, cfg config) (_ *server, err error) {
	s := &server{nextNumber: 201, invoiceDelay: cfg.InvoiceDelay}
	if s.invoiceDelay == nil {
		s.invoiceDelay = randomInvoiceDelay
	}

	defer func() {
		if err != nil {
			s.Close()
		}
	}()

	if err := s.openStores(ctx, cfg); err != nil {
		return nil, err
	}

	if err := s.startHub(ctx); err != nil {
		return nil, err
	}

	if err := s.seed(ctx); err != nil {
		return nil, err
	}

	if err := s.routes(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *server) openStores(ctx context.Context, cfg config) error {
	db, err := openSQLite(filepath.Join(cfg.DataDir, "contextual.db"))
	if err != nil {
		return err
	}

	s.db = db
	s.closers = append(s.closers, func() { _ = db.Close() })

	// The engine's tables, the host's purchasing records and the notifications
	// share one database. A host applies each schema through its own
	// migrations.
	taskStore := sqlstore.New(db, sqlcore.SQLite)
	if err := taskStore.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate tasks: %w", err)
	}

	s.records = &store{db: db}
	if err := s.records.Migrate(ctx); err != nil {
		return err
	}

	executor, err := stdsqlexec.New(db, sqlkit.SQLite)
	if err != nil {
		return fmt.Errorf("new executor: %w", err)
	}

	notificationStore, err := notifysql.New(executor)
	if err != nil {
		return fmt.Errorf("new notification store: %w", err)
	}

	if err := notificationStore.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate notifications: %w", err)
	}

	engineOpts := []hmntsk.Option{hmntsk.WithGroupResolver(directory())}
	notifyOpts := []notify.Option{}

	if cfg.Clock != nil {
		engineOpts = append(engineOpts, hmntsk.WithClock(cfg.Clock))
		notifyOpts = append(notifyOpts, notify.WithClock(cfg.Clock))
	}

	if s.engine, err = hmntsk.New(taskStore, engineOpts...); err != nil {
		return fmt.Errorf("new engine: %w", err)
	}

	if err := registerTypes(s.engine); err != nil {
		return err
	}

	if s.notifier, err = notify.New(notificationStore, notifyOpts...); err != nil {
		return fmt.Errorf("new notifier: %w", err)
	}

	// Default rules, links and titles: the context link is the types'
	// hmntsk.route, which is the order page.
	projector, err := tasknotify.New(s.engine, s.notifier)
	if err != nil {
		return fmt.Errorf("new projector: %w", err)
	}

	s.workflow = &workflowSink{engine: s.engine, records: s.records, invoiceDelay: s.invoiceDelay}

	// Each sink accepts every event independently: a workflow step that must be
	// retried never holds back a notification, nor the other way round.
	if s.relay, err = relay.NewRelay(s.engine, relay.WithSinks(projector, s.workflow)); err != nil {
		return fmt.Errorf("new relay: %w", err)
	}

	return nil
}

// startHub runs the hub, which fans change signals out to open streams, and
// waits until streams can subscribe.
func (s *server) startHub(ctx context.Context) error {
	hub, err := notify.NewHub(s.notifier.Broadcaster())
	if err != nil {
		return fmt.Errorf("new hub: %w", err)
	}

	s.hub = hub

	stop, err := demo.RunHub(ctx, hub, 5*time.Second)
	if err != nil {
		return fmt.Errorf("start hub: %w", err)
	}

	s.closers = append(s.closers, stop)

	return nil
}

// start runs the relay and the demo's suppliers until the returned stop is
// called, so notifications and workflow steps follow task changes, and
// invoices arrive, within a second.
func (s *server) start(ctx context.Context) (stop func()) {
	stopRelay := demo.Background(ctx, func(ctx context.Context) error {
		return s.relay.Run(ctx, time.Second)
	})

	stopSuppliers := demo.Background(ctx, func(ctx context.Context) error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				// A failed pass leaves the invoices due, so the next one tries
				// them again.
				_, _ = s.receiveDueInvoices(ctx)
			}
		}
	})

	return func() {
		stopSuppliers()
		stopRelay()
	}
}

// Handler is everything the page talks to.
func (s *server) Handler() http.Handler { return s.handler }

// Close stops the hub and closes the database.
func (s *server) Close() {
	for _, closer := range slices.Backward(s.closers) {
		closer()
	}

	s.closers = nil
}

// seed creates an application worth looking at: eight orders erin placed, at
// every stage from approval to the invoice's, at different priorities and
// deadlines, one of them already claimed by alice.
func (s *server) seed(ctx context.Context) error {
	now := s.engine.Clock().Now()

	seeds := []struct {
		supplier    string
		description string
		amount      int64
		stage       string
		priority    hmntsk.Priority
		due         time.Duration
		claimBy     string
	}{
		{"Acme Paper", "Printer paper for the finance floor", 1299, orderPendingApproval, 2, 20 * time.Hour, ""},
		{"Stark Industries", "Laptops for the new starters", 4200, orderPendingApproval, 1, 3 * time.Hour, ""},
		{"Globex Cloud", "Annual cloud hosting renewal", 18400, orderAwaitingPurchaseOrder, 1, 6 * time.Hour, ""},
		{"Vandelay Imports", "Warehouse shelving", 9900, orderAwaitingPurchaseOrder, 3, 40 * time.Hour, ""},
		{"Hooli Travel", "Flights to the partner summit", 2275, orderAwaitingInvoice, 0, 0, ""},
		{"Initech Chairs", "Ergonomic chairs for the support team", 3150, orderInvoiceReview, 3, 30 * time.Hour, ""},
		{"Umbrella Catering", "Catering for the quarterly review", 640, orderInvoiceApproval, 2, 26 * time.Hour, alice},
		{"Acme Paper", "Envelopes and letterheads", 410, orderInvoiceApproval, 4, 2 * time.Hour, ""},
	}

	for i, seed := range seeds {
		due := now.Add(seed.due)

		_, task, err := s.openOrder(ctx, newOrder{
			Number:      101 + i,
			RequestedBy: erin,
			Supplier:    seed.supplier,
			Description: seed.description,
			Amount:      seed.amount,
			Stage:       seed.stage,
			Priority:    &seed.priority,
			DueAt:       &due,
		})
		if err != nil {
			return fmt.Errorf("seed order %d: %w", 101+i, err)
		}

		if seed.claimBy != "" {
			if _, err := s.engine.Claim(ctx, hmntsk.TaskRequest{TaskID: task.ID, Actor: seed.claimBy}); err != nil {
				return fmt.Errorf("seed claim %s: %w", task.Correlation.OwnerRef, err)
			}
		}
	}

	return nil
}

func (s *server) routes() error {
	mux := http.NewServeMux()

	// The task contract, with its defaults: self-only queries and
	// participants-only reads. The page never needs more.
	api, err := transportcore.New(s.engine)
	if err != nil {
		return fmt.Errorf("new task api: %w", err)
	}

	// Handler, not Mount: Mount claims the mux's "/" for its own not-found
	// answers, and here "/" is the page.
	tasks, err := httptransport.Handler(api, httptransport.WithActorFunc(actor))
	if err != nil {
		return fmt.Errorf("new task handler: %w", err)
	}

	mux.Handle("/v1/", tasks)

	notifications, err := notify.NewHandler(s.notifier, s.hub, notify.WithActor(func(r *http.Request) (string, error) {
		return actor(r), nil
	}))
	if err != nil {
		return fmt.Errorf("new notification handler: %w", err)
	}

	mux.Handle("/v1/notifications", notifications)
	mux.Handle("/v1/notifications/", notifications)

	// The application's own API: who is signed in, the supplier registry,
	// orders and their records, and purchase order documents.
	mux.HandleFunc("GET /demo/users", listUsers)
	mux.HandleFunc("GET /demo/session", session)
	mux.HandleFunc("POST /demo/session", signIn)
	mux.HandleFunc("DELETE /demo/session", signOut)
	mux.HandleFunc("GET /demo/suppliers", listSuppliers)
	mux.HandleFunc("GET /demo/orders", s.listOrders)
	mux.HandleFunc("POST /demo/orders", s.placeOrder)
	mux.HandleFunc("GET /demo/orders/{id}", s.orderRecord)
	mux.HandleFunc("POST /demo/orders/{id}/purchase-order/send", s.sendPurchaseOrder)
	mux.HandleFunc("POST /demo/orders/{id}/purchase-order/upload", s.uploadPurchaseOrder)
	mux.HandleFunc("GET /demo/documents/{id}", s.downloadDocument)
	// Anything else under /demo/ is an API miss, not a page route.
	mux.Handle("/demo/", http.NotFoundHandler())

	page, err := fs.Sub(dist, "dist")
	if err != nil {
		return fmt.Errorf("embedded page: %w", err)
	}

	mux.Handle("/", spa(page))

	s.handler = mux

	return nil
}

// spa serves the built page's files, and the page itself for any other path
// that does not name a file, so the client's own routes survive a reload. API
// paths never reach it: the mux routes /v1/ and /demo/ first.
func spa(page fs.FS) http.Handler {
	files := http.FileServerFS(page)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

		if name != "" {
			if _, err := fs.Stat(page, name); err == nil {
				files.ServeHTTP(w, r)

				return
			}

			if path.Ext(name) != "" {
				http.NotFound(w, r)

				return
			}
		}

		http.ServeFileFS(w, r, page, "index.html")
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
