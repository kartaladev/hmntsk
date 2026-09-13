package sqlcore

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// TimestampLayout is the one encoding the engine writes into a dialect with no
// native timestamp type. It is [sqlkit.TimestampLayout].
const TimestampLayout = sqlkit.TimestampLayout

// encodeTime renders an instant as the dialect stores it: a native timestamp
// where there is one, and [TimestampLayout] where there is not. A nil instant
// becomes NULL.
func (b *Builder) encodeTime(instant *time.Time) any {
	return sqlkit.EncodeTime(b.dialect, instant)
}

// DecodeTime reads back whatever a driver produced for a timestamp column. It is
// [sqlkit.DecodeTime].
func DecodeTime(value any) (time.Time, error) { return sqlkit.DecodeTime(value) }

// encodeRaw renders an opaque JSON payload for storage, as text. It is
// [sqlkit.EncodeRaw].
func (b *Builder) encodeRaw(payload []byte) any { return sqlkit.EncodeRaw(payload) }

// encodeValue marshals a Go value into its JSON column, mapping absence and
// empty collections to NULL so that an empty map and no map at all read back
// the same way.
func (b *Builder) encodeValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]string:
		if len(typed) == 0 {
			return nil
		}
	case json.RawMessage:
		return b.encodeRaw(typed)
	case *hmntsk.EscalationPolicy:
		if typed == nil {
			return nil
		}
	}

	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return nil
	}

	return string(encoded)
}

// encodeSinks renders the sinks that have accepted an event as the JSON array
// the column stores.
//
// No sink having accepted is NULL rather than an empty array, so that a row
// nobody has delivered yet and a row whose acceptances were cleared read back
// identically — and so that the column costs nothing on the overwhelming
// majority of rows, which are delivered on the first attempt.
func (b *Builder) encodeSinks(sinks []string) any {
	if len(sinks) == 0 {
		return nil
	}

	return b.encodeValue(sinks)
}

// decodeSinks reads back the accepted-sink set, mapping NULL and an empty array
// alike to no sinks.
func decodeSinks(value any) ([]string, error) {
	raw, err := DecodeJSON(value)
	if err != nil {
		return nil, err
	}

	if len(raw) == 0 {
		return nil, nil
	}

	var sinks []string

	if err := json.Unmarshal(raw, &sinks); err != nil {
		return nil, fmt.Errorf("sqlcore: column accepted_sinks: %w", err)
	}

	return sinks, nil
}

// DecodeJSON reads back a JSON column as raw bytes, whatever shape the driver
// returned it in. It is [sqlkit.DecodeJSON].
func DecodeJSON(value any) (json.RawMessage, error) { return sqlkit.DecodeJSON(value) }

// DecodeString reads back a text column, mapping NULL to the empty string. It
// is [sqlkit.DecodeString].
func DecodeString(value any) (string, error) { return sqlkit.DecodeString(value) }

// DecodeInt reads back an integer column, mapping NULL to zero. It is
// [sqlkit.DecodeInt].
func DecodeInt(value any) (int64, error) { return sqlkit.DecodeInt(value) }
