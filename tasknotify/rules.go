package tasknotify

import (
	"context"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/notify"
)

// Rules decide which notifications an event opens and closes.
//
// A plan's steps run in order. Every step must be idempotent under
// redelivery, because the relay delivers at least once and a failed attempt
// may have run any prefix of the plan. A plan produces at most one draft per
// recipient per event, and every draft and close names the event's task.
type Rules interface {
	// Plan returns the steps that project one event.
	Plan(ctx context.Context, in Input) (Plan, error)
}

// RulesFunc adapts a function to the [Rules] interface.
type RulesFunc func(ctx context.Context, in Input) (Plan, error)

// Plan implements [Rules].
func (f RulesFunc) Plan(ctx context.Context, in Input) (Plan, error) { return f(ctx, in) }

// Input is what a plan is built from.
type Input struct {
	// Event is the event being projected.
	Event hmntsk.Event
	// Eligible expands Event.Candidates through the engine's directory and
	// removes Event.Actor. It is resolved once per Input, on first call, and
	// every call returns its own copy.
	Eligible func(ctx context.Context) ([]string, error)
	// Draft builds a draft of a kind for a recipient, with the projector's
	// subject, version, source, title, links and data.
	Draft func(ctx context.Context, recipient, kind string) (notify.Draft, error)
	// Closing reports whether Event.Status is in the projector's closing set.
	Closing bool
}

// Plan is an ordered list of steps.
type Plan struct {
	// Steps run in order.
	Steps []Step
}

// Step is one call to the notification service: exactly one of Close or
// Publish.
type Step struct {
	// Close closes notifications, optionally publishing a successor to each
	// recipient it closed.
	Close *notify.CloseRequest
	// Publish creates notifications from drafts.
	Publish []notify.Draft
}

// closingReasons names the reason a closing status records.
var closingReasons = map[hmntsk.Status]string{
	hmntsk.StatusCompleted: "completed",
	hmntsk.StatusFailed:    "failed",
	hmntsk.StatusError:     "errored",
	hmntsk.StatusExited:    "cancelled",
	hmntsk.StatusObsolete:  "obsoleted",
}

// DefaultRules are the rules a projector applies unless [WithRules] replaces
// them:
//
//   - a pooled creation offers the task to every eligible actor, and a reserved
//     one assigns it to its holder;
//   - a claim closes every offer and, in the same step, tells everyone whose
//     offer it closed, except the claimant, that the task was taken;
//   - a release closes taken and assigned notifications, then offers the task
//     again;
//   - a delegation closes every other assignment, then assigns the new holder;
//   - widening a pooled task offers it to eligible actors without an offer open;
//   - a closing status closes every notification of the task.
//
// The actor of an event is never notified of their own action. Every other
// transition changes nothing.
var DefaultRules Rules = RulesFunc(defaultPlan)

// defaultPlan is [DefaultRules].
func defaultPlan(ctx context.Context, in Input) (Plan, error) {
	event := in.Event
	subject := string(event.TaskID)

	if in.Closing {
		return plan(Step{Close: &notify.CloseRequest{
			Subject: subject, Version: event.Version, Reason: closingReasons[event.Status],
		}}), nil
	}

	switch event.Type {
	case hmntsk.EventTypeCreated:
		switch event.Status {
		case hmntsk.StatusReady:
			return offers(ctx, in, false)
		case hmntsk.StatusReserved:
			return assignment(ctx, in)
		default:
			return Plan{}, nil
		}

	case hmntsk.EventTypeClaimed:
		successor, err := in.Draft(ctx, "", KindTaken)
		if err != nil {
			return Plan{}, err
		}

		return plan(Step{Close: &notify.CloseRequest{
			Subject: subject, Kinds: []string{KindOffer}, Version: event.Version, Reason: ReasonTaken,
			Successor: &notify.Successor{
				SourceID: successor.SourceID, Kind: successor.Kind, Title: successor.Title,
				SubjectVersion: successor.SubjectVersion, Links: successor.Links, Data: successor.Data,
			},
			SuccessorSkip: []string{event.Actor},
		}}), nil

	case hmntsk.EventTypeReleased:
		retire := Step{Close: &notify.CloseRequest{
			Subject: subject, Kinds: []string{KindTaken, KindAssigned}, Version: event.Version, Reason: ReasonReleased,
		}}

		offered, err := offers(ctx, in, false)
		if err != nil {
			return Plan{}, err
		}

		return plan(append([]Step{retire}, offered.Steps...)...), nil

	case hmntsk.EventTypeDelegated:
		reassign := Step{Close: &notify.CloseRequest{
			Subject: subject, Kinds: []string{KindAssigned}, Version: event.Version, Reason: ReasonReassigned,
			Except: event.Assignee,
		}}

		assigned, err := assignment(ctx, in)
		if err != nil {
			return Plan{}, err
		}

		return plan(append([]Step{reassign}, assigned.Steps...)...), nil

	case hmntsk.EventTypeEscalated:
		if event.Status != hmntsk.StatusReady {
			return Plan{}, nil
		}

		return offers(ctx, in, true)

	default:
		return Plan{}, nil
	}
}

// offers publishes an offer to every eligible actor. A coalescing offer creates
// nothing for an actor who already has one open.
func offers(ctx context.Context, in Input, coalesce bool) (Plan, error) {
	eligible, err := in.Eligible(ctx)
	if err != nil {
		return Plan{}, err
	}

	drafts := make([]notify.Draft, 0, len(eligible))

	for _, actor := range eligible {
		draft, err := in.Draft(ctx, actor, KindOffer)
		if err != nil {
			return Plan{}, err
		}

		draft.Coalesce = coalesce
		drafts = append(drafts, draft)
	}

	if len(drafts) == 0 {
		return Plan{}, nil
	}

	return plan(Step{Publish: drafts}), nil
}

// assignment tells the event's assignee that the task is theirs, unless they
// are the actor who made it so.
func assignment(ctx context.Context, in Input) (Plan, error) {
	holder := in.Event.Assignee
	if holder == "" || holder == in.Event.Actor {
		return Plan{}, nil
	}

	draft, err := in.Draft(ctx, holder, KindAssigned)
	if err != nil {
		return Plan{}, err
	}

	return plan(Step{Publish: []notify.Draft{draft}}), nil
}

// plan builds a plan from steps.
func plan(steps ...Step) Plan { return Plan{Steps: steps} }
