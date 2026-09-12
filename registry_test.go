package hmntsk_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

const approvalInputSchema = `{
  "type": "object",
  "properties": {
    "amount":        {"type": "number"},
    "justification": {"type": "string"},
    "requester":     {"type": "object", "properties": {"id": {"type": "string"}}, "required": ["id"]}
  },
  "required": ["amount", "justification"]
}`

const approvalOutputSchema = `{
  "type": "object",
  "properties": {
    "approved": {"type": "boolean"},
    "note":     {"type": "string"}
  },
  "required": ["approved"]
}`

func approvalSpec() hmntsk.TypeSpec {
	return hmntsk.TypeSpec{
		Name:            "approval",
		Title:           "Approval",
		InputSchema:     json.RawMessage(approvalInputSchema),
		OutputSchema:    json.RawMessage(approvalOutputSchema),
		DefaultPriority: hmntsk.PriorityDefault,
		DefaultDeadline: 24 * time.Hour,
		DefaultEscalation: &hmntsk.EscalationPolicy{
			Action: hmntsk.EscalationWiden, AddGroups: []string{"managers"},
		},
		DefaultAssignment: hmntsk.CandidatePool{Groups: []string{"finance-approvers"}},
	}
}

func registryWithApproval(t *testing.T) *hmntsk.Registry {
	t.Helper()

	registry := hmntsk.NewRegistry()
	require.NoError(t, registry.Register(approvalSpec()))

	return registry
}

func TestRegistryLookup(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		typeName string
		assert   func(t *testing.T, spec hmntsk.TypeSpec, err error)
	}

	cases := []testCase{
		{
			name:     "a registered type is returned with its schemas and defaults",
			typeName: "approval",
			assert: func(t *testing.T, spec hmntsk.TypeSpec, err error) {
				require.NoError(t, err)
				assert.JSONEq(t, approvalInputSchema, string(spec.InputSchema),
					"the schemas must come back as supplied at registration")
				assert.JSONEq(t, approvalOutputSchema, string(spec.OutputSchema))
				assert.Equal(t, 24*time.Hour, spec.DefaultDeadline)
			},
		},
		{
			name:     "an unregistered type is refused by name",
			typeName: "approvel",
			assert: func(t *testing.T, _ hmntsk.TypeSpec, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnregisteredType)
				assert.ErrorIs(t, err, hmntsk.ErrValidation,
					"the task-types capability calls this a validation error")
				assert.Contains(t, err.Error(), "approvel", "the error must name the unknown type")
			},
		},
		{
			name:     "type names are case-sensitive",
			typeName: "Approval",
			assert: func(t *testing.T, _ hmntsk.TypeSpec, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnregisteredType)
			},
		},
		{
			name:     "the empty type name is not registered",
			typeName: "",
			assert: func(t *testing.T, _ hmntsk.TypeSpec, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnregisteredType)
			},
		},
	}

	registry := registryWithApproval(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec, err := registry.Lookup(tc.typeName)
			tc.assert(t, spec, err)
		})
	}
}

func TestRegistryRegisterConflicts(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		second hmntsk.TypeSpec
		assert func(t *testing.T, registry *hmntsk.Registry, err error)
	}

	accepted := func(t *testing.T, registry *hmntsk.Registry, err error) {
		require.NoError(t, err)
		assert.Equal(t, []string{"approval"}, registry.Names(),
			"an identical re-registration must leave the type registered once")
	}

	rejected := func(t *testing.T, _ *hmntsk.Registry, err error) {
		require.ErrorIs(t, err, hmntsk.ErrConfiguration)
		assert.Contains(t, err.Error(), "approval", "the error must identify the conflicting type name")
	}

	cases := []testCase{
		{
			name:   "an identical re-registration is accepted",
			second: approvalSpec(),
			assert: accepted,
		},
		{
			name: "a re-registration differing only in schema formatting is accepted",
			second: func() hmntsk.TypeSpec {
				spec := approvalSpec()
				spec.InputSchema = json.RawMessage(
					`{"required":["amount","justification"],"type":"object","properties":` +
						`{"justification":{"type":"string"},"amount":{"type":"number"},` +
						`"requester":{"required":["id"],"type":"object","properties":{"id":{"type":"string"}}}}}`)

				return spec
			}(),
			assert: accepted,
		},
		{
			name: "a differing input schema is rejected",
			second: func() hmntsk.TypeSpec {
				spec := approvalSpec()
				spec.InputSchema = json.RawMessage(`{"type":"object"}`)

				return spec
			}(),
			assert: rejected,
		},
		{
			name: "a differing default deadline is rejected",
			second: func() hmntsk.TypeSpec {
				spec := approvalSpec()
				spec.DefaultDeadline = time.Hour

				return spec
			}(),
			assert: rejected,
		},
		{
			name: "a differing escalation policy is rejected",
			second: func() hmntsk.TypeSpec {
				spec := approvalSpec()
				spec.DefaultEscalation = &hmntsk.EscalationPolicy{Action: hmntsk.EscalationSupersede}

				return spec
			}(),
			assert: rejected,
		},
		{
			name: "a differing default assignment is rejected",
			second: func() hmntsk.TypeSpec {
				spec := approvalSpec()
				spec.DefaultAssignment = hmntsk.CandidatePool{Users: []string{"alice"}}

				return spec
			}(),
			assert: rejected,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry := registryWithApproval(t)
			tc.assert(t, registry, registry.Register(tc.second))
		})
	}
}

func TestRegistryRegisterRejectsUnusableSpecifications(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		spec   hmntsk.TypeSpec
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "an unnamed type is refused",
			spec: hmntsk.TypeSpec{},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
		{
			name: "a schema that is not JSON is refused",
			spec: hmntsk.TypeSpec{Name: "broken", InputSchema: json.RawMessage(`{"type":`)},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
		{
			name: "a schema that is not a schema is refused",
			spec: hmntsk.TypeSpec{Name: "broken", OutputSchema: json.RawMessage(`{"type":42}`)},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
		{
			name: "a priority outside the scale is refused",
			spec: hmntsk.TypeSpec{Name: "urgent", DefaultPriority: hmntsk.PriorityLowest + 1},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
		{
			name: "a negative deadline is refused",
			spec: hmntsk.TypeSpec{Name: "backwards", DefaultDeadline: -time.Hour},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
			},
		},
		{
			name: "a type with no schemas at all is accepted",
			spec: hmntsk.TypeSpec{Name: "freeform"},
			assert: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.NewRegistry().Register(tc.spec))
		})
	}
}

func TestRegistryValidationScope(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		validate func(registry *hmntsk.Registry) error
		assert   func(t *testing.T, err error)
	}

	valid := func(t *testing.T, err error) { require.NoError(t, err) }

	invalidAt := func(pointer string) func(t *testing.T, err error) {
		return func(t *testing.T, err error) {
			require.ErrorIs(t, err, hmntsk.ErrValidation)

			var validation *hmntsk.ValidationError

			require.ErrorAs(t, err, &validation)
			require.NotEmpty(t, validation.Issues)

			pointers := make([]string, 0, len(validation.Issues))
			for _, issue := range validation.Issues {
				pointers = append(pointers, issue.Pointer)
			}

			assert.Contains(t, pointers, pointer, "the error must identify the offending field")
		}
	}

	cases := []testCase{
		{
			name: "a half-filled draft is saved",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateShape("approval", json.RawMessage(`{"amount":100}`))
			},
			assert: valid,
		},
		{
			name: "an empty draft is saved",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateShape("approval", json.RawMessage(`{}`))
			},
			assert: valid,
		},
		{
			name: "a draft missing a nested required field is saved",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateShape("approval", json.RawMessage(`{"requester":{}}`))
			},
			assert: valid,
		},
		{
			name: "a wrongly typed field is refused on save",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateShape("approval", json.RawMessage(`{"amount":"a lot"}`))
			},
			assert: invalidAt("/amount"),
		},
		{
			name: "a wrongly typed nested field is refused on save",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateShape("approval", json.RawMessage(`{"requester":{"id":7}}`))
			},
			assert: invalidAt("/requester/id"),
		},
		{
			name: "a complete input is accepted",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateInput("approval",
					json.RawMessage(`{"amount":100,"justification":"new laptop"}`))
			},
			assert: valid,
		},
		{
			name: "an incomplete input is refused in full validation",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateInput("approval", json.RawMessage(`{"amount":100}`))
			},
			assert: invalidAt(""),
		},
		{
			name: "a complete output is accepted",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateOutput("approval", json.RawMessage(`{"approved":false}`))
			},
			assert: valid,
		},
		{
			name: "an output missing a required field is refused on completion",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateOutput("approval", json.RawMessage(`{"note":"looks fine"}`))
			},
			assert: invalidAt(""),
		},
		{
			name: "a wrongly typed output field is refused on completion",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateOutput("approval", json.RawMessage(`{"approved":"yes"}`))
			},
			assert: invalidAt("/approved"),
		},
		{
			name: "validating against an unregistered type is refused",
			validate: func(registry *hmntsk.Registry) error {
				return registry.ValidateOutput("nope", json.RawMessage(`{}`))
			},
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, hmntsk.ErrUnregisteredType)
			},
		},
	}

	registry := registryWithApproval(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.validate(registry))
		})
	}
}

// TestRegistryShapeValidationDoesNotStripAPropertyCalledRequired guards the
// schema-aware relaxation walk against a blind search for the keyword.
func TestRegistryShapeValidationDoesNotStripAPropertyCalledRequired(t *testing.T) {
	t.Parallel()

	registry := hmntsk.NewRegistry()
	require.NoError(t, registry.Register(hmntsk.TypeSpec{
		Name: "form",
		InputSchema: json.RawMessage(`{
		  "type": "object",
		  "properties": {"required": {"type": "boolean"}},
		  "required": ["required"]
		}`),
	}))

	assert.NoError(t, registry.ValidateShape("form", json.RawMessage(`{}`)),
		"the completeness requirement must be relaxed")
	assert.Error(t, registry.ValidateShape("form", json.RawMessage(`{"required":"yes"}`)),
		"the property that happens to be called \"required\" must still be type-checked")
}

func TestTypeSpecNewTaskAppliesDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	explicitDue := testNow.Add(90 * time.Minute)
	explicitDeadline := 2 * time.Hour
	highest := hmntsk.PriorityHighest

	type testCase struct {
		name    string
		request hmntsk.CreateRequest
		assert  func(t *testing.T, task hmntsk.Task)
	}

	cases := []testCase{
		{
			name:    "defaults are applied when nothing is supplied",
			request: hmntsk.CreateRequest{Type: "approval"},
			assert: func(t *testing.T, task hmntsk.Task) {
				assert.Equal(t, hmntsk.StatusCreated, task.Status)
				assert.Equal(t, hmntsk.PriorityDefault, task.Priority)
				require.NotNil(t, task.DueAt)
				assert.Equal(t, hmntsk.NormalizeTime(testNow.Add(24*time.Hour)), *task.DueAt)
				assert.Equal(t, []string{"finance-approvers"}, task.Candidates.Groups)
				require.NotNil(t, task.Escalation)
				assert.Equal(t, hmntsk.EscalationWiden, task.Escalation.Action)
			},
		},
		{
			name:    "an explicit due date wins over the type default",
			request: hmntsk.CreateRequest{Type: "approval", DueAt: &explicitDue},
			assert: func(t *testing.T, task hmntsk.Task) {
				require.NotNil(t, task.DueAt)
				assert.Equal(t, hmntsk.NormalizeTime(explicitDue), *task.DueAt)
			},
		},
		{
			name: "an explicit due date wins over an explicit interval too",
			request: hmntsk.CreateRequest{
				Type: "approval", DueAt: &explicitDue, Deadline: &explicitDeadline,
			},
			assert: func(t *testing.T, task hmntsk.Task) {
				require.NotNil(t, task.DueAt)
				assert.Equal(t, hmntsk.NormalizeTime(explicitDue), *task.DueAt)
			},
		},
		{
			name:    "an explicit interval wins over the type default",
			request: hmntsk.CreateRequest{Type: "approval", Deadline: &explicitDeadline},
			assert: func(t *testing.T, task hmntsk.Task) {
				require.NotNil(t, task.DueAt)
				assert.Equal(t, hmntsk.NormalizeTime(testNow.Add(2*time.Hour)), *task.DueAt)
			},
		},
		{
			name:    "the most urgent priority is not mistaken for an absent one",
			request: hmntsk.CreateRequest{Type: "approval", Priority: &highest},
			assert: func(t *testing.T, task hmntsk.Task) {
				assert.Equal(t, hmntsk.PriorityHighest, task.Priority)
			},
		},
		{
			name: "an explicit candidate pool replaces the type default",
			request: hmntsk.CreateRequest{
				Type:       "approval",
				Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
			},
			assert: func(t *testing.T, task hmntsk.Task) {
				assert.Equal(t, []string{"alice"}, task.Candidates.Users)
				assert.Empty(t, task.Candidates.Groups)
			},
		},
		{
			name: "the payload is stored exactly as supplied",
			request: hmntsk.CreateRequest{
				Type:  "approval",
				Input: json.RawMessage(`{"zulu":1,"amount":9007199254740993}`),
			},
			assert: func(t *testing.T, task hmntsk.Task) {
				assertVerbatim(t, `{"zulu":1,"amount":9007199254740993}`, task.Input)
			},
		},
	}

	spec := approvalSpec()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, spec.NewTask("task-1", tc.request, testNow))
		})
	}
}

func TestTypeSpecNewTaskWithoutADeadlineDefault(t *testing.T) {
	t.Parallel()

	spec := hmntsk.TypeSpec{Name: "freeform"}
	task := spec.NewTask("task-1", hmntsk.CreateRequest{Type: "freeform"}, testNow)

	assert.Nil(t, task.DueAt, "a type with no deadline default gives a task with no deadline")
}
