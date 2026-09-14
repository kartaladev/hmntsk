package tasknotify

import "errors"

// ErrInvalidPlan reports a plan that breaks the [Rules] contract: a step that is
// neither a close nor a publish, or a draft or close naming a task other than
// the event's. No retry can change it, so a projector reports it to its error
// handler and records the event as delivered.
var ErrInvalidPlan = errors.New("tasknotify: invalid plan")
