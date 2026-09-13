package transportcore

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// DefaultBasePath is where the contract is served unless the host says
// otherwise.
//
// The version is in the path from the first release rather than retrofitted.
// A bad Go API can be deprecated and a bad column can be migrated, but a URL
// that is wrong is wrong for every client that ever calls it.
const DefaultBasePath = "/v1"

// API is the REST contract over an engine.
type API struct {
	service    *hmntsk.Service
	basePath   string
	authorizer QueryAuthorizer
}

// Option configures an [API].
type Option func(*API)

// WithBasePath serves the contract somewhere other than [DefaultBasePath].
func WithBasePath(path string) Option {
	return func(a *API) {
		trimmed := strings.TrimRight(path, "/")
		if trimmed != "" {
			a.basePath = trimmed
		}
	}
}

// New returns the contract over an engine.
//
// With no options it serves under [DefaultBasePath] and authorizes queries with
// [SelfOnly], so an actor may read only their own inbox. A contradictory or
// empty option, such as a nil query policy, is a configuration error here,
// before any traffic.
func New(service *hmntsk.Service, opts ...Option) (*API, error) {
	if service == nil {
		return nil, &hmntsk.ConfigurationError{Detail: "a service is required to serve the API"}
	}

	api := &API{service: service, basePath: DefaultBasePath, authorizer: SelfOnly}

	for _, opt := range opts {
		opt(api)
	}

	if api.authorizer == nil {
		return nil, &hmntsk.ConfigurationError{
			Detail: "WithQueryAuthorizer was given no policy; pass transportcore.AllowAll to permit every query",
		}
	}

	return api, nil
}

// BasePath returns the prefix every route is served under.
func (a *API) BasePath() string { return a.basePath }

// Service returns the engine behind the contract.
func (a *API) Service() *hmntsk.Service { return a.service }

// Routes returns the whole contract, in a stable order.
//
// This table is the contract. The OpenAPI document is generated from it, so the
// two cannot drift apart, and every binder registers exactly what is here, so
// no binding can quietly serve a route the others do not.
func (a *API) Routes() []Route {
	return []Route{
		{
			Method: "POST", Pattern: a.path("/tasks"),
			OperationID: "createTask", Summary: "Create a task.",
			Handler: a.createTask,
		},
		{
			Method: "GET", Pattern: a.path("/tasks"),
			OperationID: "queryTasks",
			Summary:     "Query tasks by assignee, eligibility, group, status, type or correlation, in a supported ordering.",
			Handler:     a.queryTasks,
		},
		{
			// Before /tasks/{id}, so that a router matching in registration
			// order does not read "count" as a task identifier.
			Method: "GET", Pattern: a.path("/tasks/count"),
			OperationID: "countTasks", Summary: "Count the tasks a query matches, such as for an inbox badge.",
			Handler: a.countTasks,
		},
		{
			Method: "GET", Pattern: a.path("/tasks/{id}"),
			OperationID: "getTask", Summary: "Read one task.",
			Handler: a.getTask,
		},
		{
			Method: "DELETE", Pattern: a.path("/tasks/{id}"),
			OperationID: "cancelTask", Summary: "Cancel a task.",
			Handler: a.operation(hmntsk.OpCancel),
		},
		{
			Method: "GET", Pattern: a.path("/tasks/{id}/history"),
			OperationID: "getTaskHistory", Summary: "Read a task's transition history.",
			Handler: a.getHistory,
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/claim"),
			OperationID: "claimTask", Summary: "Reserve a pooled task.",
			Handler: a.operation(hmntsk.OpClaim),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/release"),
			OperationID: "releaseTask", Summary: "Return a reserved task to its pool.",
			Handler: a.operation(hmntsk.OpRelease),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/start"),
			OperationID: "startTask", Summary: "Begin work on a reserved task.",
			Handler: a.operation(hmntsk.OpStart),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/progress"),
			OperationID: "saveTaskProgress", Summary: "Apply an RFC 6902 patch to a task's saved progress.",
			Handler: a.operation(hmntsk.OpSaveProgress),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/complete"),
			OperationID: "completeTask", Summary: "Close a task with an output payload.",
			Handler: a.operation(hmntsk.OpComplete),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/fail"),
			OperationID: "failTask", Summary: "Record that the actor could not do the work.",
			Handler: a.operation(hmntsk.OpFail),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/delegate"),
			OperationID: "delegateTask", Summary: "Reassign a task to another eligible actor.",
			Handler: a.operation(hmntsk.OpDelegate),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/suspend"),
			OperationID: "suspendTask", Summary: "Withdraw a task from circulation.",
			Handler: a.operation(hmntsk.OpSuspend),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/resume"),
			OperationID: "resumeTask", Summary: "Return a suspended task to the state it came from.",
			Handler: a.operation(hmntsk.OpResume),
		},
		{
			Method: "POST", Pattern: a.path("/tasks/{id}/escalate"),
			OperationID: "escalateTask", Summary: "Apply a task's escalation policy.",
			Handler: a.operation(hmntsk.OpEscalate),
		},
		{
			Method: "GET", Pattern: a.path("/task-types"),
			OperationID: "listTaskTypes", Summary: "List the registered task types.",
			Handler: a.listTaskTypes,
		},
		{
			Method: "GET", Pattern: a.path("/task-types/{name}"),
			OperationID: "getTaskType", Summary: "Read a task type's input and output schemas.",
			Handler: a.getTaskType,
		},
	}
}

// path prefixes a route with the base path.
func (a *API) path(suffix string) string { return a.basePath + suffix }

// createTask answers POST /tasks.
func (a *API) createTask(ctx context.Context, req Request) Response {
	var body CreateTaskRequest

	if err := decode(req.Body, &body); err != nil {
		return fail(err)
	}

	create := hmntsk.CreateRequest{
		Type:        body.Type,
		Actor:       req.Actor,
		ID:          hmntsk.TaskID(body.ID),
		Input:       body.Input,
		DueAt:       body.DueAt,
		Candidates:  body.Candidates,
		Escalation:  body.Escalation,
		Correlation: body.Correlation,
		Callback:    body.Callback,
	}

	if body.Priority != nil {
		priority := hmntsk.Priority(*body.Priority)
		create.Priority = &priority
	}

	if body.DeadlineSeconds != nil {
		deadline := time.Duration(*body.DeadlineSeconds) * time.Second
		create.Deadline = &deadline
	}

	result, err := a.service.Create(ctx, create)
	if err != nil {
		return fail(err)
	}

	return a.respond(ctx, StatusCreated, result)
}

// getTask answers GET /tasks/{id}.
func (a *API) getTask(ctx context.Context, req Request) Response {
	task, err := a.service.Get(ctx, hmntsk.TaskID(req.Param("id")))
	if err != nil {
		return fail(err)
	}

	return encode(StatusOK, task)
}

// getHistory answers GET /tasks/{id}/history.
func (a *API) getHistory(ctx context.Context, req Request) Response {
	id := hmntsk.TaskID(req.Param("id"))

	// A history read on a task that does not exist is a 404, not an empty log.
	if _, err := a.service.Get(ctx, id); err != nil {
		return fail(err)
	}

	records, err := a.service.History(ctx, id)
	if err != nil {
		return fail(err)
	}

	if records == nil {
		records = []hmntsk.TransitionRecord{}
	}

	return encode(StatusOK, HistoryResponse{Records: records})
}

// queryTasks answers GET /tasks.
func (a *API) queryTasks(ctx context.Context, req Request) Response {
	query, err := a.authorizedQuery(ctx, req)
	if err != nil {
		return fail(err)
	}

	page, err := a.service.Query(ctx, query)
	if err != nil {
		return fail(err)
	}

	tasks := page.Tasks
	if tasks == nil {
		tasks = []hmntsk.Task{}
	}

	return encode(StatusOK, PageResponse{Tasks: tasks, NextCursor: page.NextCursor})
}

// countTasks answers GET /tasks/count.
func (a *API) countTasks(ctx context.Context, req Request) Response {
	query, err := a.authorizedQuery(ctx, req)
	if err != nil {
		return fail(err)
	}

	count, err := a.service.Count(ctx, query)
	if err != nil {
		return fail(err)
	}

	return encode(StatusOK, CountResponse{Count: count})
}

// authorizedQuery turns a query request into the query the engine runs, in the
// order the contract promises: parse it, resolve [Me] against the acting user,
// then ask the policy. A malformed request is a 400 before anyone is asked
// whether it may run, and the policy always sees the actor a query names rather
// than the word me.
func (a *API) authorizedQuery(ctx context.Context, req Request) (hmntsk.Query, error) {
	query, err := parseQuery(req)
	if err != nil {
		return hmntsk.Query{}, err
	}

	for _, named := range []*string{&query.Candidate, &query.Assignee} {
		if *named != Me {
			continue
		}

		if req.Actor == "" {
			return hmntsk.Query{}, &queryRefusedError{
				cause: errors.New("the query names me, but no acting user is established"),
			}
		}

		*named = req.Actor
	}

	if err := a.authorizer.AuthorizeQuery(ctx, req.Actor, query); err != nil {
		return hmntsk.Query{}, &queryRefusedError{cause: err}
	}

	return query, nil
}

// listTaskTypes answers GET /task-types.
func (a *API) listTaskTypes(_ context.Context, _ Request) Response {
	names := a.service.Registry().Names()

	types := make([]TaskTypeResponse, 0, len(names))

	for _, name := range names {
		spec, err := a.service.Registry().Lookup(name)
		if err != nil {
			continue
		}

		types = append(types, taskTypeResponse(spec))
	}

	return encode(StatusOK, TaskTypeListResponse{Types: types})
}

// getTaskType answers GET /task-types/{name}.
func (a *API) getTaskType(_ context.Context, req Request) Response {
	spec, err := a.service.Registry().Lookup(req.Param("name"))
	if err != nil {
		// A type nobody registered is a 404 when it is the thing being
		// addressed, even though it is a 400 when it is named in a create.
		return encode(StatusNotFound, ErrorResponse{Error: ErrorDetail{
			Code:     CodeNotFound,
			Message:  err.Error(),
			TaskType: req.Param("name"),
		}})
	}

	return encode(StatusOK, taskTypeResponse(spec))
}

// operation answers every lifecycle route. They differ only in which engine
// method they call and which fields of the body they read, so they share one
// handler rather than thirteen near-copies.
func (a *API) operation(op hmntsk.Operation) Handler {
	return func(ctx context.Context, req Request) Response {
		var body OperationRequest

		if err := decode(req.Body, &body); err != nil {
			return fail(err)
		}

		task := hmntsk.TaskRequest{
			TaskID:  hmntsk.TaskID(req.Param("id")),
			Actor:   req.Actor,
			Version: body.Version,
			Comment: body.Comment,
		}

		result, err := a.invoke(ctx, op, task, body)
		if err != nil {
			return fail(err)
		}

		return a.respond(ctx, StatusOK, result)
	}
}

// invoke calls the engine method an operation names.
func (a *API) invoke(
	ctx context.Context,
	op hmntsk.Operation,
	task hmntsk.TaskRequest,
	body OperationRequest,
) (hmntsk.Result, error) {
	switch op {
	case hmntsk.OpClaim:
		return a.service.Claim(ctx, task)
	case hmntsk.OpRelease:
		return a.service.Release(ctx, task)
	case hmntsk.OpStart:
		return a.service.Start(ctx, task)
	case hmntsk.OpSaveProgress:
		return a.service.SaveProgress(ctx, hmntsk.SaveProgressRequest{
			TaskRequest: task, Patch: body.Patch,
		})
	case hmntsk.OpComplete:
		return a.service.Complete(ctx, hmntsk.CompleteRequest{
			TaskRequest: task, Output: body.Output,
		})
	case hmntsk.OpFail:
		return a.service.Fail(ctx, task)
	case hmntsk.OpDelegate:
		return a.service.Delegate(ctx, hmntsk.DelegateRequest{
			TaskRequest: task, Target: body.Delegate,
		})
	case hmntsk.OpSuspend:
		return a.service.Suspend(ctx, task)
	case hmntsk.OpResume:
		return a.service.Resume(ctx, task)
	case hmntsk.OpEscalate:
		return a.service.Escalate(ctx, task)
	case hmntsk.OpCancel:
		return a.service.Cancel(ctx, task)
	default:
		return hmntsk.Result{}, badRequest("", "operation "+op.String()+" is not reachable over HTTP")
	}
}

// respond dispatches any pending events and renders the task.
//
// The dispatch is a no-op unless the host wrapped the handler in a transaction
// of its own, in which case the engine handed the delivery back and this is
// where it happens.
func (a *API) respond(ctx context.Context, status int, result hmntsk.Result) Response {
	_ = result.Dispatch(ctx)

	return encode(status, result.Task)
}

// parseQuery turns query parameters into an engine query.
func parseQuery(req Request) (hmntsk.Query, error) {
	query := hmntsk.Query{
		Assignee:    req.QueryValue("assignee"),
		Candidate:   req.QueryValue("candidate"),
		Types:       req.QueryValues("type"),
		OwnerType:   req.QueryValue("ownerType"),
		OwnerRef:    req.QueryValue("ownerRef"),
		ActivityKey: req.QueryValue("activityKey"),
		Group:       req.QueryValue("group"),
		Cursor:      req.QueryValue("cursor"),
	}

	ordering, ok := parseOrdering(req.QueryValue("orderBy"))
	if !ok {
		return hmntsk.Query{}, badRequest("orderBy", "must be one of created, priority, due or urgency")
	}

	query.OrderBy = ordering

	switch req.QueryValue("direction") {
	case "", "asc":
	case "desc":
		query.Descending = true
	default:
		return hmntsk.Query{}, badRequest("direction", "must be asc or desc")
	}

	for _, status := range req.QueryValues("status") {
		parsed := hmntsk.Status(status)
		if !parsed.Valid() {
			return hmntsk.Query{}, badRequest("status", "is not a task status: "+status)
		}

		query.Statuses = append(query.Statuses, parsed)
	}

	if raw := req.QueryValue("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return hmntsk.Query{}, badRequest("limit", "must be a non-negative whole number")
		}

		query.Limit = limit
	}

	if raw := req.QueryValue("dueBefore"); raw != "" {
		instant, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return hmntsk.Query{}, badRequest("dueBefore", "must be an RFC 3339 timestamp")
		}

		query.DueBefore = &instant
	}

	return query, nil
}

// parseOrdering reads the orderBy parameter. Creation order is spelled created
// on the wire, and is also what an absent parameter means.
func parseOrdering(value string) (hmntsk.Ordering, bool) {
	switch value {
	case "", "created":
		return hmntsk.OrderCreated, true
	case string(hmntsk.OrderPriority), string(hmntsk.OrderDue), string(hmntsk.OrderUrgency):
		return hmntsk.Ordering(value), true
	default:
		return "", false
	}
}

// decode reads a request body, treating an absent one as an empty object so
// that an operation with nothing to say needs no body at all.
func decode(body []byte, into any) error {
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}

	if err := json.Unmarshal(body, into); err != nil {
		return badRequest("", "the request body is not valid JSON: "+err.Error())
	}

	return nil
}

// encode renders a response body.
func encode(status int, value any) Response {
	body, err := json.Marshal(value)
	if err != nil {
		return Response{
			Status: StatusInternalServerError,
			Body: []byte(`{"error":{"code":"internal",` +
				`"message":"the response could not be encoded"}}`),
		}
	}

	return Response{Status: status, Body: body}
}

// fail renders an engine error.
func fail(err error) Response {
	return encode(StatusFor(err), ErrorResponse{Error: errorDetail(err)})
}

// NotFoundResponse is what a binder returns for a path this contract does not
// serve, so that an unknown route answers in the contract's own shape rather
// than the framework's.
func NotFoundResponse() Response {
	return encode(StatusNotFound, ErrorResponse{Error: ErrorDetail{
		Code:    CodeNotFound,
		Message: "no such route",
	}})
}
