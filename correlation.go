package hmntsk

import (
	"encoding/json"
	"maps"
)

// CorrelationData ties a task back to whatever asked for it without naming the
// caller's types. It is modelled on the WS-HumanTask notion of the owning unit
// of work: the engine stores these fields, filters on them and echoes them on
// every event, and never interprets them.
//
// A workflow engine might set OwnerType to "process", OwnerRef to a process
// instance identifier and ActivityKey to a node identifier; a plain service
// might set OwnerType to "order" and OwnerRef to an order number. The engine
// does not care which.
type CorrelationData struct {
	// OwnerType names the kind of thing that owns the task, such as "process"
	// or "order". It is opaque to the engine.
	OwnerType string `json:"ownerType,omitempty"`
	// OwnerRef identifies the specific owning unit of work.
	OwnerRef string `json:"ownerRef,omitempty"`
	// ActivityKey identifies the step within the owning unit of work that the
	// task stands for.
	ActivityKey string `json:"activityKey,omitempty"`
	// Extra carries further correlation keys the host wants echoed on events.
	// It is not indexed and is not filterable; put anything you need to query
	// on into one of the three fields above.
	Extra map[string]string `json:"extra,omitempty"`
}

// IsZero reports whether no correlation was supplied.
func (c CorrelationData) IsZero() bool {
	return c.OwnerType == "" && c.OwnerRef == "" && c.ActivityKey == "" && len(c.Extra) == 0
}

// Clone returns a deep copy, so that a value handed to a caller cannot be
// mutated through the map it shares.
func (c CorrelationData) Clone() CorrelationData {
	out := c

	if c.Extra != nil {
		out.Extra = maps.Clone(c.Extra)
	}

	return out
}

// CallbackTarget is where a host wants notifications about a task delivered. It
// follows the WS-Addressing Endpoint Reference pattern over JSON: an address
// plus reference parameters that the caller owns.
//
// The engine treats ReferenceParameters as opaque bytes. It never reads them,
// never reorders them and never reformats them; it stores them as supplied and
// returns them unchanged with any notification addressed to this target.
type CallbackTarget struct {
	// Address is the delivery address. Its meaning is the host's: a URL, a
	// queue name, a topic. The engine does not dial it — delivery belongs to
	// consumers of the event stream.
	Address string `json:"address"`
	// ReferenceParameters are caller-supplied values echoed back verbatim with
	// every notification sent to Address.
	ReferenceParameters json.RawMessage `json:"referenceParameters,omitempty"`
}

// IsZero reports whether no callback target was supplied.
func (t CallbackTarget) IsZero() bool {
	return t.Address == "" && len(t.ReferenceParameters) == 0
}

// Clone returns a deep copy, so that the caller's byte slice and the engine's
// cannot alias.
func (t CallbackTarget) Clone() CallbackTarget {
	out := t
	out.ReferenceParameters = cloneRaw(t.ReferenceParameters)

	return out
}

// cloneRaw copies raw JSON, preserving the nil/empty distinction.
func cloneRaw(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}

	out := make(json.RawMessage, len(in))
	copy(out, in)

	return out
}
