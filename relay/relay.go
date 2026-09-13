package relay

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// Defaults for a relay the host does not configure.
const (
	// DefaultMaxAttempts is how many times an event is attempted before it is
	// dead-lettered.
	DefaultMaxAttempts = 5
	// DefaultBackoff is the delay before the first retry, doubled on each
	// attempt after it.
	DefaultBackoff = 30 * time.Second
	// DefaultBackoffCeiling caps the doubling. Unbounded doubling turns a
	// day-long outage into a week-long backlog drain.
	DefaultBackoffCeiling = time.Hour
	// DefaultJitter is the fraction of the computed delay the random component
	// may move it by, in either direction.
	//
	// It is not a tuning knob so much as the point of the exercise: without
	// jitter, a receiver that goes down for five minutes comes back to its
	// entire backlog retrying in lockstep, which is how a recovering service
	// is knocked over a second time.
	DefaultJitter = 0.2
)

// Relay delivers durably recorded events to every configured sink.
//
// Nothing here starts on its own. Constructing a Relay starts no goroutine, no
// timer and no polling; the host drives it, from a long-running goroutine
// through [Relay.Run], from its own scheduler through [Relay.Relay], or by
// hand. An embedded engine does not get to decide that the process it lives in
// now has a background thread.
//
// A Relay is safe for concurrent use only in the sense that two relays may run
// against the same events: exclusivity comes from the lease, not from this
// type. One relay's passes are meant to be serial.
type Relay struct {
	engine *hmntsk.Service
	sinks  []Sink
	// names holds each sink's name, read once at construction and parallel to
	// sinks. See [Relay.nameSinks].
	names       []string
	owner       string
	lease       time.Duration
	batch       int
	maxAttempts int
	backoff     time.Duration
	ceiling     time.Duration
	jitter      float64
	random      func() float64
	onError     func(ctx context.Context, err error)
}

// RelayOption configures a [Relay].
type RelayOption func(*Relay)

// WithSinks adds the destinations an event is fanned out to. At least one is
// required: a relay with no sink would mark every event delivered to nobody.
func WithSinks(sinks ...Sink) RelayOption {
	return func(r *Relay) {
		for _, sink := range sinks {
			if sink != nil {
				r.sinks = append(r.sinks, sink)
			}
		}
	}
}

// WithRelayOwner names the relay in the leases it takes, so that an abandoned
// lease is traceable to the instance that abandoned it. The default is a
// generated identifier.
func WithRelayOwner(owner string) RelayOption {
	return func(r *Relay) {
		if owner != "" {
			r.owner = owner
		}
	}
}

// WithRelayLease sets how long a relay holds a claimed event.
//
// It must outlast a whole fan-out, every sink included, or a second relay
// reclaims an event the first is still delivering — which is at-least-once
// behaving as designed, but for no reason.
func WithRelayLease(duration time.Duration) RelayOption {
	return func(r *Relay) {
		if duration > 0 {
			r.lease = duration
		}
	}
}

// WithRelayBatch caps how many events one pass claims.
func WithRelayBatch(batch int) RelayOption {
	return func(r *Relay) {
		if batch > 0 {
			r.batch = batch
		}
	}
}

// WithMaxAttempts sets how many times an event is attempted before it is
// dead-lettered.
func WithMaxAttempts(attempts int) RelayOption {
	return func(r *Relay) {
		if attempts > 0 {
			r.maxAttempts = attempts
		}
	}
}

// WithBackoff sets the delay before the first retry and the ceiling the
// doubling is capped at. Either argument is ignored when it is not positive,
// and a ceiling below the base is raised to it.
func WithBackoff(base, ceiling time.Duration) RelayOption {
	return func(r *Relay) {
		if base > 0 {
			r.backoff = base
		}

		if ceiling > 0 {
			r.ceiling = ceiling
		}

		if r.ceiling < r.backoff {
			r.ceiling = r.backoff
		}
	}
}

// WithJitter sets the fraction of the computed delay the random component may
// move it by, in either direction. Zero switches jitter off, which is a choice
// only a single-event deployment can afford; anything at or above one is
// clamped to one.
func WithJitter(fraction float64) RelayOption {
	return func(r *Relay) {
		if fraction < 0 {
			return
		}

		r.jitter = min(fraction, 1)
	}
}

// WithJitterSource supplies the randomness the jitter is drawn from, as a
// function returning a value in [0, 1). The default is [math/rand/v2.Float64].
//
// It exists so that a test can assert what the schedule is rather than that it
// is within a range, and so that a host may substitute a source of its own.
func WithJitterSource(source func() float64) RelayOption {
	return func(r *Relay) {
		if source != nil {
			r.random = source
		}
	}
}

// WithRelayErrorHandler supplies a hook for failures raised while relaying: a
// sink that could not deliver, a sink that would not classify its result, and
// a settlement the store refused. The default does nothing, which is safe but
// silent.
func WithRelayErrorHandler(handler func(ctx context.Context, err error)) RelayOption {
	return func(r *Relay) {
		if handler != nil {
			r.onError = handler
		}
	}
}

// NewRelay returns a relay over an engine. It starts nothing.
//
// It refuses a configuration that could only fail later: no sinks at all, a
// sink with no name, or two sinks sharing one. Names are what per-sink
// acceptance is recorded under, so two sinks with one name would each be
// credited with the other's deliveries, and the event would be marked
// delivered having reached one destination.
func NewRelay(svc *hmntsk.Service, opts ...RelayOption) (*Relay, error) {
	if svc == nil {
		return nil, &hmntsk.ConfigurationError{Detail: "a service is required to relay"}
	}

	relay := &Relay{
		engine:      svc,
		lease:       hmntsk.DefaultOutboxLease,
		batch:       hmntsk.DefaultOutboxBatch,
		maxAttempts: DefaultMaxAttempts,
		backoff:     DefaultBackoff,
		ceiling:     DefaultBackoffCeiling,
		jitter:      DefaultJitter,
		random:      rand.Float64,
		onError:     func(context.Context, error) {},
	}

	for _, opt := range opts {
		opt(relay)
	}

	if err := relay.nameSinks(); err != nil {
		return nil, err
	}

	// Only now, so that a host that named its relay cannot be refused one for
	// want of randomness it had no use for.
	if relay.owner == "" {
		owner, err := svc.NewEventID()
		if err != nil {
			return nil, err
		}

		relay.owner = "relay-" + owner
	}

	return relay, nil
}

// nameSinks records each sink's name once and reports a wiring mistake that
// would otherwise surface as a lost event on the first pass.
//
// The names are read here and never again. A sink is entitled to compute its
// name, but per-sink acceptance is written to the database under it, so a name
// that changed between passes would re-deliver every event that sink had
// already taken — reading it once makes that unrepresentable rather than
// merely discouraged.
func (r *Relay) nameSinks() error {
	if len(r.sinks) == 0 {
		return &hmntsk.ConfigurationError{
			Detail: "a relay needs at least one sink; one with none would mark every event " +
				"delivered having delivered it nowhere",
		}
	}

	r.names = make([]string, 0, len(r.sinks))
	seen := make(map[string]struct{}, len(r.sinks))

	for _, sink := range r.sinks {
		name := sink.Name()
		if name == "" {
			return &hmntsk.ConfigurationError{
				Detail: "every sink must be named; per-sink acceptance is recorded under the name",
			}
		}

		if _, duplicate := seen[name]; duplicate {
			return &hmntsk.ConfigurationError{
				Detail: "two sinks are named " + name + "; acceptance recorded for one would " +
					"be read as acceptance by the other",
			}
		}

		seen[name] = struct{}{}
		r.names = append(r.names, name)
	}

	return nil
}

// Owner returns the identifier this relay records in the leases it takes.
func (r *Relay) Owner() string { return r.owner }

// Sinks returns the names of the destinations this relay fans out to, in the
// order they are attempted.
func (r *Relay) Sinks() []string { return slices.Clone(r.names) }

// SinkResult is what one sink did during one pass.
type SinkResult struct {
	// Delivered is how many events the sink took.
	Delivered int
	// Retryable is how many it refused in a way another attempt might survive.
	Retryable int
	// Permanent is how many it refused in a way no further attempt can change.
	Permanent int
	// Unclassified is how many it refused without saying which. A non-zero
	// count is a bug in the sink, not in its destination.
	Unclassified int
	// Skipped is how many it was not offered because it had already accepted
	// them on an earlier attempt.
	Skipped int
}

// Result is what one relay pass did.
//
// Delivered, Retried, DeadLettered and Unsettled count what the pass made
// durable, not what it decided, and together they account for every claimed
// event: a settlement the store refused is counted as Unsettled and nothing
// else, because the relay's decision about that event did not survive the pass.
type Result struct {
	// Claimed is how many due events this relay leased. Events another relay
	// had already leased are not counted: they are not this relay's to deliver.
	Claimed int
	// Delivered is how many of them every configured sink accepted, and which
	// are therefore now marked delivered.
	Delivered int
	// Retried is how many were left pending for another attempt.
	Retried int
	// DeadLettered is how many will not be attempted again, because their
	// attempts ran out or a sink reported a permanent failure.
	DeadLettered int
	// Unsettled is how many were attempted but whose outcome the store refused
	// to record.
	//
	// It is the pass's at-least-once exposure, and the one number only the
	// relay can produce: those events may well have reached their destinations,
	// and every one of them will be offered again once its lease expires,
	// because nothing durable says otherwise. A non-zero count means duplicates
	// are coming.
	Unsettled int
	// Sinks is the per-sink breakdown, keyed by sink name. A sink that was
	// offered nothing still appears, with a zero entry.
	Sinks map[string]SinkResult
}

// Relay claims a batch of due events and delivers them.
//
// Claiming is a conditional update on the lease columns, taken and committed
// before anything is delivered, so a second relay running at the same moment
// finds nothing left to take. It needs no lock primitive, which is what lets it
// work identically on a database that has none.
//
// Each claimed event is settled in its own transaction. An event that cannot be
// delivered does not take the rest of the batch down with it: the failure
// reaches the configured error handler and the pass continues. Only claiming
// itself returns an error, because a pass that could not claim did nothing and
// has nothing to report.
func (r *Relay) Relay(ctx context.Context) (Result, error) {
	// One instant for the whole pass: the lease, the schedule and the delivery
	// stamp all measure from it, and a pass whose events each read the clock
	// again would spread a batch that failed together across the schedule by
	// however long the batch took.
	//
	// It is normalised to what the stores keep, so that a timestamp this pass
	// writes is the timestamp the next pass reads back rather than one
	// sub-microsecond away from it.
	now := hmntsk.NormalizeTime(r.engine.Clock().Now())

	claimed, err := r.engine.ClaimDueEvents(ctx, hmntsk.OutboxClaim{
		Now:      now,
		Owner:    r.owner,
		Duration: r.lease,
		Limit:    r.batch,
	})
	if err != nil {
		return Result{}, err
	}

	result := r.emptyResult()
	result.Claimed = len(claimed)

	for _, entry := range claimed {
		r.settle(ctx, entry, now, &result)
	}

	return result, nil
}

// Run relays every interval until ctx is cancelled.
//
// It blocks. The host decides whether that is a goroutine of its own, a
// scheduled job, or a command; the engine starts nothing by itself.
func (r *Relay) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return &hmntsk.ConfigurationError{Detail: "a relay interval must be positive"}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := r.Relay(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			r.onError(ctx, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// settle delivers one claimed event and writes down what happened to it.
func (r *Relay) settle(ctx context.Context, entry hmntsk.OutboxEntry, now time.Time, result *Result) {
	event := entry.Event
	fan := r.fanOut(ctx, entry, result)
	// The attempt count after this attempt. The delay is computed from the
	// count before it, so that the first retry waits the configured base rather
	// than twice it.
	attempts := entry.Attempts + 1

	var (
		err error
		// settled is the counter this outcome belongs in, bumped only once the
		// store has taken it: a result that counted decisions rather than
		// writes would report an event delivered whose row still says pending.
		settled *int
	)

	switch {
	case len(fan.failures) == 0:
		published := now
		err = r.engine.MarkEventAccepted(ctx, hmntsk.Acceptance{
			EventID:     event.ID,
			Accepted:    fan.accepted,
			PublishedAt: &published,
			Attempts:    attempts,
		})
		settled = &result.Delivered

	case fan.permanent || attempts >= r.maxAttempts:
		// The acceptances travel with the dead letter, this attempt's included.
		// Nothing will read them to decide a retry — there will not be one —
		// but they are what "which destinations got it?" is answered from, and
		// a sink that succeeded on the same pass that another gave up on did
		// receive the event.
		err = r.engine.MarkEventDeadLettered(ctx, hmntsk.DeadLetter{
			EventID:   event.ID,
			Attempts:  attempts,
			LastError: fan.lastError(),
			Accepted:  fan.accepted,
		})
		settled = &result.DeadLettered

	case len(fan.accepted) > len(entry.Accepted):
		// Some sink took it and some did not: the acceptance and the reschedule
		// are one write, so that a crash between them cannot lose either.
		next := r.nextAttemptAt(now, entry.Attempts)
		err = r.engine.MarkEventAccepted(ctx, hmntsk.Acceptance{
			EventID:       event.ID,
			Accepted:      fan.accepted,
			NextAttemptAt: &next,
			Attempts:      attempts,
			LastError:     fan.lastError(),
		})
		settled = &result.Retried

	default:
		err = r.engine.RecordDeliveryAttempt(ctx, hmntsk.AttemptRecord{
			EventID:       event.ID,
			Attempts:      attempts,
			NextAttemptAt: r.nextAttemptAt(now, entry.Attempts),
			LastError:     fan.lastError(),
		})
		settled = &result.Retried
	}

	if err != nil {
		// The delivery may well have happened; only the record of it failed.
		// The lease expires on its own and the event is attempted again, which
		// is at-least-once behaving exactly as documented.
		result.Unsettled++
		r.onError(ctx, fmt.Errorf("hmntsk: settle event %s: %w", event.ID, err))

		return
	}

	*settled++
}

// fanResult is what one event's fan-out came to, in the shape the store
// records it.
type fanResult struct {
	// accepted is every sink that has now taken the event, earlier attempts
	// included, because that is the whole set the store records.
	accepted []string
	// failures describes the sinks that refused, one message per sink.
	failures []string
	// permanent reports whether any refusal was one no further attempt can
	// change.
	permanent bool
}

// lastError renders the refusals as the row's last error.
func (f fanResult) lastError() string { return strings.Join(f.failures, "; ") }

// errUnclassified is what a sink that will not classify its own result is
// treated as having said.
//
// It is deliberately fatal rather than retryable. A sink that cannot say
// whether another attempt would help is a sink whose event would otherwise be
// retried until its budget ran out, once per pass, against a destination that
// may well have taken it already.
var errUnclassified = errors.New(
	"the sink returned an unclassified outcome, which is a bug in the sink rather than a " +
		"verdict about its destination",
)

// errUnexplained is what a sink that reports a failure with no error is treated
// as having said.
var errUnexplained = errors.New("the sink reported a failure without saying what went wrong")

// fanOut offers one event to every sink that has not already taken it, and
// reports every refusal to the host.
func (r *Relay) fanOut(ctx context.Context, entry hmntsk.OutboxEntry, result *Result) fanResult {
	// Room for the acceptances this attempt may add, so that the first one does
	// not reallocate a slice that was just copied.
	accepted := make([]string, len(entry.Accepted), len(entry.Accepted)+len(r.sinks))
	copy(accepted, entry.Accepted)

	out := fanResult{accepted: accepted}

	for i, sink := range r.sinks {
		name := r.names[i]
		tally := result.Sinks[name]

		if entry.HasAccepted(name) {
			// Offering it again would deliver a second time to a destination
			// that has already taken it, for no reason but another sink's
			// failure. That is what per-sink acceptance exists to prevent.
			tally.Skipped++
			result.Sinks[name] = tally

			continue
		}

		attempt := Attempt{
			Event:  entry.Event,
			Number: entry.Attempts + 1,
		}

		// An identifier the relay could not mint is not worth abandoning a
		// delivery over: the event identifier still lets a receiver
		// de-duplicate, which is the guarantee that matters.
		if deliveryID, err := r.engine.NewEventID(); err == nil {
			attempt.DeliveryID = deliveryID
		} else {
			r.onError(ctx, fmt.Errorf("hmntsk: mint a delivery identifier for event %s: %w",
				entry.Event.ID, err))
		}

		switch outcome := sink.Deliver(ctx, attempt); outcome.Status {
		case OutcomeDelivered:
			tally.Delivered++
			out.accepted = append(out.accepted, name)
		case OutcomeRetryable:
			tally.Retryable++
			r.refused(ctx, &out, entry.Event.ID, name, cmp.Or(outcome.Err, errUnexplained))
		case OutcomePermanent:
			tally.Permanent++
			out.permanent = true
			r.refused(ctx, &out, entry.Event.ID, name, cmp.Or(outcome.Err, errUnexplained))
		default:
			// [OutcomeUnclassified] and anything outside the catalogue alike: a
			// sink that will not say whether another attempt would help has not
			// given a verdict, and is not owed one more attempt per pass until
			// its budget runs out.
			tally.Unclassified++
			out.permanent = true
			r.refused(ctx, &out, entry.Event.ID, name, errUnclassified)
		}

		result.Sinks[name] = tally
	}

	return out
}

// refused records one sink's refusal on the row and reports it to the host.
//
// Both, not either: the row's last error is what whoever queries the outbox
// reads, and the handler is what tells an operator before they think to.
func (r *Relay) refused(ctx context.Context, out *fanResult, eventID, sink string, cause error) {
	out.failures = append(out.failures, sink+": "+cause.Error())

	r.onError(ctx, fmt.Errorf("hmntsk: deliver event %s to sink %s: %w", eventID, sink, cause))
}

// emptyResult returns a result with a zero entry for every configured sink, so
// that a caller ranging over the breakdown sees every sink whether or not it
// was offered anything.
func (r *Relay) emptyResult() Result {
	sinks := make(map[string]SinkResult, len(r.names))
	for _, name := range r.names {
		sinks[name] = SinkResult{}
	}

	return Result{Sinks: sinks}
}

// nextAttemptAt returns when an event that has failed attempts times becomes
// due again: now plus min(backoff * 2^attempts, ceiling), moved by the random
// component.
//
// attempts is the number of attempts made before the one that just failed, so
// that the first retry waits the configured base rather than twice it.
func (r *Relay) nextAttemptAt(now time.Time, attempts int) time.Time {
	delay := math.Ldexp(float64(r.backoff), max(attempts, 0))
	if ceiling := float64(r.ceiling); delay > ceiling {
		delay = ceiling
	}

	// The random component is symmetric: source() is in [0, 1), so the factor
	// runs from -jitter to +jitter and the expected delay is unchanged.
	if r.jitter > 0 {
		delay *= 1 + r.jitter*(2*r.random()-1)
	}

	return now.Add(time.Duration(max(delay, 0)))
}
