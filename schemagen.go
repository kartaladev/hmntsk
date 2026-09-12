package hmntsk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// timeType and rawMessageType are the two standard-library types the deriver
// treats specially rather than structurally.
var (
	timeType       = reflect.TypeOf(time.Time{})
	rawMessageType = reflect.TypeOf(json.RawMessage{})
)

// DeriveSchema produces a JSON Schema from a Go type.
//
// It exists so that [Define] can register a type whose payloads are Go structs
// without the host restating their shape as a schema document. It is a
// convenience and not an authority: hand a [TypeSpec] an explicit schema
// whenever the real contract is richer than the Go type — enumerations, string
// formats, numeric bounds, conditional requirements — and the explicit one is
// used unchanged.
//
// What it derives:
//
//   - a struct becomes an object whose properties follow the json tags, with
//     every field that is neither a pointer nor tagged omitempty or omitzero
//     listed as required;
//   - additionalProperties is deliberately left open, because payloads are
//     stored as supplied and fields the schema does not describe must survive a
//     round trip;
//   - time.Time becomes a date-time string, json.RawMessage and any/interface
//     become an unconstrained schema, and []byte becomes a string, matching how
//     encoding/json actually treats them;
//   - a recursive type stops at the cycle with an unconstrained schema rather
//     than looping.
//
// The result is a document, not a compiled schema, so a caller can inspect or
// amend it before registering.
func DeriveSchema(sample any) (json.RawMessage, error) {
	typ := reflect.TypeOf(sample)
	if typ == nil {
		return json.RawMessage(`{}`), nil
	}

	node := deriveNode(typ, map[reflect.Type]bool{})

	document, err := json.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("hmntsk: derive schema for %s: %w", typ, err)
	}

	return document, nil
}

// deriveNode builds the schema node for one type. seen holds the struct types
// already on the stack, so a recursive type terminates.
func deriveNode(typ reflect.Type, seen map[reflect.Type]bool) map[string]any {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case rawMessageType:
		return map[string]any{}
	}

	switch typ.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 && typ.Kind() == reflect.Slice {
			// encoding/json renders a byte slice as a base64 string.
			return map[string]any{"type": "string"}
		}

		return map[string]any{"type": "array", "items": deriveNode(typ.Elem(), seen)}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": deriveNode(typ.Elem(), seen),
		}
	case reflect.Struct:
		return deriveStruct(typ, seen)
	case reflect.Interface, reflect.Invalid, reflect.Uintptr, reflect.Complex64,
		reflect.Complex128, reflect.Chan, reflect.Func, reflect.Pointer, reflect.UnsafePointer:
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

// deriveStruct builds the object node for a struct type.
func deriveStruct(typ reflect.Type, seen map[reflect.Type]bool) map[string]any {
	if seen[typ] {
		return map[string]any{}
	}

	seen[typ] = true
	defer delete(seen, typ)

	properties := make(map[string]any)
	required := make([]string, 0, typ.NumField())

	collectFields(typ, seen, properties, &required)

	node := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		node["required"] = required
	}

	return node
}

// collectFields walks a struct's exported fields, flattening embedded structs
// that encoding/json would flatten.
func collectFields(typ reflect.Type, seen map[reflect.Type]bool, properties map[string]any, required *[]string) {
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		name, opts, skip := jsonFieldName(field)
		if skip {
			continue
		}

		if field.Anonymous && name == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}

			if embedded.Kind() == reflect.Struct {
				collectFields(embedded, seen, properties, required)

				continue
			}
		}

		if name == "" {
			name = field.Name
		}

		properties[name] = deriveNode(field.Type, seen)

		optional := field.Type.Kind() == reflect.Pointer ||
			strings.Contains(opts, "omitempty") ||
			strings.Contains(opts, "omitzero")

		if !optional {
			*required = append(*required, name)
		}
	}
}

// jsonFieldName reads a field's json tag, returning its name, its options, and
// whether encoding/json would skip it.
func jsonFieldName(field reflect.StructField) (name, opts string, skip bool) {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		if field.Anonymous {
			return "", "", false
		}

		return field.Name, "", false
	}

	if tag == "-" {
		return "", "", true
	}

	name, opts, _ = strings.Cut(tag, ",")
	if name == "" && !field.Anonymous {
		name = field.Name
	}

	return name, opts, false
}
