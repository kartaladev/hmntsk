package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
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

// userCookie names the demo user the page chose.
//
// THIS IS NOT AUTHENTICATION. Anyone can set any cookie. It stands for the
// middleware a real host runs, which establishes who the caller is before
// hmntsk is asked anything.
const userCookie = "demo_user"

// demoUser is one person the page can act as.
type demoUser struct {
	ID     string   `json:"id"`
	Groups []string `json:"groups"`
}

// users are the demo's people, in the order the page lists them.
var users = []demoUser{
	{ID: invoicing.Alice, Groups: []string{invoicing.GroupApprovers}},
	{ID: invoicing.Bob, Groups: []string{invoicing.GroupApprovers}},
	{ID: invoicing.Carol, Groups: []string{invoicing.GroupManagers}},
	{ID: invoicing.Dave, Groups: []string{invoicing.GroupAuditors}},
}

type config struct {
	// DataDir holds the SQLite database, so that a reader can open it.
	DataDir string
	// Clock is the engine's and the notifier's clock; nil is the system clock.
	Clock hmntsk.Clock
}

// server is the demo's back end: the task engine and notifications on one
// SQLite database, and the HTTP contracts a browser client consumes.
type server struct {
	engine   *hmntsk.Service
	notifier *notify.Service
	hub      *notify.Hub
	relay    *relay.Relay
	invoices *invoicing.SQLRepository
	handler  http.Handler

	mu          sync.Mutex
	nextInvoice int
	closers     []func()
}

func newServer(ctx context.Context, cfg config) (_ *server, err error) {
	s := &server{nextInvoice: 201}

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
	db, err := invoicing.OpenSQLite(filepath.Join(cfg.DataDir, "inbox.db"))
	if err != nil {
		return err
	}

	s.closers = append(s.closers, func() { _ = db.Close() })

	// The engine's tables, the host's invoices and the notifications share one
	// database. A host applies each schema through its own migrations.
	taskStore := sqlstore.New(db, sqlcore.SQLite)
	if err := taskStore.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate tasks: %w", err)
	}

	s.invoices = invoicing.NewSQLRepository(db)
	if err := s.invoices.Migrate(ctx); err != nil {
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

	engineOpts := []hmntsk.Option{hmntsk.WithGroupResolver(invoicing.Directory())}
	notifyOpts := []notify.Option{}

	if cfg.Clock != nil {
		engineOpts = append(engineOpts, hmntsk.WithClock(cfg.Clock))
		notifyOpts = append(notifyOpts, notify.WithClock(cfg.Clock))
	}

	if s.engine, err = hmntsk.New(taskStore, engineOpts...); err != nil {
		return fmt.Errorf("new engine: %w", err)
	}

	if err := invoicing.Register(s.engine); err != nil {
		return err
	}

	if s.notifier, err = notify.New(notificationStore, notifyOpts...); err != nil {
		return fmt.Errorf("new notifier: %w", err)
	}

	// Default rules, links and titles: the context link is the invoice type's
	// hmntsk.route, which is what the page opens.
	projector, err := tasknotify.New(s.engine, s.notifier)
	if err != nil {
		return fmt.Errorf("new projector: %w", err)
	}

	if s.relay, err = relay.NewRelay(s.engine, relay.WithSinks(projector)); err != nil {
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
	s.closers = append(s.closers, demo.Background(ctx, hub.Run))

	if err := demo.WaitUntil(hub.Running, 5*time.Second); err != nil {
		return fmt.Errorf("hub did not start: %w", err)
	}

	return nil
}

// start runs the relay until the returned stop is called, so notifications
// follow task changes within a second.
func (s *server) start(ctx context.Context) (stop func()) {
	return demo.Background(ctx, func(ctx context.Context) error {
		return s.relay.Run(ctx, time.Second)
	})
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

// seed creates an inbox worth looking at: six tasks alice and bob may claim,
// at different priorities and deadlines, and one alice has already claimed.
func (s *server) seed(ctx context.Context) error {
	now := s.engine.Clock().Now()

	seeds := []struct {
		invoice  invoicing.Invoice
		taskType string
		priority hmntsk.Priority
		due      time.Duration
		claimBy  string
	}{
		{invoicing.Invoice{ID: "INV-101", Supplier: "Acme Paper", Amount: 1299}, invoicing.ApproveType, 0, 4 * time.Hour, ""},
		{invoicing.Invoice{ID: "INV-102", Supplier: "Globex Cloud", Amount: 18400}, invoicing.ApproveType, 1, 26 * time.Hour, ""},
		{invoicing.Invoice{ID: "INV-103", Supplier: "Initech Chairs", Amount: 3150}, invoicing.ApproveType, 3, 72 * time.Hour, ""},
		{invoicing.Invoice{ID: "INV-104", Supplier: "Umbrella Catering", Amount: 640}, invoicing.ApproveType, 5, 7 * 24 * time.Hour, ""},
		{invoicing.Invoice{ID: "INV-105", Supplier: "Hooli Travel", Amount: 2275}, invoicing.ApproveType, 2, 2 * time.Hour, ""},
		{invoicing.Invoice{ID: "INV-106", Supplier: "Vandelay Imports", Amount: 9900}, invoicing.ApproveType, 1, 30 * time.Hour, invoicing.Alice},
		{invoicing.Invoice{ID: "INV-107", Supplier: "Acme Paper", Amount: 410}, invoicing.ReviewType, 4, 48 * time.Hour, ""},
	}

	for _, seed := range seeds {
		if err := s.invoices.Save(ctx, seed.invoice); err != nil {
			return err
		}

		due := now.Add(seed.due)

		created, err := s.engine.Create(ctx, hmntsk.CreateRequest{
			Type:        seed.taskType,
			Actor:       "billing-service",
			Input:       invoicing.Input(seed.invoice),
			Correlation: invoicing.Correlation(seed.invoice.ID, invoicing.ActivityOf(seed.taskType)),
			Priority:    &seed.priority,
			DueAt:       &due,
		})
		if err != nil {
			return fmt.Errorf("seed %s: %w", seed.invoice.ID, err)
		}

		if seed.claimBy != "" {
			if _, err := s.engine.Claim(ctx, hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: seed.claimBy}); err != nil {
				return fmt.Errorf("seed claim %s: %w", seed.invoice.ID, err)
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

	mux.HandleFunc("GET /demo/users", s.listUsers)
	mux.HandleFunc("POST /demo/user", s.chooseUser)
	mux.HandleFunc("POST /demo/invoices", s.simulateInvoice)
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

// actor reads the demo cookie, accepting only a known demo user.
func actor(r *http.Request) string {
	cookie, err := r.Cookie(userCookie)
	if err != nil || !knownUser(cookie.Value) {
		return ""
	}

	return cookie.Value
}

func knownUser(id string) bool {
	return slices.ContainsFunc(users, func(u demoUser) bool { return u.ID == id })
}

func (s *server) listUsers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, users)
}

func (s *server) chooseUser(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !knownUser(name) {
		http.Error(w, "unknown demo user", http.StatusBadRequest)

		return
	}

	// Readable by the page's script, which shows who it is acting as. A real
	// session cookie is HttpOnly and issued by your authentication.
	http.SetCookie(w, &http.Cookie{Name: userCookie, Value: name, Path: "/", SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

// simulateInvoice stands for the billing system receiving an invoice: it
// creates the invoice and its approval task, which every approver is then
// offered.
func (s *server) simulateInvoice(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	id := fmt.Sprintf("INV-%d", s.nextInvoice)
	s.nextInvoice++
	s.mu.Unlock()

	invoice := invoicing.Invoice{ID: id, Supplier: "Stark Industries", Amount: 4200}

	if err := s.invoices.Save(r.Context(), invoice); err != nil {
		http.Error(w, "save invoice", http.StatusInternalServerError)

		return
	}

	created, err := s.engine.Create(r.Context(), hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoice),
		Correlation: invoicing.Correlation(invoice.ID, invoicing.ActivityApprove),
	})
	if err != nil {
		http.Error(w, "create task", http.StatusInternalServerError)

		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"invoice": invoice.ID, "task": string(created.Task.ID)})
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
