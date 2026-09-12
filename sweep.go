package hmntsk

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultLeaseDuration is how long a sweeper holds a task by default.
//
// It is also the escalation back-off: a task escalated by widening keeps its
// lease, so it cannot be escalated again until the lease expires.
const DefaultLeaseDuration = 5 * time.Minute

// DefaultSweepBatch is how many overdue tasks one sweep claims by default.
const DefaultSweepBatch = 100

// Sweeper finds tasks that have passed their deadline and escalates them.
//
// Deadlines are evaluated here and nowhere else. Reading a task never escalates
// it, because a read that mutates is a read that cannot be cached, cannot be
// done on a replica and surprises everyone who ever calls it.
//
// Nothing here starts on its own. Constructing a Sweeper starts no goroutine,
// no timer and no polling; the host drives it, from a long-running goroutine
// through [Sweeper.Run], from its own scheduler through [Sweeper.Sweep], or by
// hand. An embedded engine does not get to decide that the process it lives in
// now has a background thread.
type Sweeper struct {
	service *Service
	owner   string
	lease   time.Duration
	batch   int
	types   []string
	onError func(ctx context.Context, err error)
}

// SweepOption configures a [Sweeper].
type SweepOption func(*Sweeper)

// WithSweepOwner names the sweeper in the leases it takes, so that an abandoned
// lease is traceable to the instance that abandoned it. The default is a
// generated identifier.
func WithSweepOwner(owner string) SweepOption {
	return func(s *Sweeper) {
		if owner != "" {
			s.owner = owner
		}
	}
}

// WithLeaseDuration sets how long a sweeper holds a task.
//
// It is a back-off as much as a lock: too short and two instances will both
// reach a slow escalation, too long and a crashed sweeper strands its batch for
// that long. Minutes, not seconds or hours.
func WithLeaseDuration(duration time.Duration) SweepOption {
	return func(s *Sweeper) {
		if duration > 0 {
			s.lease = duration
		}
	}
}

// WithSweepBatch caps how many tasks one sweep claims.
func WithSweepBatch(batch int) SweepOption {
	return func(s *Sweeper) {
		if batch > 0 {
			s.batch = batch
		}
	}
}

// WithSweepTypes restricts a sweeper to certain task types, so that a host can
// run one sweeper per type with different lease durations.
func WithSweepTypes(types ...string) SweepOption {
	return func(s *Sweeper) { s.types = append(s.types, types...) }
}

// WithSweepErrorHandler supplies a hook for errors raised while sweeping. The
// default does nothing, which is safe but silent.
func WithSweepErrorHandler(handler func(ctx context.Context, err error)) SweepOption {
	return func(s *Sweeper) {
		if handler != nil {
			s.onError = handler
		}
	}
}

// NewSweeper returns a sweeper over a service. It starts nothing.
func NewSweeper(svc *Service, opts ...SweepOption) (*Sweeper, error) {
	if svc == nil {
		return nil, &ConfigurationError{Detail: "a service is required to sweep"}
	}

	owner, err := svc.eventIDs.NewTaskID()
	if err != nil {
		return nil, err
	}

	sweeper := &Sweeper{
		service: svc,
		owner:   "sweeper-" + owner.String(),
		lease:   DefaultLeaseDuration,
		batch:   DefaultSweepBatch,
		onError: func(context.Context, error) {},
	}

	for _, opt := range opts {
		opt(sweeper)
	}

	return sweeper, nil
}

// Owner returns the identifier this sweeper records in the leases it takes.
func (s *Sweeper) Owner() string { return s.owner }

// SweepResult is what one sweep did.
type SweepResult struct {
	// Claimed is how many overdue tasks this sweeper leased. Tasks another
	// sweeper had already leased are not counted: they are not this sweeper's
	// to escalate.
	Claimed int
	// Escalated is how many of them were escalated.
	Escalated int
	// Exempted is how many were left alone because their policy said so.
	Exempted int
	// Events are the events the escalations produced, already durable.
	Events []Event
}

// Sweep claims a batch of overdue tasks and escalates them.
//
// Claiming is a conditional update on the lease columns, taken and committed
// before anything is escalated, so a second instance sweeping at the same
// moment finds nothing left to take. It needs no lock primitive, which is what
// lets it work identically on a database that has none.
//
// Escalation of each claimed task runs in its own transaction. A task that
// fails to escalate does not take the rest of the batch down with it; the
// error reaches the configured handler and the lease expires on its own.
func (s *Sweeper) Sweep(ctx context.Context) (SweepResult, error) {
	claimed, err := s.service.ClaimOverdue(ctx, LeaseRequest{
		Now:      s.service.clock.Now(),
		Owner:    s.owner,
		Duration: s.lease,
		Limit:    s.batch,
		Types:    s.types,
	})
	if err != nil {
		return SweepResult{}, err
	}

	result := SweepResult{Claimed: len(claimed)}

	for _, task := range claimed {
		policy := s.service.escalationPolicy(task)

		if reason := exemption(task, policy); reason != "" {
			// The lease is left to expire rather than released, which stops the
			// next sweep a second later from reconsidering the same task and
			// reaching the same conclusion.
			result.Exempted++

			continue
		}

		version := task.Version

		escalated, err := s.service.Escalate(ctx, TaskRequest{
			TaskID:  task.ID,
			Actor:   s.owner,
			Version: &version,
			Comment: "deadline passed",
		})
		if err != nil {
			// A conflict means somebody got to the task between the lease and
			// the escalation. That is the mechanism working.
			if !errors.Is(err, ErrConflict) {
				s.onError(ctx, fmt.Errorf("hmntsk: escalate task %s: %w", task.ID, err))
			}

			continue
		}

		result.Escalated++
		result.Events = append(result.Events, escalated.Events...)

		if escalated.Pending() {
			if err := escalated.Dispatch(ctx); err != nil {
				s.onError(ctx, err)
			}
		}
	}

	return result, nil
}

// Run sweeps every interval until ctx is cancelled.
//
// It blocks. The host decides whether that is a goroutine of its own, a
// scheduled job, or a command; the engine starts nothing by itself.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return &ConfigurationError{Detail: "a sweep interval must be positive"}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := s.Sweep(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			s.onError(ctx, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// exemption reports why a task must not be escalated, or the empty string when
// it may be.
func exemption(task Task, policy *EscalationPolicy) string {
	switch {
	case task.Status.IsTerminal():
		return "the task is already closed"
	case task.Status == StatusSuspended:
		return "the task is suspended"
	case policy == nil:
		return ""
	case policy.ExemptInProgress && task.Status == StatusInProgress:
		return "the policy exempts work already started"
	case policy.MaxEscalations > 0 && task.EscalationCount >= policy.MaxEscalations:
		return "the task has been escalated as often as its policy allows"
	default:
		return ""
	}
}

// ClaimOverdue takes a time-bounded lease on a batch of overdue tasks and
// returns them.
//
// It is exposed so that a host can drive escalation from its own scheduler
// without the engine's sweeper, and so that the claiming and the acting can sit
// in separate transactions: the lease has to be committed before anything is
// escalated, or a second instance would not see it.
func (s *Service) ClaimOverdue(ctx context.Context, lease LeaseRequest) ([]Task, error) {
	if lease.Duration <= 0 {
		lease.Duration = DefaultLeaseDuration
	}

	if lease.Limit <= 0 {
		lease.Limit = DefaultSweepBatch
	}

	if lease.Now.IsZero() {
		lease.Now = s.clock.Now()
	}

	var claimed []Task

	err := s.store.Do(ctx, func(ctx context.Context) error {
		var claimErr error

		claimed, claimErr = s.store.ClaimOverdue(ctx, lease)

		return claimErr
	})
	if err != nil {
		return nil, err
	}

	return claimed, nil
}
