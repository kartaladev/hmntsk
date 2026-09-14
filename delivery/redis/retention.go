package redis

import (
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kartaladev/hmntsk"
)

// TrimMode says what trimming the stream does about consumer groups: whether an
// entry a group has not acknowledged may be trimmed, and what happens to its
// identifier in the group's pending list if it is.
//
// Every mode requires Redis 8.2 or newer; on an older broker every publish
// fails and the backlog is eventually dead-lettered. The zero value sends no
// mode and works everywhere. The operational constraints of each mode are in
// docs/delivery.md, under "Bounding the Redis stream".
type TrimMode string

// The trim modes. Each value is sent to the broker verbatim.
const (
	// TrimKeepRef trims unacknowledged entries too, leaving their identifiers
	// pending with nothing behind them.
	TrimKeepRef TrimMode = "KEEPREF"

	// TrimDelRef trims unacknowledged entries too, and removes their
	// identifiers from every group's pending list.
	TrimDelRef TrimMode = "DELREF"

	// TrimAcked trims only up to the first entry some group has not
	// acknowledged. One stale group stops trimming altogether.
	TrimAcked TrimMode = "ACKED"
)

// valid reports whether the mode is one of the defined modes.
func (m TrimMode) valid() bool {
	switch m {
	case TrimKeepRef, TrimDelRef, TrimAcked:
		return true
	default:
		return false
	}
}

// retention is how far a [Sink] lets its stream grow, and what trimming it does
// about consumer groups. The zero value bounds nothing.
//
// The bounds are pointers because an explicit zero is a wiring mistake to
// report, while an absent bound is the default.
type retention struct {
	maxLen *int64
	maxAge *time.Duration
	mode   TrimMode
	// exact trims to the bound itself rather than to the nearest whole stream
	// node. See [WithExactTrim].
	exact bool
}

// validate reports a contradictory or meaningless retention setting.
func (r retention) validate() error {
	switch {
	case r.maxLen != nil && *r.maxLen <= 0:
		return &ConfigurationError{
			Detail: "the stream length bound must be positive; omit WithMaxLen to leave the stream unbounded",
		}
	case r.maxAge != nil && *r.maxAge <= 0:
		return &ConfigurationError{
			Detail: "the stream age bound must be positive; omit WithMaxAge to leave the stream unbounded",
		}
	case r.maxLen != nil && r.maxAge != nil:
		return &ConfigurationError{
			Detail: "the stream can be bounded by length or by age, not both; choose WithMaxLen or WithMaxAge",
		}
	case (r.mode != "" || r.exact) && r.maxLen == nil && r.maxAge == nil:
		option := "WithTrimMode"
		if r.mode == "" {
			option = "WithExactTrim"
		}

		return &ConfigurationError{
			Detail: option + " needs WithMaxLen or WithMaxAge; on its own it trims nothing",
		}
	case r.mode != "" && !r.mode.valid():
		return &ConfigurationError{
			Detail: fmt.Sprintf("trim mode %q is not TrimKeepRef, TrimDelRef or TrimAcked", string(r.mode)),
		}
	default:
		return nil
	}
}

// trim puts the bound on an XADD, so the publish trims in the same command:
// approximately unless [WithExactTrim] asked for exact.
//
// An unset mode sends no mode keyword at all. That is load-bearing: a broker
// older than Redis 8.2 rejects every mode, even the one that is 8.2's default,
// so only an absent keyword keeps a bound working there.
func (r retention) trim(args *goredis.XAddArgs, clock hmntsk.Clock) {
	args.Mode = string(r.mode)

	switch {
	case r.maxLen != nil:
		args.MaxLen = *r.maxLen
	case r.maxAge != nil:
		args.MinID = strconv.FormatInt(clock.Now().Add(-*r.maxAge).UnixMilli(), 10) + "-0"
	default:
		return
	}

	args.Approx = !r.exact
}

// WithMaxLen bounds the stream to at least n entries, trimming the oldest
// approximately as part of each publish, whether or not a consumer has read
// them unless [WithTrimMode] says otherwise. It cannot be combined with
// [WithMaxAge]. Without a bound the stream is never trimmed; see
// docs/delivery.md for what approximate means and what trimming costs.
func WithMaxLen(n int64) Option {
	return func(c *config) { c.retention.maxLen = &n }
}

// WithMaxAge bounds the stream by age, trimming entries older than the sink's
// clock minus age approximately as part of each publish, whether or not a
// consumer has read them unless [WithTrimMode] says otherwise. It cannot be
// combined with [WithMaxLen]. The cutoff uses the sink's clock (see
// [WithClock]) against IDs stamped by the broker's, so clock skew shifts
// retention; see docs/delivery.md.
func WithMaxAge(age time.Duration) Option {
	return func(c *config) { c.retention.maxAge = &age }
}

// WithTrimMode selects what trimming does about consumer groups; see
// [TrimMode]. It requires [WithMaxLen] or [WithMaxAge], and Redis 8.2 or newer:
// [New] does not dial and cannot check the server, so check its version before
// setting a mode.
func WithTrimMode(mode TrimMode) Option {
	return func(c *config) { c.retention.mode = mode }
}

// WithExactTrim trims the stream exactly to its bound instead of approximately:
// after a publish, a [WithMaxLen] stream holds exactly n entries once n have
// been published, and a [WithMaxAge] stream holds no entry older than the
// cutoff. It requires [WithMaxLen] or [WithMaxAge], and combines with
// [WithTrimMode].
//
// Approximate trimming is the default because it is cheaper. Exact trimming
// makes the broker split a stream node on most publishes, which costs CPU on
// every one, and the broker accepts no per-publish limit for it: set on a
// stream already far above its bound, the first publish removes the whole
// excess in one command and blocks the broker while it does. Trim such a
// stream once by hand with XTRIM before enabling it. See docs/delivery.md.
func WithExactTrim() Option {
	return func(c *config) { c.retention.exact = true }
}

// WithClock supplies the sink's source of time, which is what the
// [WithMaxAge] cutoff is computed from. It defaults to the system clock. A nil
// clock is ignored.
func WithClock(clock hmntsk.Clock) Option {
	return func(c *config) {
		if clock != nil {
			c.clock = clock
		}
	}
}
