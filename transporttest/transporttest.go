// Package transporttest is the behavioural suite every hmntsk transport binding
// must pass.
//
// Three frameworks serve one contract. Writing their tests independently would
// produce three sets of nearly-identical cases and no way to tell whether a
// client could really move between them, which is the whole claim the contract
// makes. One exported suite makes the claim checkable: each binding's test file
// starts a server and calls [RunSuite].
//
// Every case talks to the binding over real HTTP rather than through an
// in-process handler. That is deliberate: one of the supported frameworks is
// not built on the standard library's HTTP types at all, and a suite that
// assumed an http.Handler could not reach it without the conversion layer the
// whole design exists to avoid.
package transporttest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// ActorHeader is where each binding's test harness reads the acting actor from.
//
// It is a header only because a test needs some way to say who is calling. In a
// real deployment the actor comes from whatever the host's middleware
// established — a session, a token, a header it trusts — and the engine never
// looks at the request itself.
const ActorHeader = "X-Hmntsk-Actor"

// Actors the suite exercises the contract as.
const (
	// Alice is eligible for the suite's tasks.
	Alice = "alice"
	// Bob is eligible too, and is the wrong actor for work Alice holds.
	Bob = "bob"
	// Carol is outside the pool.
	Carol = "carol"
	// Owner stands for whoever asked for the work.
	Owner = "owner"
)

// Binding is a mounted API, reachable over HTTP.
type Binding struct {
	// BaseURL is the scheme, host and port the API is served at, with no
	// trailing slash. The contract's own base path is appended to it.
	BaseURL string
}

// Mount serves an API and returns where it became reachable. It may register
// cleanup with t.
//
// The harness a binding supplies is responsible for reading [ActorHeader] and
// handing the actor to the binder, exactly as a host's own middleware would
// hand over whoever it authenticated.
type Mount func(t *testing.T, api *transportcore.API) Binding

// ApprovalInputSchema is the input schema of the suite's main task type.
const ApprovalInputSchema = `{
  "type": "object",
  "properties": {
    "amount":        {"type": "number"},
    "justification": {"type": "string"}
  },
  "required": ["amount", "justification"]
}`

// ApprovalOutputSchema is its output schema.
const ApprovalOutputSchema = `{
  "type": "object",
  "properties": {
    "approved": {"type": "boolean"},
    "note":     {"type": "string"}
  },
  "required": ["approved"]
}`

// approvalMetadata is the metadata the suite's approval type is registered
// with: both well-known keys, and one of the host's own.
func approvalMetadata() map[string]string {
	return map[string]string{
		hmntsk.MetadataFormKey: "approval-form",
		hmntsk.MetadataRoute:   "/approvals/{correlation.ownerRef}?task={task.id}",
		"acme.icon":            "receipt",
	}
}

// NewAPI builds the engine and the contract the suite exercises, configured by
// opts. With no options the contract's defaults apply, the self-only query
// policy included.
//
// It is exported so that a binding's own tests can wire the same engine when
// they need to check something outside the shared cases.
func NewAPI(t *testing.T, opts ...transportcore.Option) *transportcore.API {
	t.Helper()

	registry := hmntsk.NewRegistry()

	require.NoError(t, registry.Register(hmntsk.TypeSpec{
		Name:            "approval",
		Title:           "Approval",
		Description:     "Sign off on spending.",
		InputSchema:     json.RawMessage(ApprovalInputSchema),
		OutputSchema:    json.RawMessage(ApprovalOutputSchema),
		DefaultPriority: hmntsk.PriorityDefault,
		DefaultDeadline: time.Hour,
		DefaultEscalation: &hmntsk.EscalationPolicy{
			Action: hmntsk.EscalationWiden, AddGroups: []string{"managers"},
		},
		Metadata: approvalMetadata(),
	}))

	require.NoError(t, registry.Register(hmntsk.TypeSpec{Name: "note", Title: "Note"}))

	svc, err := hmntsk.New(memstore.New(),
		hmntsk.WithRegistry(registry),
		hmntsk.WithGroupResolver(hmntsk.NewStaticAssignment(map[string][]string{
			"finance-approvers": {Alice, Bob},
			"managers":          {Carol},
		})),
	)
	require.NoError(t, err)

	api, err := transportcore.New(svc, opts...)
	require.NoError(t, err)

	return api
}

// RunSuite runs every case against the binding the mount serves.
//
// A binding that passes it serves the same routes, the same shapes and the same
// status codes as every other, which is what lets a host swap one for another
// without its clients noticing.
func RunSuite(t *testing.T, mount Mount) {
	t.Helper()

	t.Run("Lifecycle", func(t *testing.T) { runLifecycleCases(t, mount) })
	t.Run("Errors", func(t *testing.T) { runErrorCases(t, mount) })
	t.Run("Payloads", func(t *testing.T) { runPayloadCases(t, mount) })
	t.Run("Inbox", func(t *testing.T) { runInboxCases(t, mount) })
	t.Run("TaskTypes", func(t *testing.T) { runTaskTypeCases(t, mount) })
	t.Run("Host", func(t *testing.T) { runHostCases(t, mount) })
}
