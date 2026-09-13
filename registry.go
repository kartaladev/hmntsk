package hmntsk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"
)

// TypeSpec describes everything that is true of a kind of work rather than of
// one piece of work: what its payloads look like, how urgent it is by default,
// how long it has, what happens when it runs out of time, and who may do it.
//
// A TypeSpec is plain data. That is deliberate: a host written in Go can
// register one through [Define], and a host that is not — or one driven by
// configuration — can register exactly the same thing without Go types.
type TypeSpec struct {
	// Name is the type identifier tasks refer to. Comparison is
	// case-sensitive.
	Name string `json:"name"`
	// Title is a human-readable label for inboxes and forms.
	Title string `json:"title,omitempty"`
	// Description explains the work to whoever has to do it.
	Description string `json:"description,omitempty"`
	// InputSchema is the JSON Schema for the task's input payload. An empty
	// schema accepts anything. Progress saves are checked against a relaxed
	// form of it that drops completeness requirements.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// OutputSchema is the JSON Schema an output must satisfy in full before a
	// task may complete.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// DefaultPriority is the priority a task of this type carries when the
	// caller names none.
	DefaultPriority Priority `json:"defaultPriority,omitempty"`
	// DefaultDeadline is how long a task of this type has, measured from
	// creation, when the caller names no due date. Zero means no deadline.
	DefaultDeadline time.Duration `json:"defaultDeadline,omitempty"`
	// DefaultEscalation is the escalation policy a task of this type carries
	// when the caller supplies none.
	DefaultEscalation *EscalationPolicy `json:"defaultEscalation,omitempty"`
	// DefaultAssignment is the candidate pool a task of this type carries when
	// the caller supplies none.
	DefaultAssignment CandidatePool `json:"defaultAssignment,omitzero"`
	// Metadata is data the host attaches to the type, such as how a client
	// links a task of this type to its business form. The engine stores it and
	// returns it unchanged, and never interprets it: it is not copied onto
	// tasks and cannot be filtered on. It takes part in [TypeSpec.Equal], so
	// re-registering a type with different metadata is a conflict.
	//
	// Keys under the "hmntsk." prefix are reserved for the library's documented
	// conventions, [MetadataFormKey] and [MetadataRoute]. Every other key
	// belongs to the host. The default is no metadata.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Clone returns a deep copy.
func (s TypeSpec) Clone() TypeSpec {
	out := s
	out.InputSchema = cloneRaw(s.InputSchema)
	out.OutputSchema = cloneRaw(s.OutputSchema)
	out.DefaultEscalation = s.DefaultEscalation.Clone()
	out.DefaultAssignment = s.DefaultAssignment.Clone()
	out.Metadata = maps.Clone(s.Metadata)

	return out
}

// Equal reports whether two specifications describe the same type. Schemas are
// compared semantically, so re-registering a type from a reformatted document
// is still an identical registration.
func (s TypeSpec) Equal(other TypeSpec) bool {
	if s.Name != other.Name ||
		s.Title != other.Title ||
		s.Description != other.Description ||
		s.DefaultPriority != other.DefaultPriority ||
		s.DefaultDeadline != other.DefaultDeadline ||
		!s.DefaultEscalation.Equal(other.DefaultEscalation) ||
		!s.DefaultAssignment.Equal(other.DefaultAssignment) ||
		// No metadata and empty metadata compare equal: neither says anything.
		!maps.Equal(s.Metadata, other.Metadata) {
		return false
	}

	return equalJSON(s.InputSchema, other.InputSchema) &&
		equalJSON(s.OutputSchema, other.OutputSchema)
}

// registeredType is a specification together with the schemas compiled from it.
type registeredType struct {
	spec TypeSpec
	// input validates a complete input payload.
	input schemaValidator
	// shape validates a partial input payload: the same schema with every
	// completeness requirement stripped out.
	shape schemaValidator
	// output validates a complete output payload.
	output schemaValidator
}

// Registry holds the task types a host has registered. Registration is
// mandatory: a task whose type is not in the registry cannot be created, so a
// typo in a type name is caught at the boundary rather than surfacing later as
// an orphan task no inbox can render.
//
// A Registry is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	types    map[string]registeredType
	compiler schemaCompiler
}

// NewRegistry returns an empty registry using the built-in JSON Schema
// validator.
func NewRegistry() *Registry {
	return &Registry{
		types:    make(map[string]registeredType),
		compiler: jsonSchemaCompiler{},
	}
}

// Register adds a task type. Registering the same name twice with identical
// configuration succeeds and leaves the type registered once; registering it
// with different configuration fails here, at wiring time, rather than at first
// use.
func (r *Registry) Register(spec TypeSpec) error {
	if spec.Name == "" {
		return &ConfigurationError{Detail: "task type name must not be empty"}
	}

	if !spec.DefaultPriority.Valid() {
		return &ConfigurationError{
			Detail: fmt.Sprintf("task type %q: default priority %d is outside the range %d..%d",
				spec.Name, spec.DefaultPriority, PriorityHighest, PriorityLowest),
		}
	}

	if spec.DefaultDeadline < 0 {
		return &ConfigurationError{
			Detail: fmt.Sprintf("task type %q: default deadline must not be negative", spec.Name),
		}
	}

	entry, err := r.compile(spec.Clone())
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.types[spec.Name]; ok {
		if existing.spec.Equal(spec) {
			return nil
		}

		return &ConfigurationError{
			Detail: fmt.Sprintf("task type %q is already registered with a different configuration", spec.Name),
		}
	}

	r.types[spec.Name] = entry

	return nil
}

// compile builds the three validators a registered type needs.
func (r *Registry) compile(spec TypeSpec) (registeredType, error) {
	input, err := r.compiler.Compile(spec.Name+".input", spec.InputSchema)
	if err != nil {
		return registeredType{}, &ConfigurationError{
			Detail: fmt.Sprintf("task type %q has an invalid input schema: %v", spec.Name, err),
		}
	}

	shape, err := r.compileRelaxed(spec.Name, spec.InputSchema)
	if err != nil {
		return registeredType{}, err
	}

	output, err := r.compiler.Compile(spec.Name+".output", spec.OutputSchema)
	if err != nil {
		return registeredType{}, &ConfigurationError{
			Detail: fmt.Sprintf("task type %q has an invalid output schema: %v", spec.Name, err),
		}
	}

	return registeredType{spec: spec, input: input, shape: shape, output: output}, nil
}

// compileRelaxed compiles the shape-only form of the input schema.
func (r *Registry) compileRelaxed(name string, document json.RawMessage) (schemaValidator, error) {
	if len(bytes.TrimSpace(document)) == 0 {
		return permissiveSchema{}, nil
	}

	var parsed any
	if err := json.Unmarshal(document, &parsed); err != nil {
		return nil, &ConfigurationError{
			Detail: fmt.Sprintf("task type %q has an invalid input schema: %v", name, err),
		}
	}

	relaxed, err := json.Marshal(relaxSchema(parsed))
	if err != nil {
		return nil, &ConfigurationError{
			Detail: fmt.Sprintf("task type %q: cannot derive a shape-only input schema: %v", name, err),
		}
	}

	validator, err := r.compiler.Compile(name+".input.shape", relaxed)
	if err != nil {
		return nil, &ConfigurationError{
			Detail: fmt.Sprintf("task type %q: cannot compile the shape-only input schema: %v", name, err),
		}
	}

	return validator, nil
}

// Lookup returns the specification registered under name. It returns an error
// matching [ErrUnregisteredType] when nothing is registered under that name.
func (r *Registry) Lookup(name string) (TypeSpec, error) {
	entry, err := r.lookup(name)
	if err != nil {
		return TypeSpec{}, err
	}

	return entry.spec.Clone(), nil
}

// lookup returns the registered entry, including its compiled schemas.
func (r *Registry) lookup(name string) (registeredType, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.types[name]
	if !ok {
		return registeredType{}, &UnregisteredTypeError{Type: name}
	}

	return entry, nil
}

// Names returns every registered type name, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := slices.Collect(maps.Keys(r.types))
	sort.Strings(names)

	return names
}

// Has reports whether a type is registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.types[name]

	return ok
}

// ValidateInput checks a complete input payload against the type's input
// schema.
func (r *Registry) ValidateInput(name string, payload json.RawMessage) error {
	entry, err := r.lookup(name)
	if err != nil {
		return err
	}

	return entry.input.Validate("input", payload)
}

// ValidateShape checks a partial input payload: every field present must agree
// with the input schema, but nothing is required to be there. This is what a
// progress save is held to, because a draft is incomplete by definition.
func (r *Registry) ValidateShape(name string, payload json.RawMessage) error {
	entry, err := r.lookup(name)
	if err != nil {
		return err
	}

	return entry.shape.Validate("progress", payload)
}

// ValidateOutput checks a complete output payload against the type's output
// schema, required fields included. This is what completion is held to.
func (r *Registry) ValidateOutput(name string, payload json.RawMessage) error {
	entry, err := r.lookup(name)
	if err != nil {
		return err
	}

	return entry.output.Validate("output", payload)
}

// equalJSON reports whether two JSON documents are semantically equal. Absent
// and empty documents are equal to each other.
func equalJSON(a, b json.RawMessage) bool {
	left, leftOK := canonicalJSON(a)
	right, rightOK := canonicalJSON(b)

	if !leftOK || !rightOK {
		return bytes.Equal(a, b)
	}

	return left == right
}

// canonicalJSON renders a document with object keys sorted, so that two
// documents differing only in formatting compare equal.
func canonicalJSON(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", true
	}

	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false
	}

	out, err := json.Marshal(parsed)
	if err != nil {
		return "", false
	}

	return string(out), true
}
