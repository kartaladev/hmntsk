package hmntsk_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// ApprovalInput and ApprovalOutput are the Go shapes of the approval type used
// through the typed path.
type ApprovalInput struct {
	Amount        int64  `json:"amount"`
	Justification string `json:"justification"`
	Note          string `json:"note,omitempty"`
}

type ApprovalOutput struct {
	Approved bool   `json:"approved"`
	Note     string `json:"note,omitempty"`
}

// typedHarness wires a service with one typed handle over the in-memory store.
type typedHarness struct {
	svc      *hmntsk.Service
	store    *memstore.Store
	recorder *recorder
	kind     hmntsk.Kind[ApprovalInput, ApprovalOutput]
}

func newTypedHarness(t *testing.T, extra ...hmntsk.EventHandler) *typedHarness {
	t.Helper()

	store := memstore.New()
	rec := &recorder{}

	handlers := append([]hmntsk.EventHandler{rec}, extra...)

	svc, err := hmntsk.New(store,
		hmntsk.WithGroupResolver(testDirectory()),
		hmntsk.WithClock(hmntsk.ClockFunc(func() time.Time { return testNow })),
		hmntsk.WithEventHandlers(handlers...),
	)
	require.NoError(t, err)

	kind, err := hmntsk.Define[ApprovalInput, ApprovalOutput](svc, hmntsk.TypeSpec{
		Name:            "approval",
		Title:           "Approval",
		DefaultPriority: hmntsk.PriorityDefault,
	})
	require.NoError(t, err)

	return &typedHarness{svc: svc, store: store, recorder: rec, kind: kind}
}

func TestKindTypedAndUntypedCreationAgree(t *testing.T) {
	t.Parallel()

	h := newTypedHarness(t)
	ctx := t.Context()

	input := ApprovalInput{Amount: 9007199254740993, Justification: "new laptop"}

	pool := hmntsk.CandidatePool{Groups: []string{"finance-approvers"}}
	base := hmntsk.CreateRequest{
		Actor:       "system",
		Candidates:  &pool,
		Correlation: hmntsk.CorrelationData{OwnerType: "process", OwnerRef: "p-1"},
	}

	typedResult, err := h.kind.Create(ctx, input, base)
	require.NoError(t, err)

	raw, err := json.Marshal(input)
	require.NoError(t, err)

	untypedReq := base
	untypedReq.Type = "approval"
	untypedReq.Input = raw

	untypedResult, err := h.svc.Create(ctx, untypedReq)
	require.NoError(t, err)

	assertVerbatim(t, string(raw), typedResult.Task.Input,
		"the typed path must store exactly the bytes the untyped path would")
	assertVerbatim(t, string(untypedResult.Task.Input), typedResult.Task.Input)

	assert.Equal(t, untypedResult.Task.Type, typedResult.Task.Type)
	assert.Equal(t, untypedResult.Task.Status, typedResult.Task.Status)
	assert.Equal(t, untypedResult.Task.Priority, typedResult.Task.Priority)
	assert.Equal(t, untypedResult.Task.Candidates, typedResult.Task.Candidates)
	assert.Equal(t, untypedResult.Task.Correlation, typedResult.Task.Correlation)

	assert.Contains(t, string(typedResult.Task.Input), "9007199254740993",
		"a 64-bit amount must not be rounded through a float64")
}

func TestKindGet(t *testing.T) {
	t.Parallel()

	h := newTypedHarness(t)
	ctx := t.Context()
	pool := hmntsk.CandidatePool{Users: []string{"alice"}}

	created, err := h.kind.Create(ctx,
		ApprovalInput{Amount: 100, Justification: "new laptop"},
		hmntsk.CreateRequest{Candidates: &pool})
	require.NoError(t, err)

	typed, err := h.kind.Get(ctx, created.Task.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(100), typed.Input.Amount)
	assert.Equal(t, "new laptop", typed.Input.Justification)
	assert.Equal(t, hmntsk.StatusReserved, typed.Task.Status)
	assert.False(t, typed.Output.Approved, "an unfinished task has a zero output")

	_, err = h.svc.Start(ctx, hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: "alice"})
	require.NoError(t, err)

	_, err = h.kind.Complete(ctx, ApprovalOutput{Approved: false, Note: "over budget"},
		hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: "alice"})
	require.NoError(t, err)

	finished, err := h.kind.Get(ctx, created.Task.ID)
	require.NoError(t, err)
	assert.Equal(t, hmntsk.StatusCompleted, finished.Task.Status,
		"a denial is still a completion")
	assert.False(t, finished.Output.Approved)
	assert.Equal(t, "over budget", finished.Output.Note)
}

func TestKindGetRefusesAnotherType(t *testing.T) {
	t.Parallel()

	h := newTypedHarness(t)
	ctx := t.Context()

	require.NoError(t, h.svc.Register(hmntsk.TypeSpec{Name: "freeform"}))

	pool := hmntsk.CandidatePool{Users: []string{"alice"}}

	other, err := h.svc.Create(ctx, hmntsk.CreateRequest{
		Type: "freeform", Input: json.RawMessage(`{"anything":1}`), Candidates: &pool,
	})
	require.NoError(t, err)

	_, err = h.kind.Get(ctx, other.Task.ID)
	require.ErrorIs(t, err, hmntsk.ErrValidation,
		"decoding another type's payload into the wrong struct would silently zero it")
}

func TestKindOnCompleted(t *testing.T) {
	t.Parallel()

	type observed struct {
		event  hmntsk.Event
		output ApprovalOutput
	}

	var seen []observed

	store := memstore.New()

	svc, err := hmntsk.New(store,
		hmntsk.WithGroupResolver(testDirectory()),
		hmntsk.WithClock(hmntsk.ClockFunc(func() time.Time { return testNow })),
	)
	require.NoError(t, err)

	kind, err := hmntsk.Define[ApprovalInput, ApprovalOutput](svc, hmntsk.TypeSpec{Name: "approval"})
	require.NoError(t, err)

	// A second service over the same store, wired with the typed handler, so
	// that the handler can be built from the handle it observes.
	handled, err := hmntsk.New(store,
		hmntsk.WithRegistry(svc.Registry()),
		hmntsk.WithGroupResolver(testDirectory()),
		hmntsk.WithClock(hmntsk.ClockFunc(func() time.Time { return testNow })),
		hmntsk.WithEventHandlers(kind.OnCompleted(
			func(_ context.Context, event hmntsk.Event, output ApprovalOutput) error {
				seen = append(seen, observed{event: event, output: output})

				return nil
			},
		)),
	)
	require.NoError(t, err)

	ctx := t.Context()
	pool := hmntsk.CandidatePool{Users: []string{"alice"}}

	created, err := handled.Create(ctx, hmntsk.CreateRequest{
		Type:       "approval",
		Input:      json.RawMessage(`{"amount":1,"justification":"x"}`),
		Candidates: &pool,
	})
	require.NoError(t, err)

	require.NoError(t, handled.Register(hmntsk.TypeSpec{Name: "freeform"}))

	otherPool := hmntsk.CandidatePool{Users: []string{"alice"}}

	other, err := handled.Create(ctx, hmntsk.CreateRequest{
		Type: "freeform", Input: json.RawMessage(`{}`), Candidates: &otherPool,
	})
	require.NoError(t, err)

	_, err = handled.Start(ctx, hmntsk.TaskRequest{TaskID: other.Task.ID, Actor: "alice"})
	require.NoError(t, err)

	_, err = handled.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: other.Task.ID, Actor: "alice"},
		Output:      json.RawMessage(`{"whatever":true}`),
	})
	require.NoError(t, err)

	assert.Empty(t, seen, "a completion of another task type must not reach this handler")

	_, err = handled.Start(ctx, hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: "alice"})
	require.NoError(t, err)

	_, err = handled.Complete(ctx, hmntsk.CompleteRequest{
		TaskRequest: hmntsk.TaskRequest{TaskID: created.Task.ID, Actor: "alice"},
		Output:      json.RawMessage(`{"approved":true,"note":"fine"}`),
	})
	require.NoError(t, err)

	require.Len(t, seen, 1)
	assert.Equal(t, hmntsk.EventTypeCompleted, seen[0].event.Type)
	assert.True(t, seen[0].output.Approved)
	assert.Equal(t, "fine", seen[0].output.Note)
}

func TestDefineConflicts(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		define func(svc *hmntsk.Service) error
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "an identical redefinition is accepted",
			define: func(svc *hmntsk.Service) error {
				_, err := hmntsk.Define[ApprovalInput, ApprovalOutput](svc,
					hmntsk.TypeSpec{Name: "approval", Title: "Approval"})

				return err
			},
			assert: func(t *testing.T, err error) { require.NoError(t, err) },
		},
		{
			name: "redefining with different Go types fails at registration",
			define: func(svc *hmntsk.Service) error {
				type otherInput struct {
					Amount string `json:"amount"`
				}

				_, err := hmntsk.Define[otherInput, ApprovalOutput](svc,
					hmntsk.TypeSpec{Name: "approval", Title: "Approval"})

				return err
			},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.Contains(t, err.Error(), "approval")
			},
		},
		{
			name: "redefining with a different title fails at registration",
			define: func(svc *hmntsk.Service) error {
				_, err := hmntsk.Define[ApprovalInput, ApprovalOutput](svc,
					hmntsk.TypeSpec{Name: "approval", Title: "Sign-off"})

				return err
			},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
		{
			name: "an unnamed type is refused",
			define: func(svc *hmntsk.Service) error {
				_, err := hmntsk.Define[ApprovalInput, ApprovalOutput](svc, hmntsk.TypeSpec{})

				return err
			},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(testDirectory()))
			require.NoError(t, err)

			_, err = hmntsk.Define[ApprovalInput, ApprovalOutput](svc,
				hmntsk.TypeSpec{Name: "approval", Title: "Approval"})
			require.NoError(t, err)

			tc.assert(t, tc.define(svc))
		})
	}
}

func TestDefineDerivesSchemasButKeepsExplicitOnes(t *testing.T) {
	t.Parallel()

	svc, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(testDirectory()))
	require.NoError(t, err)

	_, err = hmntsk.Define[ApprovalInput, ApprovalOutput](svc, hmntsk.TypeSpec{Name: "derived"})
	require.NoError(t, err)

	derived, err := svc.Registry().Lookup("derived")
	require.NoError(t, err)
	assert.JSONEq(t, `{
	  "type": "object",
	  "properties": {
	    "amount":        {"type": "integer"},
	    "justification": {"type": "string"},
	    "note":          {"type": "string"}
	  },
	  "required": ["amount", "justification"]
	}`, string(derived.InputSchema),
		"a field tagged omitempty is optional; the others are required")

	assert.NoError(t, svc.Registry().ValidateInput("derived",
		json.RawMessage(`{"amount":1,"justification":"x","extra":true}`)),
		"a derived schema must not forbid fields it does not describe")

	require.Error(t, svc.Registry().ValidateInput("derived", json.RawMessage(`{"amount":1}`)))

	explicit := json.RawMessage(`{"type":"object","properties":{"amount":{"type":"integer","minimum":10}}}`)

	_, err = hmntsk.Define[ApprovalInput, ApprovalOutput](svc, hmntsk.TypeSpec{
		Name: "explicit", InputSchema: explicit,
	})
	require.NoError(t, err)

	kept, err := svc.Registry().Lookup("explicit")
	require.NoError(t, err)
	assert.JSONEq(t, string(explicit), string(kept.InputSchema),
		"an explicit schema can say things a Go type cannot, so it is kept unchanged")
}

func TestHeterogeneousQueriesRemainAvailableAlongsideTypedHandles(t *testing.T) {
	t.Parallel()

	h := newTypedHarness(t)
	ctx := t.Context()

	require.NoError(t, h.svc.Register(hmntsk.TypeSpec{Name: "freeform"}))

	pool := hmntsk.CandidatePool{Groups: []string{"finance-approvers"}}

	_, err := h.kind.Create(ctx,
		ApprovalInput{Amount: 100, Justification: "new laptop"},
		hmntsk.CreateRequest{Candidates: &pool})
	require.NoError(t, err)

	_, err = h.svc.Create(ctx, hmntsk.CreateRequest{
		Type:       "freeform",
		Input:      json.RawMessage(`{"zulu":1,"seq":9007199254740993}`),
		Candidates: &pool,
	})
	require.NoError(t, err)

	page, err := h.svc.Query(ctx, hmntsk.Query{Candidate: "alice"})
	require.NoError(t, err)
	require.Len(t, page.Tasks, 2, "one inbox holds both types at once")

	byType := make(map[string]hmntsk.Task, 2)
	for _, task := range page.Tasks {
		byType[task.Type] = task
	}

	require.Contains(t, byType, "approval")
	require.Contains(t, byType, "freeform")
	assertVerbatim(t, `{"zulu":1,"seq":9007199254740993}`, byType["freeform"].Input,
		"the untyped payload must survive a query made while typed handles are in use")
	assert.JSONEq(t, `{"amount":100,"justification":"new laptop"}`, string(byType["approval"].Input))
}
