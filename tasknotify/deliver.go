package tasknotify

import (
	"context"
	"errors"
	"fmt"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
)

// Deliver implements [relay.Sink]. It plans the event with the projector's
// rules, runs the plan's steps in order, and classifies the result:
//
//   - every step succeeded, including steps that created nothing because of
//     duplicates, suppression or coalescing: delivered;
//   - a failure another attempt might survive, such as an unavailable store, a
//     driver error, a passed deadline or a failing directory: retryable;
//   - a failure no attempt can change, such as invalid content from a host
//     func, a plan that breaks the [Rules] contract or a pool with groups and no
//     directory: reported to the error handler and recorded as delivered.
//
// It never reports a permanent failure. A permanent outcome dead-letters the
// whole event and stops its delivery to every other sink, and retrying a
// failure that cannot change would spend the event's shared attempt budget and
// dead-letter it anyway.
func (p *Projector) Deliver(ctx context.Context, attempt relay.Attempt) relay.Outcome {
	event := attempt.Event

	err := p.project(ctx, event)
	if err == nil {
		return relay.Delivered()
	}

	err = fmt.Errorf("tasknotify: project event %s on task %s: %w", event.ID, event.TaskID, err)

	if !unfixable(err) {
		return relay.Retryable(err)
	}

	p.onError(ctx, err)

	return relay.Delivered()
}

// project plans one event and runs the plan.
func (p *Projector) project(ctx context.Context, event hmntsk.Event) error {
	plan, err := p.rules.Plan(ctx, p.input(event))
	if err != nil {
		return err
	}

	if err := validatePlan(event, plan); err != nil {
		return err
	}

	for _, step := range plan.Steps {
		if step.Close != nil {
			if _, err := p.notifier.Close(ctx, *step.Close); err != nil {
				return err
			}

			continue
		}

		for batch := range chunks(step.Publish, p.batch) {
			if _, err := p.notifier.Publish(ctx, batch...); err != nil {
				return err
			}
		}
	}

	return nil
}

// validatePlan checks the part of the [Rules] contract a projector can see
// before running anything: every step is exactly one of a close or a publish,
// and every close and draft names the event's task.
func validatePlan(event hmntsk.Event, plan Plan) error {
	subject := string(event.TaskID)

	for i, step := range plan.Steps {
		switch {
		case (step.Close == nil) == (len(step.Publish) == 0):
			return fmt.Errorf("%w: step %d must be exactly one of a close or a publish", ErrInvalidPlan, i)
		case step.Close != nil && step.Close.Subject != subject:
			return fmt.Errorf("%w: step %d closes task %q, not the event's task %q",
				ErrInvalidPlan, i, step.Close.Subject, subject)
		}

		for _, draft := range step.Publish {
			if draft.Subject != subject {
				return fmt.Errorf("%w: step %d drafts on task %q, not the event's task %q",
					ErrInvalidPlan, i, draft.Subject, subject)
			}
		}
	}

	return nil
}

// unfixable reports whether no further attempt can change a failure.
//
// A directory that fails is worth retrying; a directory that is not configured
// at all is a wiring mistake, which is why group resolution is classified by
// its cause rather than by [hmntsk.ErrGroupResolution].
func unfixable(err error) bool {
	return errors.Is(err, ErrInvalidPlan) ||
		errors.Is(err, notify.ErrValidation) ||
		errors.Is(err, notify.ErrConfiguration) ||
		errors.Is(err, hmntsk.ErrConfiguration)
}

// chunks yields drafts in batches of at most size, in order.
func chunks(drafts []notify.Draft, size int) func(yield func([]notify.Draft) bool) {
	return func(yield func([]notify.Draft) bool) {
		for start := 0; start < len(drafts); start += size {
			if !yield(drafts[start:min(start+size, len(drafts))]) {
				return
			}
		}
	}
}
