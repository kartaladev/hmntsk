package hmntsk

import "strings"

// Well-known task type metadata keys, for [TypeSpec.Metadata].
//
// They are conventions, not requirements. A client that follows them can link
// any task to where its work is done without knowing the type in advance; a
// host that ignores them loses nothing else. Keys under the "hmntsk." prefix are
// reserved for conventions the library defines, and every other key belongs to
// the host.
const (
	// MetadataFormKey names a form the client knows how to render for tasks of
	// the type, such as a key into the client's own form registry.
	MetadataFormKey = "hmntsk.formKey"
	// MetadataRoute is a link to where a task's work is done, written as a
	// template that [ExpandRoute] expands for one task.
	MetadataRoute = "hmntsk.route"
)

// ExpandRoute expands a route template, such as a type's [MetadataRoute], for
// one task. It is an optional helper: the engine never calls it, and a host may
// expand its templates any other way.
//
// These placeholders are replaced:
//
//   - {task.id} and {task.type};
//   - {correlation.ownerType}, {correlation.ownerRef} and
//     {correlation.activityKey};
//   - {correlation.extra.<key>}, for a key the task's correlation carries.
//
// A known field with no value becomes empty. Anything else in braces, an extra
// key the task does not carry included, is left exactly as written, so a
// template can hold placeholders of the host's own for a later pass.
//
// Values are inserted raw, never escaped. Whether a value needs path escaping,
// query escaping or none at all depends on where the template points, which
// only the host knows, so escaping is the host's to do before the result is
// rendered or followed.
func ExpandRoute(template string, task Task) string {
	var out strings.Builder

	rest := template

	for {
		closing := strings.IndexByte(rest, '}')
		if closing < 0 {
			break
		}

		// The nearest opening brace before the closing one, so that a
		// placeholder wrapped in braces of the template's own still expands.
		opening := strings.LastIndexByte(rest[:closing], '{')
		if opening < 0 {
			out.WriteString(rest[:closing+1])
			rest = rest[closing+1:]

			continue
		}

		out.WriteString(rest[:opening])

		if value, ok := routeValue(rest[opening+1:closing], task); ok {
			out.WriteString(value)
		} else {
			out.WriteString(rest[opening : closing+1])
		}

		rest = rest[closing+1:]
	}

	out.WriteString(rest)

	return out.String()
}

// routeValue resolves one placeholder name, reporting whether it is one
// [ExpandRoute] knows.
func routeValue(name string, task Task) (string, bool) {
	switch name {
	case "task.id":
		return task.ID.String(), true
	case "task.type":
		return task.Type, true
	case "correlation.ownerType":
		return task.Correlation.OwnerType, true
	case "correlation.ownerRef":
		return task.Correlation.OwnerRef, true
	case "correlation.activityKey":
		return task.Correlation.ActivityKey, true
	}

	if key, ok := strings.CutPrefix(name, "correlation.extra."); ok {
		value, present := task.Correlation.Extra[key]

		return value, present
	}

	return "", false
}
