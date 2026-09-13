package notify

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// IDGenerator mints notification identifiers. The default is a
// [UUIDv7Generator]; [WithIDGenerator] replaces it.
type IDGenerator interface {
	// NewID returns a new identifier, unique among every notification.
	NewID() (string, error)
}

// IDGeneratorFunc adapts a function to the [IDGenerator] interface.
type IDGeneratorFunc func() (string, error)

// NewID implements [IDGenerator].
func (f IDGeneratorFunc) NewID() (string, error) { return f() }

// UUIDv7Generator mints RFC 9562 version 7 UUIDs: a millisecond timestamp, a
// 12-bit counter and random bits.
//
// Identifiers minted by one generator sort in the order they were minted, even
// within one millisecond and even if the wall clock steps backwards. That keeps
// newest-first listings stable when two notifications share a creation time.
//
// It is the same algorithm the task engine uses, copied rather than imported so
// that notify stays free of the engine. A UUIDv7Generator is safe for concurrent
// use.
type UUIDv7Generator struct {
	mu      sync.Mutex
	lastMS  int64
	counter uint16
}

// NewUUIDv7Generator returns a ready generator.
func NewUUIDv7Generator() *UUIDv7Generator { return &UUIDv7Generator{} }

// uuidV7CounterMax is the largest value the 12-bit counter can hold.
const uuidV7CounterMax = 0x0FFF

// NewID implements [IDGenerator].
func (g *UUIDv7Generator) NewID() (string, error) {
	var buf [16]byte

	if _, err := rand.Read(buf[8:]); err != nil {
		return "", fmt.Errorf("notify: read randomness for an identifier: %w", err)
	}

	ms, counter := g.tick()

	binary.BigEndian.PutUint64(buf[0:8], uint64(ms)<<16)
	buf[6] = 0x70 | byte(counter>>8&0x0F)
	buf[7] = byte(counter)
	buf[8] = buf[8]&0x3F | 0x80

	return formatUUID(buf), nil
}

// tick returns the millisecond and counter for the next identifier, never going
// backwards.
func (g *UUIDv7Generator) tick() (ms int64, counter uint16) {
	g.mu.Lock()
	defer g.mu.Unlock()

	observed := time.Now().UTC().UnixMilli()

	switch {
	case observed > g.lastMS:
		g.lastMS = observed
		g.counter = 0
	case g.counter < uuidV7CounterMax:
		g.counter++
	default:
		g.lastMS++
		g.counter = 0
	}

	return g.lastMS, g.counter
}

// formatUUID renders 16 bytes in the canonical 8-4-4-4-12 form.
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
