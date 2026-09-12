package hmntsk

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// TaskID identifies one task. It is an opaque string so that hosts may
// substitute their own identifier scheme and so that every supported dialect
// can store it in one portable column type.
type TaskID string

// String implements [fmt.Stringer].
func (id TaskID) String() string { return string(id) }

// IsZero reports whether id is unset.
func (id TaskID) IsZero() bool { return id == "" }

// IDGenerator produces task identifiers. It is a port: the default
// implementation returns UUIDv7 values, which are time-ordered and therefore
// index well and page stably, but a host may substitute ULIDs or a scheme of
// its own.
//
// Implementations must be safe for concurrent use, and successive identifiers
// must sort in creation order.
type IDGenerator interface {
	// NewTaskID returns a fresh identifier.
	NewTaskID() (TaskID, error)
}

// IDGeneratorFunc adapts a function to the [IDGenerator] interface.
type IDGeneratorFunc func() (TaskID, error)

// NewTaskID implements [IDGenerator].
func (f IDGeneratorFunc) NewTaskID() (TaskID, error) { return f() }

// UUIDv7Generator is the default [IDGenerator]. It emits RFC 9562 version 7
// UUIDs in canonical hyphenated text form.
//
// Identifiers minted within the same millisecond are kept strictly increasing
// by using the twelve rand_a bits as a sub-millisecond counter, so the
// generator is monotonic rather than merely time-ordered. That is what lets
// keyset pagination over the identifier column be stable.
//
// The zero value is ready to use and is safe for concurrent use.
type UUIDv7Generator struct {
	mu      sync.Mutex
	lastMS  int64
	counter uint16
	// now is overridable for tests; nil means [time.Now].
	now func() time.Time
}

// NewUUIDv7Generator returns the default [IDGenerator].
func NewUUIDv7Generator() *UUIDv7Generator { return &UUIDv7Generator{} }

// uuidV7CounterMax is the largest value the twelve rand_a counter bits hold.
const uuidV7CounterMax = 0x0FFF

// NewTaskID implements [IDGenerator].
func (g *UUIDv7Generator) NewTaskID() (TaskID, error) {
	var buf [16]byte

	if _, err := rand.Read(buf[8:]); err != nil {
		return "", fmt.Errorf("hmntsk: read randomness for task id: %w", err)
	}

	ms, counter := g.tick()

	binary.BigEndian.PutUint64(buf[0:8], uint64(ms)<<16)
	buf[6] = 0x70 | byte(counter>>8&0x0F)
	buf[7] = byte(counter)
	buf[8] = buf[8]&0x3F | 0x80

	return TaskID(formatUUID(buf)), nil
}

// tick returns the millisecond timestamp and sub-millisecond counter for the
// next identifier, advancing the timestamp if the counter has been exhausted.
func (g *UUIDv7Generator) tick() (ms int64, counter uint16) {
	g.mu.Lock()
	defer g.mu.Unlock()

	nowFn := g.now
	if nowFn == nil {
		nowFn = time.Now
	}

	observed := nowFn().UTC().UnixMilli()

	switch {
	case observed > g.lastMS:
		g.lastMS = observed
		g.counter = 0
	case g.counter < uuidV7CounterMax:
		g.counter++
	default:
		// The counter is exhausted within this millisecond: borrow from the
		// next one rather than emit a non-increasing identifier.
		g.lastMS++
		g.counter = 0
	}

	return g.lastMS, g.counter
}

// formatUUID renders the sixteen bytes in canonical 8-4-4-4-12 hyphenated form.
func formatUUID(b [16]byte) string {
	var out [36]byte

	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])

	return string(out[:])
}
