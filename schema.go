package hmntsk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaValidator checks one document against one compiled schema. It is an
// internal seam: the engine does not commit to a particular JSON Schema
// implementation in its public API, so the validator can be replaced without a
// breaking change.
type schemaValidator interface {
	// Validate reports every way document fails the schema. It returns a
	// *ValidationError naming subject, or nil when the document is valid.
	Validate(subject string, document json.RawMessage) error
}

// schemaCompiler turns a JSON Schema document into a [schemaValidator].
type schemaCompiler interface {
	// Compile compiles document, which must be a JSON Schema. id is used only
	// to make error messages locatable.
	Compile(id string, document json.RawMessage) (schemaValidator, error)
}

// permissiveSchema accepts every document. It stands in for an absent schema,
// so that a type may omit one without every call site testing for nil.
type permissiveSchema struct{}

// Validate implements [schemaValidator].
func (permissiveSchema) Validate(string, json.RawMessage) error { return nil }

// jsonSchemaCompiler is the default [schemaCompiler], backed by
// santhosh-tekuri/jsonschema.
type jsonSchemaCompiler struct{}

// Compile implements [schemaCompiler].
func (jsonSchemaCompiler) Compile(id string, document json.RawMessage) (schemaValidator, error) {
	if len(bytes.TrimSpace(document)) == 0 {
		return permissiveSchema{}, nil
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("hmntsk: parse schema %q: %w", id, err)
	}

	return compileDocument(id, doc)
}

// compileDocument compiles an already-parsed schema document.
func compileDocument(id string, doc any) (schemaValidator, error) {
	url := "https://hmntsk.invalid/" + id

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("hmntsk: add schema %q: %w", id, err)
	}

	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("hmntsk: compile schema %q: %w", id, err)
	}

	return &jsonSchema{compiled: compiled}, nil
}

// jsonSchema adapts a compiled jsonschema.Schema to [schemaValidator].
type jsonSchema struct {
	compiled *jsonschema.Schema
}

// Validate implements [schemaValidator].
func (s *jsonSchema) Validate(subject string, document json.RawMessage) error {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return &ValidationError{
			Subject: subject,
			Issues:  []ValidationIssue{{Detail: "is not well-formed JSON: " + err.Error()}},
		}
	}

	err = s.compiled.Validate(instance)
	if err == nil {
		return nil
	}

	var validationErr *jsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return fmt.Errorf("hmntsk: validate %s: %w", subject, err)
	}

	return &ValidationError{Subject: subject, Issues: collectIssues(validationErr.BasicOutput())}
}

// collectIssues flattens a basic output unit into the engine's issue list, in
// document order and without duplicates.
func collectIssues(unit *jsonschema.OutputUnit) []ValidationIssue {
	if unit == nil {
		return nil
	}

	issues := make([]ValidationIssue, 0, len(unit.Errors)+1)
	seen := make(map[ValidationIssue]bool, len(unit.Errors)+1)

	var walk func(u jsonschema.OutputUnit)

	walk = func(u jsonschema.OutputUnit) {
		if u.Error != nil {
			issue := ValidationIssue{Pointer: u.InstanceLocation, Detail: u.Error.String()}
			if !seen[issue] {
				seen[issue] = true

				issues = append(issues, issue)
			}
		}

		for _, child := range u.Errors {
			walk(child)
		}
	}

	walk(*unit)

	if len(issues) == 0 {
		issues = append(issues, ValidationIssue{Detail: "does not satisfy the schema"})
	}

	return issues
}

// schemaKeywordsWithSchemaValues are keywords whose value is itself a schema.
var schemaKeywordsWithSchemaValues = []string{
	"additionalItems", "additionalProperties", "contains", "contentSchema",
	"else", "if", "items", "not", "propertyNames", "then",
	"unevaluatedItems", "unevaluatedProperties",
}

// schemaKeywordsWithSchemaMaps are keywords whose value maps names to schemas.
// Their keys are names chosen by the schema author and must never be treated as
// keywords themselves.
var schemaKeywordsWithSchemaMaps = []string{
	"$defs", "definitions", "dependentSchemas", "patternProperties", "properties",
}

// schemaKeywordsWithSchemaArrays are keywords whose value is a list of schemas.
var schemaKeywordsWithSchemaArrays = []string{
	"allOf", "anyOf", "oneOf", "prefixItems",
}

// completenessKeywords are the keywords that demand a document be complete, as
// opposed to merely consistent.
var completenessKeywords = []string{
	"dependentRequired", "minItems", "minProperties", "required",
}

// relaxSchema returns a copy of a JSON Schema document with every completeness
// requirement removed, at every depth. What survives still constrains the types,
// formats and values of whatever is present.
//
// This is what makes a half-filled form saveable: the input schema says an
// approval needs a decision and a justification, and a draft that has neither
// is still checked for a decision that is a string where a boolean belongs.
//
// The walk is schema-aware rather than a blind search for the word "required",
// because a schema may legitimately describe a property called "required".
func relaxSchema(node any) any {
	switch typed := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))

		for key, value := range typed {
			if slices.Contains(completenessKeywords, key) {
				continue
			}

			switch {
			case slices.Contains(schemaKeywordsWithSchemaValues, key):
				out[key] = relaxSchema(value)
			case slices.Contains(schemaKeywordsWithSchemaMaps, key):
				out[key] = relaxSchemaMap(value)
			case slices.Contains(schemaKeywordsWithSchemaArrays, key):
				out[key] = relaxSchemaArray(value)
			default:
				out[key] = value
			}
		}

		return out
	default:
		return node
	}
}

// relaxSchemaMap relaxes every schema in a name-to-schema map.
func relaxSchemaMap(node any) any {
	typed, ok := node.(map[string]any)
	if !ok {
		return node
	}

	out := make(map[string]any, len(typed))
	for name, schema := range typed {
		out[name] = relaxSchema(schema)
	}

	return out
}

// relaxSchemaArray relaxes every schema in a list of schemas.
func relaxSchemaArray(node any) any {
	typed, ok := node.([]any)
	if !ok {
		return node
	}

	out := make([]any, 0, len(typed))
	for _, schema := range typed {
		out = append(out, relaxSchema(schema))
	}

	return out
}
