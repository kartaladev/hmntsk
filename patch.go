package hmntsk

import (
	"encoding/json"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

// emptyObject is the base a patch applies to when a task has no saved progress
// yet.
var emptyObject = json.RawMessage(`{}`)

// applyJSONPatch applies an RFC 6902 patch to a document, returning the result.
//
// A patch is used rather than a shallow merge because a form is a tree: a merge
// cannot express "remove the third line item" or "set the postcode inside the
// delivery address" without the client resending everything around it. The
// patch is also the field-level audit record, which is why the transition log
// does not need before-and-after blobs.
//
// A malformed or inapplicable patch is a validation failure, not a fault: it
// came from the caller.
func applyJSONPatch(document, patch json.RawMessage) (json.RawMessage, error) {
	if len(patch) == 0 {
		return cloneRaw(document), nil
	}

	decoded, err := jsonpatch.DecodePatch(patch)
	if err != nil {
		return nil, &ValidationError{
			Subject: "patch",
			Issues:  []ValidationIssue{{Detail: "is not a valid RFC 6902 patch: " + err.Error()}},
		}
	}

	base := document
	if len(base) == 0 {
		base = emptyObject
	}

	merged, err := decoded.Apply(base)
	if err != nil {
		return nil, &ValidationError{
			Subject: "patch",
			Issues:  []ValidationIssue{{Detail: "could not be applied: " + err.Error()}},
		}
	}

	return merged, nil
}
