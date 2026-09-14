// Command http-frameworks serves the same task contract, and the same
// notification handlers, on Gin and on Fiber, and shows that both answer
// every request identically.
//
// The contract (transport/core) is written once. A binding only translates a
// framework's request into the contract's and back, so a policy applied to the
// contract takes effect on every framework that serves it.
//
//	go run ./http-frameworks
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
	"github.com/kartaladev/hmntsk/tasknotify"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
	fibertransport "github.com/kartaladev/hmntsk/transport/fiber"
	gintransport "github.com/kartaladev/hmntsk/transport/gin"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "http-frameworks:", err)
		os.Exit(1)
	}
}

// request is one call, as one actor. {task} stands for the seeded approval.
type request struct {
	actor  string
	method string
	path   string
}

var defaultRequests = []request{
	{invoicing.Alice, http.MethodGet, "/v1/tasks?candidate=me"},
	{invoicing.Alice, http.MethodGet, "/v1/notifications/count"},
	{invoicing.Bob, http.MethodGet, "/v1/tasks?candidate=alice"},
	{invoicing.Alice, http.MethodPost, "/v1/tasks/{task}/claim"},
	{invoicing.Dave, http.MethodGet, "/v1/tasks/{task}"},
	{invoicing.Carol, http.MethodGet, "/v1/tasks?group=finance-approvers"},
	{invoicing.Alice, http.MethodGet, "/v1/nothing-here"},
}

var overrideRequests = []request{
	{invoicing.Carol, http.MethodGet, "/v1/tasks?group=finance-approvers"},
	{invoicing.Alice, http.MethodGet, "/v1/tasks?group=finance-approvers"},
}

func run(ctx context.Context, w io.Writer) error {
	// Release mode keeps gin from printing every route it registers.
	gin.SetMode(gin.ReleaseMode)

	demo.Default(w, "the same contract on gin and on fiber")

	if err := compare(ctx, w, nil, defaultRequests); err != nil {
		return err
	}

	demo.Override(w, "one query policy on the contract, served by both")

	return compare(ctx, w, []transportcore.Option{transportcore.WithQueryAuthorizer(supervisors)}, overrideRequests)
}

// supervisors lets carol read her team's queue and leaves everything else to
// the default. It is configured once, on the contract, not per framework.
var supervisors = transportcore.QueryAuthorizerFunc(
	func(ctx context.Context, actor string, q hmntsk.Query) error {
		if actor == invoicing.Carol && q.Group == invoicing.GroupApprovers && q.Candidate == "" && q.Assignee == "" {
			return nil
		}

		return transportcore.SelfOnly.AuthorizeQuery(ctx, actor, q)
	})

// framework serves a contract and a notification handler, and returns the base
// URL and how to stop serving.
type framework struct {
	name  string
	serve func(api *transportcore.API, notifications http.Handler) (base string, stop func(), err error)
}

var frameworks = []framework{
	{"gin", serveGin},
	{"fiber", serveFiber},
}

// compare runs the same requests against each framework, each over a freshly
// seeded world, so that both start from the same state.
func compare(ctx context.Context, w io.Writer, opts []transportcore.Option, requests []request) error {
	transcripts := make([][]string, 0, len(frameworks))

	for _, fw := range frameworks {
		lines, err := serveAndCall(ctx, fw, opts, requests)
		if err != nil {
			return fmt.Errorf("%s: %w", fw.name, err)
		}

		for _, line := range lines {
			fmt.Fprintf(w, "%-5s %s\n", fw.name, line)
		}

		transcripts = append(transcripts, lines)
	}

	fmt.Fprintf(w, "identical on gin and fiber: %t\n", slices.Equal(transcripts[0], transcripts[1]))

	return nil
}

func serveAndCall(ctx context.Context, fw framework, opts []transportcore.Option, requests []request) ([]string, error) {
	world, err := newWorld(ctx)
	if err != nil {
		return nil, err
	}

	api, err := transportcore.New(world.engine, opts...)
	if err != nil {
		return nil, fmt.Errorf("new api: %w", err)
	}

	notifications, err := notify.NewHandler(world.notifier, world.hub, notify.WithActor(demo.NotifyActor))
	if err != nil {
		return nil, fmt.Errorf("new notification handler: %w", err)
	}

	base, stop, err := fw.serve(api, notifications)
	if err != nil {
		return nil, err
	}
	defer stop()

	lines := make([]string, 0, len(requests))

	for _, r := range requests {
		line, err := call(ctx, base, world, r)
		if err != nil {
			return nil, err
		}

		lines = append(lines, line)
	}

	return lines, nil
}

// serveGin mounts the contract with gin's binding and the notification handler
// through gin.WrapH, because notify's handlers are standard library handlers.
func serveGin(api *transportcore.API, notifications http.Handler) (base string, stop func(), err error) {
	// Engine answers unmatched paths in the contract's error shape. The header
	// stands for your authentication middleware.
	engine, err := gintransport.Engine(api, gintransport.WithActorFunc(func(c *gin.Context) string {
		return demo.Actor(c.Request)
	}))
	if err != nil {
		return "", nil, fmt.Errorf("gin engine: %w", err)
	}

	engine.Any("/v1/notifications", gin.WrapH(notifications))
	engine.Any("/v1/notifications/*path", gin.WrapH(notifications))

	return demo.Serve(engine)
}

// serveFiber mounts the contract with fiber's binding, which translates
// fasthttp requests directly, and the notification handler through Fiber's
// adaptor. The not-found handler goes last: Fiber runs handlers in the order
// they were added, so added earlier it would answer the notification routes.
func serveFiber(api *transportcore.API, notifications http.Handler) (base string, stop func(), err error) {
	app := fiber.New()

	if err := fibertransport.Mount(app, api, fibertransport.WithActorFunc(func(c fiber.Ctx) string {
		return c.Get(demo.ActorHeader)
	})); err != nil {
		return "", nil, fmt.Errorf("fiber mount: %w", err)
	}

	handler := adaptor.HTTPHandler(notifications)
	app.All("/v1/notifications", handler)
	app.All("/v1/notifications/*", handler)
	app.Use(fibertransport.NotFoundHandler())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen: %w", err)
	}

	served := make(chan error, 1)

	go func() { served <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()

	stop = func() {
		_ = app.ShutdownWithContext(context.Background())
		<-served
	}

	return "http://" + listener.Addr().String(), stop, nil
}

// world is one engine and notifier, seeded with an approval for INV-42 whose
// offers have been projected.
type world struct {
	engine   *hmntsk.Service
	notifier *notify.Service
	hub      *notify.Hub
	task     hmntsk.TaskID
	names    *demo.Names
}

func newWorld(ctx context.Context) (*world, error) {
	clock := demo.NewClock(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC))

	engine, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(invoicing.Directory()), hmntsk.WithClock(clock))
	if err != nil {
		return nil, fmt.Errorf("new engine: %w", err)
	}

	if err := invoicing.Register(engine); err != nil {
		return nil, err
	}

	notifier, err := notify.New(notify.NewMemoryStore(), notify.WithClock(clock))
	if err != nil {
		return nil, fmt.Errorf("new notifier: %w", err)
	}

	projector, err := tasknotify.New(engine, notifier)
	if err != nil {
		return nil, fmt.Errorf("new projector: %w", err)
	}

	r, err := relay.NewRelay(engine, relay.WithSinks(projector))
	if err != nil {
		return nil, fmt.Errorf("new relay: %w", err)
	}

	// Counting and listing need no running hub; only a stream does.
	hub, err := notify.NewHub(notifier.Broadcaster())
	if err != nil {
		return nil, fmt.Errorf("new hub: %w", err)
	}

	created, err := engine.Create(ctx, hmntsk.CreateRequest{
		Type:        invoicing.ApproveType,
		Actor:       "billing-service",
		Input:       invoicing.Input(invoicing.Invoice{ID: "INV-42", Supplier: "Acme Paper", Amount: 1299}),
		Correlation: invoicing.Correlation("INV-42", invoicing.ActivityApprove),
	})
	if err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}

	if _, err := r.Relay(ctx); err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}

	names := demo.NewNames()
	names.Name(string(created.Task.ID), "approval")

	return &world{engine: engine, notifier: notifier, hub: hub, task: created.Task.ID, names: names}, nil
}

// call makes one request and prints "actor METHOD path → status summary".
func call(ctx context.Context, base string, wd *world, r request) (string, error) {
	path := strings.ReplaceAll(r.path, "{task}", string(wd.task))

	req, err := http.NewRequestWithContext(ctx, r.method, base+path, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}

	req.Header.Set(demo.ActorHeader, r.actor)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", r.method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	summary, err := summarise(r.method, path, resp.StatusCode, raw)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", r.method, path, err)
	}

	return wd.names.Mask(fmt.Sprintf("%s %s %s → %d %s", r.actor, r.method, path, resp.StatusCode, summary)), nil
}

func summarise(method, path string, status int, raw []byte) (string, error) {
	switch {
	case status != http.StatusOK:
		var failure transportcore.ErrorResponse
		if err := json.Unmarshal(raw, &failure); err != nil {
			return "", fmt.Errorf("decode error body %q: %w", raw, err)
		}

		return string(failure.Error.Code), nil
	case strings.HasPrefix(path, "/v1/notifications/count"):
		var count struct {
			Count int64 `json:"count"`
		}
		if err := json.Unmarshal(raw, &count); err != nil {
			return "", fmt.Errorf("decode count: %w", err)
		}

		return fmt.Sprintf("count=%d", count.Count), nil
	case method == http.MethodPost:
		var task transportcore.TaskResponse
		if err := json.Unmarshal(raw, &task); err != nil {
			return "", fmt.Errorf("decode task: %w", err)
		}

		return string(task.Status), nil
	default:
		var page transportcore.PageResponse
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", fmt.Errorf("decode page: %w", err)
		}

		refs := make([]string, 0, len(page.Tasks))
		for _, t := range page.Tasks {
			refs = append(refs, t.Correlation.OwnerRef)
		}

		return strings.Join(refs, " "), nil
	}
}
