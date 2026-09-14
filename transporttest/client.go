package transporttest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// Client talks to a mounted binding over HTTP.
type Client struct {
	baseURL  string
	basePath string
	http     *http.Client
}

// NewClient starts the binding over a fresh engine and returns a client for it.
// Options configure the contract, such as its query authorization policy; with
// none, the contract's defaults apply.
func NewClient(t *testing.T, mount Mount, opts ...transportcore.Option) (*Client, *transportcore.API) {
	t.Helper()

	api := NewAPI(t, opts...)

	return clientFor(t, mount, api), api
}

// clientFor mounts an API the caller built and returns a client for it.
func clientFor(t *testing.T, mount Mount, api *transportcore.API) *Client {
	t.Helper()

	binding := mount(t, api)

	require.NotEmpty(t, binding.BaseURL, "a binding must say where it is reachable")

	return &Client{
		baseURL:  binding.BaseURL,
		basePath: api.BasePath(),
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Result is one HTTP answer.
type Result struct {
	// Status is the response status code.
	Status int
	// Body is the raw response body, kept raw so that a payload can be
	// compared byte for byte.
	Body []byte
	// ContentType is what the binding said the body was.
	ContentType string
	// Allow is every Allow header the binding sent, which for this contract is
	// always none.
	Allow []string

	challenge string
}

// Decode reads the body into a value.
func (r Result) Decode(t *testing.T, into any) {
	t.Helper()

	require.NoErrorf(t, json.Unmarshal(r.Body, into), "decode response body: %s", r.Body)
}

// Task decodes the body as a task.
func (r Result) Task(t *testing.T) hmntsk.Task {
	t.Helper()

	var task hmntsk.Task

	r.Decode(t, &task)

	return task
}

// ContentTypeChallenge returns the authentication challenge the response
// carried, which for this contract is always none: authentication belongs to
// the host.
func (r Result) ContentTypeChallenge() string { return r.challenge }

// Error decodes the body as the contract's error shape.
func (r Result) Error(t *testing.T) transportcore.ErrorDetail {
	t.Helper()

	var body transportcore.ErrorResponse

	r.Decode(t, &body)

	return body.Error
}

// Do sends a request to a path below the contract's base path.
//
// body may be nil, a []byte or a json.RawMessage to send verbatim, or any value
// to marshal. Sending raw bytes matters: several cases exist to prove a payload
// survives, and re-encoding it here would prove nothing.
func (c *Client) Do(t *testing.T, method, path, actor string, body any) Result {
	t.Helper()

	var payload []byte

	switch typed := body.(type) {
	case nil:
	case []byte:
		payload = typed
	case json.RawMessage:
		payload = typed
	case string:
		payload = []byte(typed)
	default:
		encoded, err := json.Marshal(typed)
		require.NoError(t, err)

		payload = encoded
	}

	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(
		context.WithoutCancel(t.Context()), method, c.baseURL+c.basePath+path, reader)
	require.NoError(t, err)

	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	if actor != "" {
		request.Header.Set(ActorHeader, actor)
	}

	response, err := c.http.Do(request)
	require.NoErrorf(t, err, "%s %s", method, path)

	defer func() { _ = response.Body.Close() }()

	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return Result{
		Status:      response.StatusCode,
		Body:        responseBody,
		ContentType: response.Header.Get("Content-Type"),
		Allow:       response.Header.Values("Allow"),
		challenge:   response.Header.Get("WWW-Authenticate"),
	}
}

// DoAbsolute sends a request to a path that is not below the base path.
func (c *Client) DoAbsolute(t *testing.T, method, path, actor string) Result {
	t.Helper()

	request, err := http.NewRequestWithContext(
		context.WithoutCancel(t.Context()), method, c.baseURL+path, http.NoBody)
	require.NoError(t, err)

	if actor != "" {
		request.Header.Set(ActorHeader, actor)
	}

	response, err := c.http.Do(request)
	require.NoError(t, err)

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return Result{
		Status:      response.StatusCode,
		Body:        body,
		ContentType: response.Header.Get("Content-Type"),
		Allow:       response.Header.Values("Allow"),
	}
}

// CreateApproval creates a pooled approval task and returns it.
func (c *Client) CreateApproval(t *testing.T, mutate ...func(body map[string]any)) hmntsk.Task {
	t.Helper()

	body := map[string]any{
		"type":  "approval",
		"input": json.RawMessage(`{"amount":100,"justification":"new laptop"}`),
		"candidates": map[string]any{
			"groups": []string{"finance-approvers"},
		},
		"correlation": map[string]any{"ownerType": "process", "ownerRef": "p-1"},
	}

	for _, apply := range mutate {
		apply(body)
	}

	result := c.Do(t, http.MethodPost, "/tasks", Owner, body)
	require.Equalf(t, transportcore.StatusCreated, result.Status,
		"create must answer 201: %s", result.Body)

	return result.Task(t)
}
