package sqlcore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// TimestampLayout is the one encoding the engine writes into a dialect with no
// native timestamp type.
//
// It is fixed-width, UTC, and carries exactly six fractional digits, so that
// lexical ordering equals chronological ordering and a value read back compares
// equal to the value written — which is the whole reason for pinning it rather
// than letting each driver choose.
const TimestampLayout = "2006-01-02T15:04:05.000000Z"

// encodeTime renders an instant as the dialect stores it: a native timestamp
// where there is one, and [TimestampLayout] where there is not. A nil instant
// becomes NULL.
func (b *Builder) encodeTime(instant *time.Time) any {
	if instant == nil || instant.IsZero() {
		return nil
	}

	normalized := hmntsk.NormalizeTime(*instant)

	if b.dialect.TimestampColumnType() == "TEXT" {
		return normalized.Format(TimestampLayout)
	}

	return normalized
}

// DecodeTime reads back whatever a driver produced for a timestamp column:
// a time.Time from a native column, or the fixed text encoding from a dialect
// without one.
func DecodeTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return hmntsk.NormalizeTime(typed), nil
	case string:
		return parseTimestamp(typed)
	case []byte:
		return parseTimestamp(string(typed))
	default:
		return time.Time{}, fmt.Errorf("sqlcore: cannot read %T as a timestamp", value)
	}
}

// timestampLayouts are the encodings DecodeTime accepts. The first is what this
// package writes; the rest are what MySQL and SQLite drivers hand back when
// they return a timestamp as text rather than parsing it themselves.
var timestampLayouts = []string{
	TimestampLayout,
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

// parseTimestamp reads a textual timestamp, trying each accepted encoding.
func parseTimestamp(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, nil
	}

	for _, layout := range timestampLayouts {
		parsed, err := time.Parse(layout, trimmed)
		if err == nil {
			return hmntsk.NormalizeTime(parsed), nil
		}
	}

	return time.Time{}, fmt.Errorf("sqlcore: cannot read %q as a timestamp", value)
}

// encodeRaw renders an opaque JSON payload for storage. It is passed as text
// rather than bytes because a driver that sees a byte slice may reasonably
// decide the column is binary.
func (b *Builder) encodeRaw(payload []byte) any {
	if len(payload) == 0 {
		return nil
	}

	return string(payload)
}

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

// DecodeJSON reads back a JSON column as raw bytes, whatever shape the driver
// returned it in.
func DecodeJSON(value any) (json.RawMessage, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		if typed == "" {
			return nil, nil
		}

		return json.RawMessage(typed), nil
	case []byte:
		if len(typed) == 0 {
			return nil, nil
		}

		return json.RawMessage(append([]byte(nil), typed...)), nil
	default:
		return nil, fmt.Errorf("sqlcore: cannot read %T as JSON", value)
	}
}

// DecodeString reads back a text column, mapping NULL to the empty string.
func DecodeString(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		return "", fmt.Errorf("sqlcore: cannot read %T as text", value)
	}
}

// DecodeInt reads back an integer column, mapping NULL to zero. Drivers differ
// in whether they hand back an int64, a float64 or the decimal text of the
// value, so all three are accepted.
func DecodeInt(value any) (int64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case int64:
		return typed, nil
	case int32:
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case float64:
		return int64(typed), nil
	case string:
		return parseInt(typed)
	case []byte:
		return parseInt(string(typed))
	default:
		return 0, fmt.Errorf("sqlcore: cannot read %T as an integer", value)
	}
}

// parseInt reads a decimal integer produced as text.
func parseInt(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}

	var parsed int64

	if _, err := fmt.Sscanf(trimmed, "%d", &parsed); err != nil {
		return 0, fmt.Errorf("sqlcore: cannot read %q as an integer: %w", value, err)
	}

	return parsed, nil
}
