package tasknotify_test

import (
	"context"
	"fmt"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
	"github.com/kartaladev/hmntsk/tasknotify"
)

// Example wires a projector between the engine and a notification service,
// drives the relay once, and reads the offers a pooled task produced.
func Example() {
	ctx := context.Background()

	engine, err := hmntsk.New(memstore.New(),
		hmntsk.WithGroupResolver(hmntsk.NewStaticAssignment(map[string][]string{
			"approvers": {"alice", "bob"},
		})),
	)
	if err != nil {
		panic(err)
	}

	if err := engine.Register(hmntsk.TypeSpec{Name: "approval"}); err != nil {
		panic(err)
	}

	notifier, err := notify.New(notify.NewMemoryStore())
	if err != nil {
		panic(err)
	}

	projector, err := tasknotify.New(engine, notifier)
	if err != nil {
		panic(err)
	}

	r, err := relay.NewRelay(engine, relay.WithSinks(projector))
	if err != nil {
		panic(err)
	}

	pool := hmntsk.CandidatePool{Groups: []string{"approvers"}}
	if _, err := engine.Create(ctx, hmntsk.CreateRequest{Type: "approval", Actor: "owner", Candidates: &pool}); err != nil {
		panic(err)
	}

	if _, err := r.Relay(ctx); err != nil {
		panic(err)
	}

	for _, recipient := range []string{"alice", "bob"} {
		page, err := notifier.List(ctx, notify.ListQuery{Recipient: recipient})
		if err != nil {
			panic(err)
		}

		offer := page.Notifications[0]
		fmt.Println(recipient, offer.Kind, offer.State, offer.Title)
	}

	// Output:
	// alice offer ACTIVE Task available: approval
	// bob offer ACTIVE Task available: approval
}

// ExampleWithRules derives a host's rules from the defaults: the creator is told
// when their task completes, and every default step still runs.
func ExampleWithRules() {
	rules := tasknotify.RulesFunc(func(ctx context.Context, in tasknotify.Input) (tasknotify.Plan, error) {
		plan, err := tasknotify.DefaultRules.Plan(ctx, in)
		if err != nil || in.Event.Type != hmntsk.EventTypeCompleted || in.Event.CreatedBy == "" {
			return plan, err
		}

		draft, err := in.Draft(ctx, in.Event.CreatedBy, "done")
		if err != nil {
			return tasknotify.Plan{}, err
		}

		plan.Steps = append(plan.Steps, tasknotify.Step{Publish: []notify.Draft{draft}})

		return plan, nil
	})

	engine, err := hmntsk.New(memstore.New())
	if err != nil {
		panic(err)
	}

	notifier, err := notify.New(notify.NewMemoryStore())
	if err != nil {
		panic(err)
	}

	projector, err := tasknotify.New(engine, notifier, tasknotify.WithRules(rules))
	if err != nil {
		panic(err)
	}

	fmt.Println(projector.Name())

	// Output:
	// tasknotify
}
