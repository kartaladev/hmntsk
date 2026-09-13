package relay

import (
	"context"

	"github.com/kartaladev/hmntsk"
)

// OutcomeStatus classifies what happened to one delivery attempt.
//
// It is an explicit part of the contract rather than something the relay infers
// from an error value, because "try again in a minute" and "this will never
// work" are both errors and must be treated oppositely: a 400 and a 503 look
// identical to [errors.Is] and mean opposite things.
type OutcomeStatus int

const (
	// OutcomeUnclassified is the zero value, and is not a verdict. A sink that
	// returns it has forgotten to classify its result; the relay reports that
	// to the host's error handler and dead-letters the event rather than
	// retrying it forever against a sink that cannot say what went wrong.
	OutcomeUnclassified OutcomeStatus = iota
	// OutcomeDelivered reports that the destination has taken the event. The
	// relay will not offer it to this sink again.
	OutcomeDelivered
	// OutcomeRetryable reports a failure that another attempt might survive: a
	// timeout, a transport error, a receiver that is temporarily unavailable.
	OutcomeRetryable
	// OutcomePermanent reports a failure that no further attempt can change: a
	// rejected request, an address the destination policy refuses. The relay
	// dead-letters the event without spending the remaining attempts.
	OutcomePermanent
)

// String implements [fmt.Stringer].
func (s OutcomeStatus) String() string {
	switch s {
	case OutcomeDelivered:
		return "delivered"
	case OutcomeRetryable:
		return "retryable"
	case OutcomePermanent:
		return "permanent"
	case OutcomeUnclassified:
		return "unclassified"
	default:
		return "unclassified"
	}
}

// Valid reports whether the status is one of the three verdicts a sink may
// return.
func (s OutcomeStatus) Valid() bool {
	switch s {
	case OutcomeDelivered, OutcomeRetryable, OutcomePermanent:
		return true
	case OutcomeUnclassified:
		return false
	default:
		return false
	}
}

// Outcome is what a sink made of one delivery attempt.
type Outcome struct {
	// Status is the verdict. Its zero value is [OutcomeUnclassified], which is
	// not one.
	Status OutcomeStatus
	// Err is what went wrong, and is nil when Status is [OutcomeDelivered]. It
	// is recorded as the entry's last error, so it is read by whoever asks the
	// database why an event never arrived.
	Err error
}

// Delivered reports that the destination took the event.
func Delivered() Outcome { return Outcome{Status: OutcomeDelivered} }

// Retryable reports a failure another attempt might survive.
func Retryable(err error) Outcome { return Outcome{Status: OutcomeRetryable, Err: err} }

// Permanent reports a failure no further attempt can change.
func Permanent(err error) Outcome { return Outcome{Status: OutcomePermanent, Err: err} }

// Attempt is one delivery of one event to one sink.
//
// It carries the event together with the identity of this attempt, which is
// relay knowledge: the relay is what counts attempts and what decides there
// will be another. A sink that minted its own attempt identifier would be
// numbering something it cannot see, and every new sink would have to
// re-implement the same thing slightly differently.
type Attempt struct {
	// Event is the recorded event, exactly as it was written.
	Event hmntsk.Event
	// DeliveryID identifies this attempt and no other. A redelivery of the
	// same event carries the same [hmntsk.Event.ID] and a different DeliveryID,
	// which is what lets a receiver discard a repeat.
	DeliveryID string
	// Number is which attempt this is, counting from one. A sink may report it
	// to its destination, but nothing about the relay's behaviour depends on a
	// sink reading it.
	Number int
}

// Sink is one destination an event can be delivered to.
//
// A sink knows how to attempt one delivery and how to classify the result, and
// decides nothing else: claiming, retrying, backing off and dead-lettering all
// belong to the relay. That is what keeps a new destination to one module with
// no change here.
//
// Deliver is given the event and this attempt's identity, and nothing more.
// The event carries its own callback target, correlation data and task type, so
// a sink needs no second read — and, because the event describes the transition
// as it happened rather than the task as it is now, a delivery retried an hour
// later still describes what it claims to. A sink that genuinely needs live task
// state takes [hmntsk.Repository] in its own constructor and pays for that
// itself.
type Sink interface {
	// Name identifies the sink in the per-sink acceptance the relay records. It
	// must be stable across restarts: it is written to the database, and
	// renaming a sink re-delivers every event that sink has already taken.
	Name() string
	// Deliver attempts one delivery and classifies the result. It must respect
	// the deadline on ctx, and must not retry internally — retrying is the
	// relay's decision, and a sink that retries on its own multiplies the
	// relay's attempt budget by a number the relay does not know.
	Deliver(ctx context.Context, attempt Attempt) Outcome
}
