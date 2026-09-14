package tasknotify

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/relay"
)

// harness drives events through a projector and reads back what they produced.
type harness struct {
	t         *testing.T
	projector *Projector
	notifier  *notify.Service
	engine    *hmntsk.Service
	directory hmntsk.GroupResolver
}

// setup configures a harness.
type setup struct {
	// resolver is the engine's directory. The zero value resolves directory.
	resolver hmntsk.GroupResolver
	// store wraps the in-memory notification store, when set.
	store func(inner notify.Store) notify.Store
	types []hmntsk.TypeSpec
	opts  []Option
}

func (s setup) build(t *testing.T) *harness {
	t.Helper()

	resolver := s.resolver
	if resolver == nil {
		resolver = hmntsk.NewStaticAssignment(directory)
	}

	engine, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(resolver))
	require.NoError(t, err)

	for _, spec := range s.types {
		require.NoError(t, engine.Register(spec))
	}

	var store notify.Store = notify.NewMemoryStore()
	if s.store != nil {
		store = s.store(store)
	}

	notifier, err := notify.New(store)
	require.NoError(t, err)

	projector, err := New(engine, notifier, s.opts...)
	require.NoError(t, err)

	return &harness{t: t, projector: projector, notifier: notifier, engine: engine, directory: resolver}
}

// deliver projects events in the order given, requiring each to be delivered.
func (h *harness) deliver(events ...hmntsk.Event) {
	h.t.Helper()

	for _, e := range events {
		outcome := h.projector.Deliver(h.t.Context(), relay.Attempt{Event: e, DeliveryID: e.ID, Number: 1})
		require.Equal(h.t, relay.OutcomeDelivered, outcome.Status, "event %s: %v", e.ID, outcome.Err)
	}
}

// notifications lists a recipient's notifications on task-1, oldest first.
func (h *harness) notifications(recipient string) []notify.Notification {
	h.t.Helper()

	page, err := h.notifier.List(h.t.Context(), notify.ListQuery{
		Recipient: recipient, Subject: "task-1", Limit: notify.MaxListLimit,
	})
	require.NoError(h.t, err)

	slices.Reverse(page.Notifications)

	return page.Notifications
}

// count counts a recipient's notifications on task-1 of a kind in a state.
func (h *harness) count(recipient, kind string, state notify.State) int {
	h.t.Helper()

	n := 0

	for _, notification := range h.notifications(recipient) {
		if notification.Kind == kind && notification.State == state {
			n++
		}
	}

	return n
}

// at builds an event on task-1.
func at(id string, version int64, eventType hmntsk.EventType, status hmntsk.Status, actor string,
	mutate func(*hmntsk.Event),
) hmntsk.Event {
	e := hmntsk.Event{
		ID: id, Type: eventType, TaskID: "task-1", TaskType: "approval",
		Status: status, Version: version, Actor: actor, CreatedBy: "owner",
	}

	if mutate != nil {
		mutate(&e)
	}

	return e
}

// pool sets an event's candidate pool.
func pool(users ...string) func(*hmntsk.Event) {
	return func(e *hmntsk.Event) { e.Candidates = hmntsk.CandidatePool{Users: users} }
}

// holder sets an event's pool and assignee.
func holder(assignee string, users ...string) func(*hmntsk.Event) {
	return func(e *hmntsk.Event) {
		e.Candidates = hmntsk.CandidatePool{Users: users}
		e.Assignee = assignee
	}
}

// The events of one pooled task offered to alice, bob and carol, claimed by
// carol, released and completed.
var (
	offeredToThree = at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReady, "owner",
		pool("alice", "bob", "carol"))
	claimedByCarol = at("claimed", 2, hmntsk.EventTypeClaimed, hmntsk.StatusReserved, "carol",
		holder("carol", "alice", "bob", "carol"))
	releasedByCarol = at("released", 3, hmntsk.EventTypeReleased, hmntsk.StatusReady, "carol",
		func(e *hmntsk.Event) {
			pool("alice", "bob", "carol")(e)
			e.PreviousAssignee = "carol"
		})
	completedByCarol = at("completed", 3, hmntsk.EventTypeCompleted, hmntsk.StatusCompleted, "carol",
		holder("carol", "alice", "bob", "carol"))
)

func TestScenarios(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		setup  setup
		events []hmntsk.Event
		// act runs between the events and the assertions, when set.
		act    func(t *testing.T, h *harness)
		assert func(t *testing.T, h *harness)
	}

	cases := []testCase{
		{
			name: "a pooled task is offered to its candidates",
			events: []hmntsk.Event{at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReady, "owner",
				func(e *hmntsk.Event) {
					e.Candidates = hmntsk.CandidatePool{
						Users: []string{"alice"}, Groups: []string{"approvers"}, Excluded: []string{"carol"},
					}
				})},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("alice", KindOffer, notify.StateActive))
				assert.Equal(t, 1, h.count("bob", KindOffer, notify.StateActive))
				assert.Empty(t, h.notifications("carol"), "an excluded member is never offered")
			},
		},
		{
			name: "a creator who is also a candidate is not notified",
			events: []hmntsk.Event{at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReady, "alice",
				pool("alice", "bob"))},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("bob", KindOffer, notify.StateActive))
				assert.Empty(t, h.notifications("alice"))
			},
		},
		{
			name: "a reserved task is assigned",
			events: []hmntsk.Event{at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReserved, "owner",
				holder("dave", "dave"))},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("dave", KindAssigned, notify.StateActive))
			},
		},
		{
			name:   "claiming closes every offer and tells the others it was taken",
			events: []hmntsk.Event{offeredToThree, claimedByCarol},
			assert: func(t *testing.T, h *harness) {
				for _, recipient := range []string{"alice", "bob", "carol"} {
					offers := h.notifications(recipient)
					require.NotEmpty(t, offers, recipient)
					assert.Equal(t, notify.StateClosed, offers[0].State, recipient)
					assert.Equal(t, ReasonTaken, offers[0].ClosedReason, recipient)
				}

				for _, recipient := range []string{"alice", "bob"} {
					notifications := h.notifications(recipient)
					require.Len(t, notifications, 2, recipient)
					assert.Equal(t, KindTaken, notifications[1].Kind)
					assert.Equal(t, notify.StateActive, notifications[1].State)
					assert.Contains(t, notifications[1].Title, "carol", "a taken notice names who claimed it")
				}

				assert.Zero(t, h.count("carol", KindTaken, notify.StateActive), "the claimant is not told")
			},
		},
		{
			name:   "a read offer still yields a taken notification",
			events: []hmntsk.Event{offeredToThree},
			act: func(t *testing.T, h *harness) {
				offer := h.notifications("alice")[0]
				_, err := h.notifier.MarkRead(t.Context(), "alice", offer.ID)
				require.NoError(t, err)

				h.deliver(claimedByCarol)
			},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("alice", KindOffer, notify.StateClosed))
				assert.Equal(t, 1, h.count("alice", KindTaken, notify.StateActive))
			},
		},
		{
			name:   "released work is offered again",
			events: []hmntsk.Event{offeredToThree, claimedByCarol, releasedByCarol},
			assert: func(t *testing.T, h *harness) {
				for _, recipient := range []string{"alice", "bob"} {
					assert.Equal(t, 1, h.count(recipient, KindTaken, notify.StateClosed), recipient)
					assert.Equal(t, 1, h.count(recipient, KindOffer, notify.StateActive), recipient)
				}

				assert.Zero(t, h.count("carol", KindOffer, notify.StateActive), "the releaser is not offered")
			},
		},
		{
			name: "a released assignment is retired",
			events: []hmntsk.Event{
				at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReserved, "owner", holder("dave", "dave")),
				at("released", 2, hmntsk.EventTypeReleased, hmntsk.StatusReady, "dave", func(e *hmntsk.Event) {
					pool("dave")(e)
					e.PreviousAssignee = "dave"
				}),
			},
			assert: func(t *testing.T, h *harness) {
				notifications := h.notifications("dave")
				require.Len(t, notifications, 1, "the releaser is not offered their own task")
				assert.Equal(t, notify.StateClosed, notifications[0].State)
				assert.Equal(t, ReasonReleased, notifications[0].ClosedReason)
			},
		},
		{
			name: "delegating moves the assignment",
			events: []hmntsk.Event{
				at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReserved, "owner", holder("dave", "dave", "erin")),
				at("delegated", 2, hmntsk.EventTypeDelegated, hmntsk.StatusReserved, "dave", func(e *hmntsk.Event) {
					holder("erin", "dave", "erin")(e)
					e.PreviousAssignee = "dave"
				}),
			},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("erin", KindAssigned, notify.StateActive))

				dave := h.notifications("dave")
				require.Len(t, dave, 1, "the delegating holder receives nothing new")
				assert.Equal(t, notify.StateClosed, dave[0].State)
				assert.Equal(t, ReasonReassigned, dave[0].ClosedReason)
			},
		},
		{
			name: "widening offers the task only to newly eligible actors",
			events: []hmntsk.Event{
				at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReady, "owner", pool("alice", "zed")),
				at("escalated", 2, hmntsk.EventTypeEscalated, hmntsk.StatusReady, "sweeper", func(e *hmntsk.Event) {
					e.Candidates = hmntsk.CandidatePool{Users: []string{"alice", "zed"}, Groups: []string{"managers"}}
				}),
			},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("frank", KindOffer, notify.StateActive))
				assert.Len(t, h.notifications("alice"), 1, "alice still has exactly one offer")
			},
		},
		{
			name: "a held task is not offered on widening",
			events: []hmntsk.Event{
				at("escalated", 2, hmntsk.EventTypeEscalated, hmntsk.StatusReserved, "sweeper", func(e *hmntsk.Event) {
					e.Candidates = hmntsk.CandidatePool{Groups: []string{"managers"}}
					e.Assignee = "bob"
				}),
			},
			assert: func(t *testing.T, h *harness) {
				assert.Empty(t, h.notifications("frank"))
			},
		},
		{
			name:   "completion closes everything",
			events: []hmntsk.Event{offeredToThree, claimedByCarol, completedByCarol},
			assert: func(t *testing.T, h *harness) {
				for _, recipient := range []string{"alice", "bob", "carol"} {
					for _, notification := range h.notifications(recipient) {
						assert.Equal(t, notify.StateClosed, notification.State, recipient)
					}
				}

				taken := h.notifications("alice")[1]
				assert.Equal(t, "completed", taken.ClosedReason)
			},
		},
		{
			name:  "a narrowed closing set leaves a failed task's notifications open",
			setup: setup{opts: []Option{WithClosingStatuses(hmntsk.StatusCompleted, hmntsk.StatusExited)}},
			events: []hmntsk.Event{
				at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReserved, "owner", holder("dave", "dave")),
				at("failed", 3, hmntsk.EventTypeFailed, hmntsk.StatusFailed, "dave", holder("dave", "dave")),
			},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("dave", KindAssigned, notify.StateActive))
			},
		},
		{
			name: "suspension leaves notifications alone",
			events: []hmntsk.Event{
				offeredToThree,
				at("suspended", 2, hmntsk.EventTypeSuspended, hmntsk.StatusSuspended, "owner", pool("alice", "bob", "carol")),
			},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("alice", KindOffer, notify.StateActive))
			},
		},
		{
			name:  "a later group member is not offered retroactively",
			setup: setup{resolver: &changingDirectory{members: map[string][]string{"approvers": {"bob"}}}},
			events: []hmntsk.Event{at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReady, "owner",
				func(e *hmntsk.Event) { e.Candidates = hmntsk.CandidatePool{Groups: []string{"approvers"}} })},
			act: func(t *testing.T, h *harness) {
				directory, ok := h.directory.(*changingDirectory)
				require.True(t, ok)
				directory.join("approvers", "gina")
			},
			assert: func(t *testing.T, h *harness) {
				assert.Equal(t, 1, h.count("bob", KindOffer, notify.StateActive))
				assert.Empty(t, h.notifications("gina"))

				eligible, err := h.engine.Eligible(t.Context(),
					hmntsk.Task{Candidates: hmntsk.CandidatePool{Groups: []string{"approvers"}}}, "gina")
				require.NoError(t, err)
				assert.True(t, eligible, "the task is still among the ones gina may claim")
			},
		},
		{
			name: "notifications link to the task and to where its work is done",
			setup: setup{types: []hmntsk.TypeSpec{{
				Name:     "approval",
				Metadata: map[string]string{hmntsk.MetadataRoute: "/invoices/{correlation.ownerRef}/approve?task={task.id}"},
			}}},
			events: []hmntsk.Event{at("created", 1, hmntsk.EventTypeCreated, hmntsk.StatusReady, "owner",
				func(e *hmntsk.Event) {
					pool("alice")(e)
					e.Correlation = hmntsk.CorrelationData{OwnerType: "invoice", OwnerRef: "INV-42"}
				})},
			assert: func(t *testing.T, h *harness) {
				offer := h.notifications("alice")[0]
				assert.Equal(t, map[string]string{
					RelationTask:    "/v1/tasks/task-1",
					RelationContext: "/invoices/INV-42/approve?task=task-1",
				}, offer.Links)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := tc.setup.build(t)
			h.deliver(tc.events...)

			if tc.act != nil {
				tc.act(t, h)
			}

			tc.assert(t, h)
		})
	}
}

func TestOrdering(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		setup  setup
		run    func(t *testing.T, h *harness)
		assert func(t *testing.T, h *harness)
	}

	noActive := func(kind string) func(t *testing.T, h *harness) {
		return func(t *testing.T, h *harness) {
			for _, recipient := range []string{"alice", "bob", "carol"} {
				assert.Zero(t, h.count(recipient, kind, notify.StateActive), recipient)
			}
		}
	}

	cases := []testCase{
		{
			name:   "a creation retried after its claim leaves no active offer",
			run:    func(_ *testing.T, h *harness) { h.deliver(claimedByCarol, offeredToThree) },
			assert: noActive(KindOffer),
		},
		{
			name:   "a claim retried after completion leaves no active taken notice",
			run:    func(_ *testing.T, h *harness) { h.deliver(offeredToThree, completedByCarol, claimedByCarol) },
			assert: noActive(KindTaken),
		},
		{
			name: "a release projected before its claim keeps the release's offers and no taken notice",
			run:  func(_ *testing.T, h *harness) { h.deliver(offeredToThree, releasedByCarol, claimedByCarol) },
			assert: func(t *testing.T, h *harness) {
				for _, recipient := range []string{"alice", "bob"} {
					assert.Equal(t, 1, h.count(recipient, KindOffer, notify.StateActive), recipient)
					assert.Zero(t, h.count(recipient, KindTaken, notify.StateActive), recipient)
				}

				assert.Zero(t, h.count("carol", KindOffer, notify.StateActive))
			},
		},
		{
			name: "a claim failing after its close committed yields exactly one taken notice each",
			setup: setup{store: func(inner notify.Store) notify.Store {
				return &failAfterFirstClose{Store: inner}
			}},
			run: func(t *testing.T, h *harness) {
				h.deliver(offeredToThree)

				first := h.projector.Deliver(t.Context(), relay.Attempt{Event: claimedByCarol, DeliveryID: "a", Number: 1})
				require.Equal(t, relay.OutcomeRetryable, first.Status, "the injected failure is retried")

				h.deliver(claimedByCarol)
			},
			assert: func(t *testing.T, h *harness) {
				for _, recipient := range []string{"alice", "bob"} {
					assert.Equal(t, 1, h.count(recipient, KindTaken, notify.StateActive), recipient)
				}
			},
		},
		{
			name: "every event delivered twice changes nothing the second time",
			run: func(t *testing.T, h *harness) {
				sequence := []hmntsk.Event{
					offeredToThree, claimedByCarol, releasedByCarol,
					at("delegated", 4, hmntsk.EventTypeDelegated, hmntsk.StatusReserved, "bob", func(e *hmntsk.Event) {
						holder("alice", "alice", "bob", "carol")(e)
						e.PreviousAssignee = "bob"
					}),
					at("escalated", 5, hmntsk.EventTypeEscalated, hmntsk.StatusReserved, "sweeper", holder("alice", "alice")),
					at("completed", 6, hmntsk.EventTypeCompleted, hmntsk.StatusCompleted, "alice", holder("alice", "alice")),
				}

				h.deliver(sequence...)

				once := snapshot(h)

				h.deliver(sequence...)

				assert.Equal(t, once, snapshot(h))
			},
			assert: func(*testing.T, *harness) {},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := tc.setup.build(t)
			tc.run(t, h)
			tc.assert(t, h)
		})
	}
}

// snapshot captures the kind, state and reason of every notification on task-1.
func snapshot(h *harness) map[string][]string {
	out := make(map[string][]string)

	for _, recipient := range []string{"alice", "bob", "carol"} {
		for _, n := range h.notifications(recipient) {
			out[recipient] = append(out[recipient], n.Kind+"/"+string(n.State)+"/"+n.ClosedReason)
		}
	}

	return out
}

// failAfterFirstClose lets the first close commit and then reports the store as
// unavailable, the way a connection lost after a commit would.
type failAfterFirstClose struct {
	notify.Store

	closes atomic.Int32
}

func (s *failAfterFirstClose) Close(
	ctx context.Context, req notify.CloseRequest, at time.Time, ids notify.IDGenerator,
) (notify.CloseResult, error) {
	result, err := s.Store.Close(ctx, req, at, ids)
	if err != nil {
		return result, err
	}

	if s.closes.Add(1) == 1 {
		return notify.CloseResult{}, notify.ErrUnavailable
	}

	return result, nil
}

// changingDirectory is a directory whose membership can change between events.
type changingDirectory struct {
	mu      sync.Mutex
	members map[string][]string
}

func (d *changingDirectory) join(group, actor string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.members[group] = append(d.members[group], actor)
}

func (d *changingDirectory) GroupsOf(_ context.Context, actor string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var groups []string

	for group, members := range d.members {
		if slices.Contains(members, actor) {
			groups = append(groups, group)
		}
	}

	return groups, nil
}

func (d *changingDirectory) MembersOf(_ context.Context, group string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.members[group]...), nil
}
