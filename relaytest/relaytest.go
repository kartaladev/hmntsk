// Package relaytest is the behavioural suite every hmntsk relay must pass.
//
// Claiming, retry scheduling and dead-lettering are storage behaviour: the
// lease is a conditional update, the schedule is a timestamp column, and a dead
// letter is the absence of a next attempt. All three therefore have to be
// asserted against a real store rather than against a mock of one, and against
// every store — seven driver and dialect combinations, whose divergences are
// exactly where a relay would quietly stop being exclusive.
//
// Writing those cases per adapter would produce seven copies of the same suite
// and no way to tell whether a passing adapter passes the same bar as the
// others. One exported suite makes the count affordable: each adapter's test
// file builds its store and calls [RunSuite].
//
// The suite drives the real [relay.Relay] over the store the factory supplies.
// It is a relay suite and a storage suite at once, because the guarantees it
// checks belong to neither alone: "each event is attempted by exactly one
// relay" is a claim about the relay's algorithm and about the store's
// conditional update, and it is false if either half is wrong.
package relaytest

import (
	"testing"

	"github.com/kartaladev/hmntsk"
)

// Harness is one store adapter, ready to relay from. A factory builds a fresh
// one per case, with an empty schema.
type Harness struct {
	// Store is the adapter under test: the durable record the relay claims
	// from, and the transaction boundary it claims within.
	//
	// The suite seeds it through [hmntsk.EventSink] and reads it back through
	// [hmntsk.OutboxStore], but the relay reaches it through a real
	// [hmntsk.Service] built over it — so what these cases exercise is the path
	// a host takes, and an adapter that claims correctly under a hand-written
	// transaction but not behind the engine's relay port fails here.
	Store hmntsk.Store
}

// Factory builds a fresh harness over an empty schema. It is called once per
// case, and may register cleanup with t.
type Factory func(t *testing.T) Harness

// RunSuite runs every case against the store the factory builds.
//
// It is the executable form of the event-relay capability: a store that passes
// it delivers, retries and dead-letters identically to every other store, which
// is the only thing that makes "the relay behaves the same everywhere" a
// statement rather than a hope.
func RunSuite(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("Claiming", func(t *testing.T) { runClaimingCases(t, factory) })
	t.Run("Scheduling", func(t *testing.T) { runSchedulingCases(t, factory) })
	t.Run("DeadLettering", func(t *testing.T) { runDeadLetterCases(t, factory) })
	t.Run("PerSink", func(t *testing.T) { runPerSinkCases(t, factory) })
	t.Run("Resilience", func(t *testing.T) { runResilienceCases(t, factory) })
}
