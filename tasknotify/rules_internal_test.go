package tasknotify

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
)

// directory is the group membership every rules test resolves against.
var directory = map[string][]string{
	"approvers": {"bob", "carol"},
	"managers":  {"frank"},
}

// projectorWithDirectory builds a projector whose engine resolves groups
// through directory.
func projectorWithDirectory(t *testing.T, opts ...Option) *Projector {
	t.Helper()

	engine, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(hmntsk.NewStaticAssignment(directory)))
	require.NoError(t, err)

	notifier, err := notify.New(notify.NewMemoryStore())
	require.NoError(t, err)

	projector, err := New(engine, notifier, opts...)
	require.NoError(t, err)

	return projector
}

// closeStep is the part of a close a rules test asserts.
type closeStep struct {
	Kinds         []string
	Version       int64
	Reason        string
	Except        string
	Successor     string
	SuccessorSkip []string
}

// publishStep is the part of a draft a rules test asserts.
type publishStep struct {
	Recipient string
	Kind      string
	Coalesce  bool
}

// step is one step, reduced to what a rules test asserts.
type step struct {
	Close   *closeStep
	Publish []publishStep
}

// summarise reduces a plan to what a rules test asserts, and checks the fields
// every step shares: the subject, the version and the source of every draft and
// successor come from the event.
func summarise(t *testing.T, event hmntsk.Event, plan Plan) []step {
	t.Helper()

	out := make([]step, 0, len(plan.Steps))

	for _, s := range plan.Steps {
		require.NotEqual(t, s.Close == nil, len(s.Publish) == 0, "a step is exactly one of close or publish")

		if s.Close != nil {
			require.Equal(t, string(event.TaskID), s.Close.Subject)

			summary := &closeStep{
				Kinds: s.Close.Kinds, Version: s.Close.Version, Reason: s.Close.Reason,
				Except: s.Close.Except, SuccessorSkip: s.Close.SuccessorSkip,
			}

			if successor := s.Close.Successor; successor != nil {
				require.Equal(t, event.ID, successor.SourceID)
				require.Equal(t, event.Version, successor.SubjectVersion)
				require.NotEmpty(t, successor.Links[RelationTask], "a successor links to its task")

				summary.Successor = successor.Kind
			}

			out = append(out, step{Close: summary})

			continue
		}

		drafts := make([]publishStep, 0, len(s.Publish))

		for _, draft := range s.Publish {
			require.Equal(t, string(event.TaskID), draft.Subject)
			require.Equal(t, event.ID, draft.SourceID)
			require.Equal(t, event.Version, draft.SubjectVersion)
			require.NotEmpty(t, draft.Links[RelationTask], "a draft links to its task")
			require.NotEmpty(t, draft.Data, "a draft carries the default payload")

			drafts = append(drafts, publishStep{Recipient: draft.Recipient, Kind: draft.Kind, Coalesce: draft.Coalesce})
		}

		out = append(out, step{Publish: drafts})
	}

	return out
}

// event builds an event on task-1 at version 5.
func event(eventType hmntsk.EventType, status hmntsk.Status, actor string, mutate func(*hmntsk.Event)) hmntsk.Event {
	e := hmntsk.Event{
		ID: "event-5", Type: eventType, TaskID: "task-1", TaskType: "approval",
		Status: status, Version: 5, Actor: actor,
	}

	if mutate != nil {
		mutate(&e)
	}

	return e
}

func TestDefaultRulesPlan(t *testing.T) {
	t.Parallel()

	pool := hmntsk.CandidatePool{
		Users: []string{"alice", "admin"}, Groups: []string{"approvers"}, Excluded: []string{"carol"},
	}
	withPool := func(e *hmntsk.Event) { e.Candidates = pool }

	type testCase struct {
		name   string
		opts   []Option
		event  hmntsk.Event
		assert func(t *testing.T, event hmntsk.Event, plan Plan, err error)
	}

	expect := func(want ...step) func(t *testing.T, event hmntsk.Event, plan Plan, err error) {
		return func(t *testing.T, event hmntsk.Event, plan Plan, err error) {
			t.Helper()

			require.NoError(t, err)
			assert.Equal(t, want, summarise(t, event, plan))
		}
	}

	nothing := func(t *testing.T, _ hmntsk.Event, plan Plan, err error) {
		t.Helper()

		require.NoError(t, err)
		assert.Empty(t, plan.Steps)
	}

	offers := func(coalesce bool, recipients ...string) step {
		drafts := make([]publishStep, 0, len(recipients))
		for _, recipient := range recipients {
			drafts = append(drafts, publishStep{Recipient: recipient, Kind: KindOffer, Coalesce: coalesce})
		}

		return step{Publish: drafts}
	}

	cases := []testCase{
		{
			name:   "a pooled creation offers the task to its candidates, less exclusions and the actor",
			event:  event(hmntsk.EventTypeCreated, hmntsk.StatusReady, "admin", withPool),
			assert: expect(offers(false, "alice", "bob")),
		},
		{
			name: "a reserved creation assigns the task",
			event: event(hmntsk.EventTypeCreated, hmntsk.StatusReserved, "admin", func(e *hmntsk.Event) {
				e.Assignee = "dave"
			}),
			assert: expect(step{Publish: []publishStep{{Recipient: "dave", Kind: KindAssigned}}}),
		},
		{
			name: "a creation reserved for its own creator notifies nobody",
			event: event(hmntsk.EventTypeCreated, hmntsk.StatusReserved, "dave", func(e *hmntsk.Event) {
				e.Assignee = "dave"
			}),
			assert: nothing,
		},
		{
			name: "a claim closes every offer and sends taken to everyone closed but the claimant",
			event: event(hmntsk.EventTypeClaimed, hmntsk.StatusReserved, "bob", func(e *hmntsk.Event) {
				withPool(e)
				e.Assignee = "bob"
			}),
			assert: expect(step{Close: &closeStep{
				Kinds: []string{KindOffer}, Version: 5, Reason: ReasonTaken,
				Successor: KindTaken, SuccessorSkip: []string{"bob"},
			}}),
		},
		{
			name: "a release retires taken and assigned notices, then offers the task to all but the releaser",
			event: event(hmntsk.EventTypeReleased, hmntsk.StatusReady, "bob", func(e *hmntsk.Event) {
				withPool(e)
				e.PreviousAssignee = "bob"
			}),
			assert: expect(
				step{Close: &closeStep{Kinds: []string{KindTaken, KindAssigned}, Version: 5, Reason: ReasonReleased}},
				offers(false, "admin", "alice"),
			),
		},
		{
			name: "a delegation closes other assignments, then assigns the new holder",
			event: event(hmntsk.EventTypeDelegated, hmntsk.StatusReserved, "dave", func(e *hmntsk.Event) {
				e.Assignee = "erin"
				e.PreviousAssignee = "dave"
			}),
			assert: expect(
				step{Close: &closeStep{Kinds: []string{KindAssigned}, Version: 5, Reason: ReasonReassigned, Except: "erin"}},
				step{Publish: []publishStep{{Recipient: "erin", Kind: KindAssigned}}},
			),
		},
		{
			name: "widening a pooled task offers it, coalescing with offers already open",
			event: event(hmntsk.EventTypeEscalated, hmntsk.StatusReady, "sweeper", func(e *hmntsk.Event) {
				e.Candidates = hmntsk.CandidatePool{Users: []string{"alice"}, Groups: []string{"managers"}}
			}),
			assert: expect(offers(true, "alice", "frank")),
		},
		{
			name: "widening a held task offers nothing",
			event: event(hmntsk.EventTypeEscalated, hmntsk.StatusReserved, "sweeper", func(e *hmntsk.Event) {
				e.Candidates = hmntsk.CandidatePool{Groups: []string{"managers"}}
				e.Assignee = "bob"
			}),
			assert: nothing,
		},
		{
			name:   "completion closes every kind",
			event:  event(hmntsk.EventTypeCompleted, hmntsk.StatusCompleted, "bob", withPool),
			assert: expect(step{Close: &closeStep{Version: 5, Reason: "completed"}}),
		},
		{
			name:   "failure closes every kind",
			event:  event(hmntsk.EventTypeFailed, hmntsk.StatusFailed, "bob", nil),
			assert: expect(step{Close: &closeStep{Version: 5, Reason: "failed"}}),
		},
		{
			name:   "a fault closes every kind",
			event:  event(hmntsk.EventTypeErrored, hmntsk.StatusError, "", nil),
			assert: expect(step{Close: &closeStep{Version: 5, Reason: "errored"}}),
		},
		{
			name:   "cancellation closes every kind",
			event:  event(hmntsk.EventTypeCancelled, hmntsk.StatusExited, "owner", nil),
			assert: expect(step{Close: &closeStep{Version: 5, Reason: "cancelled"}}),
		},
		{
			name:   "supersession closes every kind",
			event:  event(hmntsk.EventTypeObsoleted, hmntsk.StatusObsolete, "sweeper", nil),
			assert: expect(step{Close: &closeStep{Version: 5, Reason: "obsoleted"}}),
		},
		{
			name:   "a narrowed closing set leaves a failed task's notifications alone",
			opts:   []Option{WithClosingStatuses(hmntsk.StatusCompleted, hmntsk.StatusExited)},
			event:  event(hmntsk.EventTypeFailed, hmntsk.StatusFailed, "bob", nil),
			assert: nothing,
		},
		{name: "a start changes nothing", event: event(hmntsk.EventTypeStarted, hmntsk.StatusInProgress, "bob", nil), assert: nothing},
		{
			name:   "a suspension changes nothing",
			event:  event(hmntsk.EventTypeSuspended, hmntsk.StatusSuspended, "bob", withPool),
			assert: nothing,
		},
		{
			name:   "a resumption changes nothing",
			event:  event(hmntsk.EventTypeResumed, hmntsk.StatusReady, "bob", withPool),
			assert: nothing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			projector := projectorWithDirectory(t, tc.opts...)

			plan, err := DefaultRules.Plan(t.Context(), projector.input(tc.event))
			tc.assert(t, tc.event, plan, err)
		})
	}
}

func TestInputEligibleIsResolvedOnce(t *testing.T) {
	t.Parallel()

	projector := projectorWithDirectory(t)
	in := projector.input(event(hmntsk.EventTypeCreated, hmntsk.StatusReady, "admin", func(e *hmntsk.Event) {
		e.Candidates = hmntsk.CandidatePool{Users: []string{"alice", "admin"}}
	}))

	first, err := in.Eligible(t.Context())
	require.NoError(t, err)

	first[0] = "mallory"

	second, err := in.Eligible(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"alice"}, second, "a caller changing its copy cannot change the next caller's")
}
