package hmntsk

import (
	"context"
	"encoding/json"
	"fmt"
)

// Kind is a compile-time-typed handle on one task type.
//
// The core stores payloads as opaque bytes, because storage, queries and the
// HTTP transports all handle many task types at once and a handler receiving
// arbitrary JSON cannot select a generic instantiation. Business code handles
// one type at a time and wants types, so the generics live here, at the edge,
// confined to one file: the Repository port, the aggregate and the transports
// all stay untyped.
//
// The typed path is purely additive. Anything done through it is
// indistinguishable, in storage and in events, from the same thing done through
// [Service] directly, and both paths remain usable at once.
//
// Progress saves are deliberately absent. Go cannot express a partial of a
// struct, and a draft is partial by definition; full type checking happens at
// [Kind.Complete], which is where the specification puts full validation
// anyway.
type Kind[In, Out any] struct {
	service *Service
	name    string
}

// Define registers a task type and returns a typed handle on it.
//
// spec.Name is required. Where spec leaves a schema empty, one is derived from
// In or Out by [DeriveSchema]; where spec supplies one, it is used unchanged,
// because a hand-written schema can say things a Go type cannot.
//
// Registering the same name twice with different configuration fails here,
// exactly as [Registry.Register] does.
func Define[In, Out any](svc *Service, spec TypeSpec) (Kind[In, Out], error) {
	if svc == nil {
		return Kind[In, Out]{}, &ConfigurationError{Detail: "a service is required to define a task type"}
	}

	if len(spec.InputSchema) == 0 {
		derived, err := DeriveSchema(*new(In))
		if err != nil {
			return Kind[In, Out]{}, err
		}

		spec.InputSchema = derived
	}

	if len(spec.OutputSchema) == 0 {
		derived, err := DeriveSchema(*new(Out))
		if err != nil {
			return Kind[In, Out]{}, err
		}

		spec.OutputSchema = derived
	}

	if err := svc.Register(spec); err != nil {
		return Kind[In, Out]{}, err
	}

	return Kind[In, Out]{service: svc, name: spec.Name}, nil
}

// MustDefine is [Define] for a package-level variable, where there is nowhere
// to return an error to. It panics if registration fails, which is a
// programming error discovered at startup rather than a runtime condition.
func MustDefine[In, Out any](svc *Service, spec TypeSpec) Kind[In, Out] {
	kind, err := Define[In, Out](svc, spec)
	if err != nil {
		panic(err)
	}

	return kind
}

// Name returns the registered type name this handle refers to.
func (k Kind[In, Out]) Name() string { return k.name }

// Create creates a task of this type from a typed input.
//
// req supplies everything else — correlation, candidates, deadline, callback.
// Its Type and Input fields are ignored: the handle already knows the type, and
// input comes from the typed argument.
func (k Kind[In, Out]) Create(ctx context.Context, input In, req CreateRequest) (Result, error) {
	if k.service == nil {
		return Result{}, &ConfigurationError{Detail: "task kind was not defined against a service"}
	}

	payload, err := json.Marshal(input)
	if err != nil {
		return Result{}, &ValidationError{
			Subject: "input",
			Issues:  []ValidationIssue{{Detail: "could not be encoded: " + err.Error()}},
		}
	}

	req.Type = k.name
	req.Input = payload

	return k.service.Create(ctx, req)
}

// Complete closes a task of this type with a typed output.
func (k Kind[In, Out]) Complete(ctx context.Context, output Out, req TaskRequest) (Result, error) {
	if k.service == nil {
		return Result{}, &ConfigurationError{Detail: "task kind was not defined against a service"}
	}

	payload, err := json.Marshal(output)
	if err != nil {
		return Result{}, &ValidationError{
			Subject: "output",
			Issues:  []ValidationIssue{{Detail: "could not be encoded: " + err.Error()}},
		}
	}

	return k.service.Complete(ctx, CompleteRequest{TaskRequest: req, Output: payload})
}

// TypedTask is a task together with its payloads decoded into Go values. The
// undecoded task is kept, so nothing is lost: the raw bytes, the status, the
// version and the history are all still there.
type TypedTask[In, Out any] struct {
	// Task is the task exactly as the untyped path would return it.
	Task Task
	// Input is the task's input payload, decoded.
	Input In
	// Output is the task's output payload, decoded. It is the zero value while
	// the task is unfinished.
	Output Out
}

// Get reads a task of this type and decodes its payloads.
//
// It refuses a task of another type rather than decoding it into the wrong Go
// value, because a silently zero-valued struct is far worse than an error.
func (k Kind[In, Out]) Get(ctx context.Context, id TaskID) (TypedTask[In, Out], error) {
	var typed TypedTask[In, Out]

	if k.service == nil {
		return typed, &ConfigurationError{Detail: "task kind was not defined against a service"}
	}

	task, err := k.service.Get(ctx, id)
	if err != nil {
		return typed, err
	}

	if task.Type != k.name {
		return typed, &ValidationError{
			Subject: "request",
			Issues: []ValidationIssue{{
				Detail: fmt.Sprintf("task %s is of type %q, not %q", id, task.Type, k.name),
			}},
		}
	}

	typed.Task = task

	if len(task.Input) > 0 {
		if err := json.Unmarshal(task.Input, &typed.Input); err != nil {
			return typed, &ValidationError{
				Subject: "input",
				Issues:  []ValidationIssue{{Detail: "could not be decoded: " + err.Error()}},
			}
		}
	}

	if len(task.Output) > 0 {
		if err := json.Unmarshal(task.Output, &typed.Output); err != nil {
			return typed, &ValidationError{
				Subject: "output",
				Issues:  []ValidationIssue{{Detail: "could not be decoded: " + err.Error()}},
			}
		}
	}

	return typed, nil
}

// OnCompleted returns an [EventHandler] that calls handle for completions of
// this task type only, with the output already decoded. Events of other types,
// and of other task types, pass through untouched.
//
// Register it with [WithEventHandlers] like any other consumer: the typed
// facade adds no second delivery mechanism.
func (k Kind[In, Out]) OnCompleted(handle func(ctx context.Context, event Event, output Out) error) EventHandler {
	return EventHandlerFunc(func(ctx context.Context, event Event) error {
		if event.Type != EventTypeCompleted || event.TaskType != k.name {
			return nil
		}

		var output Out

		if len(event.Output) > 0 {
			if err := json.Unmarshal(event.Output, &output); err != nil {
				return &ValidationError{
					Subject: "output",
					Issues:  []ValidationIssue{{Detail: "could not be decoded: " + err.Error()}},
				}
			}
		}

		return handle(ctx, event, output)
	})
}
