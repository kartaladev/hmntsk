// Ports and rules for deciding who may act on a task.
//
//go:generate mockgen -source=assignment.go -package=hmntsk -destination=assignment_mock_test.go -typed

package hmntsk

import (
	"context"
	"maps"
	"slices"
	"sort"
	"time"
)

// GroupResolver answers questions about group membership against whatever
// directory, role store or identity system the host already runs. The engine
// ships no membership model of its own.
//
// Membership is resolved at the moment of the operation rather than snapshotted
// when the task was created, so somebody who joins finance-approvers today can
// act on a task created yesterday. That puts this port on the path of claim,
// delegate and inbox queries: an implementation backed by a slow directory
// should cache behind this interface.
//
// Implementations must be safe for concurrent use. An implementation that
// cannot answer must return an error rather than a negative answer — the engine
// distinguishes "this actor may not" from "I could not find out", and reports
// the second as a fault rather than a permission denial.
type GroupResolver interface {
	// GroupsOf returns every group actor belongs to. An actor belonging to no
	// group is not an error.
	GroupsOf(ctx context.Context, actor string) ([]string, error)
	// MembersOf returns every actor belonging to group. An empty or unknown
	// group is not an error.
	MembersOf(ctx context.Context, group string) ([]string, error)
}

// AssignmentStrategy decides whether a newly created task goes straight to one
// actor or into the pool. It is a port so that load-balancing or auto-claim
// strategies can be added later without touching lifecycle behaviour.
//
// Implementations must be safe for concurrent use.
type AssignmentStrategy interface {
	// Assign returns the actor a new task should be reserved for, given the
	// actors its pool resolved to. Returning the empty string leaves the task
	// in the pool for anyone eligible to claim.
	Assign(ctx context.Context, task Task, eligible []string) (string, error)
}

// StaticAssignment is an in-memory [GroupResolver] and [AssignmentStrategy]
// built from a fixed group-to-members map. It exists so that tests, and hosts
// with a handful of hard-coded groups, can run the whole engine without a
// directory.
//
// Its assignment behaviour is the engine's default: reserve a task when exactly
// one actor is eligible for it, and pool it otherwise.
//
// A StaticAssignment is safe for concurrent use and is immutable once built.
type StaticAssignment struct {
	members map[string][]string
	groups  map[string][]string
}

// Compile-time proof that the static implementation satisfies both ports.
var (
	_ GroupResolver      = (*StaticAssignment)(nil)
	_ AssignmentStrategy = (*StaticAssignment)(nil)
)

// NewStaticAssignment returns a resolver over the given group-to-members map.
// The map is copied, so the caller may keep mutating theirs.
func NewStaticAssignment(members map[string][]string) *StaticAssignment {
	static := &StaticAssignment{
		members: make(map[string][]string, len(members)),
		groups:  make(map[string][]string),
	}

	for group, actors := range members {
		static.members[group] = slices.Clone(actors)

		for _, actor := range actors {
			static.groups[actor] = append(static.groups[actor], group)
		}
	}

	for actor := range static.groups {
		sort.Strings(static.groups[actor])
	}

	return static
}

// GroupsOf implements [GroupResolver].
func (s *StaticAssignment) GroupsOf(_ context.Context, actor string) ([]string, error) {
	return slices.Clone(s.groups[actor]), nil
}

// MembersOf implements [GroupResolver].
func (s *StaticAssignment) MembersOf(_ context.Context, group string) ([]string, error) {
	return slices.Clone(s.members[group]), nil
}

// Assign implements [AssignmentStrategy] with the engine's default rule.
func (s *StaticAssignment) Assign(_ context.Context, _ Task, eligible []string) (string, error) {
	return singleCandidate(eligible), nil
}

// SingleCandidateStrategy is the default [AssignmentStrategy]: it reserves a
// task for the one actor eligible for it and pools every other task. Its zero
// value is ready to use.
type SingleCandidateStrategy struct{}

// Assign implements [AssignmentStrategy].
func (SingleCandidateStrategy) Assign(_ context.Context, _ Task, eligible []string) (string, error) {
	return singleCandidate(eligible), nil
}

// singleCandidate returns the sole eligible actor, or the empty string.
func singleCandidate(eligible []string) string {
	if len(eligible) == 1 {
		return eligible[0]
	}

	return ""
}

// IsEligible reports whether actor may act on a task with the given pool. An
// actor is eligible when they are named as a candidate user, or belong to a
// candidate group, and are not excluded.
//
// Exclusion is evaluated first and wins outright, so an actor who is both a
// candidate and excluded is refused without the directory being consulted at
// all.
//
// A resolver failure is returned as a [*GroupResolutionError], which is a fault
// and never a denial.
func IsEligible(ctx context.Context, resolver GroupResolver, pool CandidatePool, actor string) (bool, error) {
	if actor == "" {
		return false, nil
	}

	if slices.Contains(pool.Excluded, actor) {
		return false, nil
	}

	if slices.Contains(pool.Users, actor) {
		return true, nil
	}

	if len(pool.Groups) == 0 {
		return false, nil
	}

	if resolver == nil {
		return false, &GroupResolutionError{
			Actor:  actor,
			Groups: slices.Clone(pool.Groups),
			Cause:  &ConfigurationError{Detail: "no group resolver is configured"},
		}
	}

	actorGroups, err := resolver.GroupsOf(ctx, actor)
	if err != nil {
		return false, &GroupResolutionError{
			Actor: actor, Groups: slices.Clone(pool.Groups), Cause: err,
		}
	}

	for _, group := range actorGroups {
		if slices.Contains(pool.Groups, group) {
			return true, nil
		}
	}

	return false, nil
}

// ResolveCandidates expands a pool into the set of actors eligible for it, with
// exclusions removed. The result is sorted and deduplicated, so that two calls
// with the same directory produce the same answer.
//
// It is used at creation to decide between reserving a task for its single
// candidate, pooling it, and faulting it because nobody can do it.
func ResolveCandidates(ctx context.Context, resolver GroupResolver, pool CandidatePool) ([]string, error) {
	eligible := make(map[string]struct{}, len(pool.Users))

	for _, user := range pool.Users {
		if user != "" {
			eligible[user] = struct{}{}
		}
	}

	for _, group := range pool.Groups {
		if resolver == nil {
			return nil, &GroupResolutionError{
				Groups: slices.Clone(pool.Groups),
				Cause:  &ConfigurationError{Detail: "no group resolver is configured"},
			}
		}

		members, err := resolver.MembersOf(ctx, group)
		if err != nil {
			return nil, &GroupResolutionError{Groups: []string{group}, Cause: err}
		}

		for _, member := range members {
			if member != "" {
				eligible[member] = struct{}{}
			}
		}
	}

	for _, excluded := range pool.Excluded {
		delete(eligible, excluded)
	}

	out := slices.Collect(maps.Keys(eligible))
	sort.Strings(out)

	return out, nil
}

// Assign resolves a newly created task's candidate pool and moves it out of
// CREATED. The outcome is one of three:
//
//   - exactly one eligible actor, or a strategy that picks one: RESERVED for
//     that actor, because leaving a task in a pool of one is a pointless extra
//     step for the only person who can do it;
//   - more than one: READY, with no assignee;
//   - nobody at all, or a directory that cannot say: ERROR, with the reason in
//     history, because a task nobody can claim is a fault and not a task that
//     merely has not been claimed yet.
//
// A resolver failure lands in that third case rather than propagating. The
// task-lifecycle capability is explicit that candidate resolution failing
// during creation produces an ERROR task with the fault recorded, which is a
// different situation from a resolver failing while evaluating an actor's
// eligibility for an existing task: that one leaves the task untouched and
// returns the fault.
func Assign(
	ctx context.Context,
	resolver GroupResolver,
	strategy AssignmentStrategy,
	task Task,
	now time.Time,
) (Task, []Event, error) {
	eligible, err := ResolveCandidates(ctx, resolver, task.Candidates)
	if err != nil {
		return task.Fault("candidate resolution failed: "+err.Error(), now)
	}

	if len(eligible) == 0 {
		return task.Fault("no actor is eligible for this task", now)
	}

	if strategy == nil {
		strategy = SingleCandidateStrategy{}
	}

	assignee, err := strategy.Assign(ctx, task, eligible)
	if err != nil {
		return task.Fault("assignment strategy failed: "+err.Error(), now)
	}

	if assignee != "" && !slices.Contains(eligible, assignee) {
		return task.Fault("assignment strategy chose an ineligible actor: "+assignee, now)
	}

	return task.Activate(task.CreatedBy, assignee, now)
}

// Authorize applies the per-operation actor rule: eligibility for a claim,
// current-assignee identity for every operation that acts on work already held.
//
// Suspend and resume are the one softening. Both can apply to a pooled task,
// which has no assignee to compare an actor against; when somebody does hold
// the task, only they may suspend or resume it.
//
// Operations the engine performs on its own behalf — creation, escalation,
// obsolescence, faults — and cancellation, which belongs to the task's owner
// rather than its assignee, are not checked here; the host decides who may
// invoke them.
//
// The [Service] applies this after the state machine has accepted the move, not
// before. An operation that is illegal from the task's present state must be
// reported as a conflict even when the actor is also wrong, and the transitions
// are pure, so running them first costs nothing and discards cleanly.
func Authorize(ctx context.Context, resolver GroupResolver, task Task, op Operation, actor string) error {
	switch op {
	case OpClaim:
		return requireEligible(ctx, resolver, task, op, actor)
	case OpRelease, OpStart, OpSaveProgress, OpComplete, OpFail, OpDelegate:
		return task.requireAssignee(op, actor)
	case OpSuspend, OpResume:
		return task.requireAssigneeIfHeld(op, actor)
	case OpCreate, OpEscalate, OpCancel, OpObsolete, OpFault:
		return nil
	default:
		return nil
	}
}

// AuthorizeDelegate checks that the actor delegating a task holds it and that
// the actor they are delegating to is eligible for it.
func AuthorizeDelegate(ctx context.Context, resolver GroupResolver, task Task, actor, target string) error {
	if err := task.requireAssignee(OpDelegate, actor); err != nil {
		return err
	}

	return requireEligible(ctx, resolver, task, OpDelegate, target)
}

// requireEligible turns an eligibility verdict into an authorisation error,
// leaving a resolver fault to propagate as itself.
func requireEligible(ctx context.Context, resolver GroupResolver, task Task, op Operation, actor string) error {
	if err := requireActor(task.ID, op, actor); err != nil {
		return err
	}

	eligible, err := IsEligible(ctx, resolver, task.Candidates, actor)
	if err != nil {
		return err
	}

	if !eligible {
		return &AuthorizationError{
			TaskID: task.ID, Actor: actor, Operation: op.String(),
			Reason: "actor is not in the task's candidate pool",
		}
	}

	return nil
}
