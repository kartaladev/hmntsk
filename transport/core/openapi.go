package transportcore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/kartaladev/hmntsk"
)

// OpenAPIVersion is the specification version the generated document declares.
const OpenAPIVersion = "3.1.0"

// componentSchemas are the document's schemas, derived from the Go types rather
// than written out.
//
// Deriving them is the point: a hand-maintained document drifts from the code
// the first time somebody adds a field in a hurry, and a REST contract is one
// of the three surfaces this project cannot fix with a deprecation.
func componentSchemas() (map[string]any, error) {
	samples := map[string]any{
		"CreateTaskRequest":    CreateTaskRequest{},
		"OperationRequest":     OperationRequest{},
		"Task":                 hmntsk.Task{},
		"PageResponse":         PageResponse{},
		"CountResponse":        CountResponse{},
		"HistoryResponse":      HistoryResponse{},
		"TaskTypeResponse":     TaskTypeResponse{},
		"TaskTypeListResponse": TaskTypeListResponse{},
		"ErrorResponse":        ErrorResponse{},
	}

	schemas := make(map[string]any, len(samples))

	for name, sample := range samples {
		document, err := hmntsk.DeriveSchema(sample)
		if err != nil {
			return nil, fmt.Errorf("transportcore: derive the schema for %s: %w", name, err)
		}

		var decoded any
		if err := json.Unmarshal(document, &decoded); err != nil {
			return nil, fmt.Errorf("transportcore: read the schema for %s: %w", name, err)
		}

		schemas[name] = decoded
	}

	return schemas, nil
}

// responseShape says what one operation returns.
type responseShape struct {
	success       int
	successSchema string
	failures      []int
}

// responseShapes describes every operation's answers. Lifecycle operations
// share one shape because they share one contract.
var responseShapes = map[string]responseShape{
	"createTask": {
		success: StatusCreated, successSchema: "Task",
		failures: []int{StatusBadRequest, StatusInternalServerError},
	},
	"queryTasks": {
		success: StatusOK, successSchema: "PageResponse",
		failures: []int{StatusBadRequest, StatusForbidden, StatusInternalServerError},
	},
	"countTasks": {
		success: StatusOK, successSchema: "CountResponse",
		failures: []int{StatusBadRequest, StatusForbidden, StatusInternalServerError},
	},
	"getTask": {
		success: StatusOK, successSchema: "Task",
		failures: []int{StatusNotFound},
	},
	"getTaskHistory": {
		success: StatusOK, successSchema: "HistoryResponse",
		failures: []int{StatusNotFound},
	},
	"listTaskTypes": {success: StatusOK, successSchema: "TaskTypeListResponse"},
	"getTaskType": {
		success: StatusOK, successSchema: "TaskTypeResponse",
		failures: []int{StatusNotFound},
	},
}

// lifecycleShape is what every operation that changes a task answers.
var lifecycleShape = responseShape{
	success: StatusOK, successSchema: "Task",
	failures: []int{
		StatusBadRequest, StatusForbidden, StatusNotFound,
		StatusConflict, StatusInternalServerError,
	},
}

// statusDescriptions explains each code the contract uses, so that the document
// says why a client would see it rather than only that it might.
var statusDescriptions = map[int]string{
	StatusOK:                  "The operation succeeded.",
	StatusCreated:             "The task was created.",
	StatusBadRequest:          "The request or its payload failed validation, or named a task type that is not registered.",
	StatusForbidden:           "The acting actor is not eligible for the task or is not its assignee, or the query authorization policy refused the query. By default an actor may query only their own inbox.",
	StatusNotFound:            "No such task, task type or route.",
	StatusConflict:            "The task has moved on since the version the caller observed, or the operation is not legal from its present state. The body carries the current version.",
	StatusInternalServerError: "The request could not be completed. A failure to resolve group membership lands here rather than on 403: the engine could not decide, which is not the same as deciding against the caller.",
}

// OpenAPI renders the contract as an OpenAPI document, generated from the route
// table so that the two cannot drift apart.
func (a *API) OpenAPI() ([]byte, error) {
	schemas, err := componentSchemas()
	if err != nil {
		return nil, err
	}

	paths := make(map[string]any)

	for _, route := range a.Routes() {
		pathItem, _ := paths[route.Pattern].(map[string]any)
		if pathItem == nil {
			pathItem = make(map[string]any)
		}

		pathItem[strings.ToLower(route.Method)] = operationObject(route)
		paths[route.Pattern] = pathItem
	}

	document := map[string]any{
		"openapi": OpenAPIVersion,
		"info": map[string]any{
			"title": "hmntsk human task API",
			"description": "The task inbox and lifecycle contract of the hmntsk engine. " +
				"Identical across every supported web framework binding. " +
				"Authentication, authorisation of the caller's identity, rate limiting, " +
				"CORS and TLS belong to the host; the engine takes the acting actor as " +
				"an input the host establishes.",
			"version": "1.0.0",
		},
		"paths":      paths,
		"components": map[string]any{"schemas": schemas},
	}

	var out bytes.Buffer

	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("transportcore: encode the OpenAPI document: %w", err)
	}

	return out.Bytes(), nil
}

// operationObject renders one route.
func operationObject(route Route) map[string]any {
	operation := map[string]any{
		"operationId": route.OperationID,
		"summary":     route.Summary,
		"responses":   responsesFor(route),
	}

	if params := parameterObjects(route); len(params) > 0 {
		operation["parameters"] = params
	}

	if body := requestBodyFor(route); body != nil {
		operation["requestBody"] = body
	}

	return operation
}

// parameterObjects renders a route's path parameters, plus the query
// parameters the inbox accepts. A count takes the query's filters and none of
// its paging or ordering.
func parameterObjects(route Route) []any {
	params := make([]any, 0, 4)

	for _, name := range route.Params() {
		params = append(params, map[string]any{
			"name":     name,
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
	}

	if route.OperationID != "queryTasks" && route.OperationID != "countTasks" {
		return params
	}

	queries := filterParams
	if route.OperationID == "queryTasks" {
		queries = append(slices.Clip(filterParams), pagingParams...)
	}

	for _, query := range queries {
		params = append(params, map[string]any{
			"name":        query.name,
			"in":          "query",
			"required":    false,
			"schema":      query.schema,
			"description": query.description,
		})
	}

	return params
}

// queryParam describes one query parameter of the inbox routes.
type queryParam struct {
	name        string
	schema      map[string]any
	description string
}

// filterParams are the parameters that choose which tasks match. A query and a
// count both take them.
var filterParams = []queryParam{
	{"assignee", map[string]any{"type": "string"}, "Tasks reserved for this actor; `me` names the acting user."},
	{"candidate", map[string]any{"type": "string"}, "Tasks this actor may act on, with group membership resolved now; `me` names the acting user."},
	{"group", map[string]any{"type": "string"}, "Tasks whose candidate pool names this group, as configured: no membership is resolved."},
	{"status", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "Restrict to these lifecycle states."},
	{"type", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "Restrict to these task types."},
	{"ownerType", map[string]any{"type": "string"}, "Correlation: the kind of thing that owns the task."},
	{"ownerRef", map[string]any{"type": "string"}, "Correlation: the owning unit of work."},
	{"activityKey", map[string]any{"type": "string"}, "Correlation: the step the task stands for."},
	{"dueBefore", map[string]any{"type": "string", "format": "date-time"}, "Tasks due strictly before this instant."},
}

// pagingParams are the parameters that shape a page. Only a query takes them.
var pagingParams = []queryParam{
	{"limit", map[string]any{"type": "integer"}, "Page size."},
	{"cursor", map[string]any{"type": "string"}, "Continue a previous page. It is bound to the ordering and direction that produced it."},
	{"orderBy", map[string]any{"type": "string", "enum": []any{"created", "priority", "due", "urgency"}}, "The ordering: creation by default; urgency is priority, then due date, then creation."},
	{"direction", map[string]any{"type": "string", "enum": []any{"asc", "desc"}}, "The direction of the ordering, asc by default. Tasks without a deadline sort last either way."},
}

// requestBodyFor renders the body a route accepts, and nil for one that takes
// none.
func requestBodyFor(route Route) map[string]any {
	var schema string

	switch {
	case route.OperationID == "createTask":
		schema = "CreateTaskRequest"
	case route.Method == "POST" || route.Method == "DELETE":
		schema = "OperationRequest"
	default:
		return nil
	}

	return map[string]any{
		"required": route.OperationID == "createTask",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/" + schema},
			},
		},
	}
}

// responsesFor renders a route's answers.
func responsesFor(route Route) map[string]any {
	shape, ok := responseShapes[route.OperationID]
	if !ok {
		shape = lifecycleShape
	}

	responses := map[string]any{
		itoa(shape.success): map[string]any{
			"description": statusDescriptions[shape.success],
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{"$ref": "#/components/schemas/" + shape.successSchema},
				},
			},
		},
	}

	failures := append([]int(nil), shape.failures...)
	sort.Ints(failures)

	for _, status := range failures {
		responses[itoa(status)] = map[string]any{
			"description": statusDescriptions[status],
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{"$ref": "#/components/schemas/ErrorResponse"},
				},
			},
		}
	}

	return responses
}

// itoa renders a status code as a string key.
func itoa(status int) string { return fmt.Sprintf("%d", status) }
