package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

// Reference values the suite builds its fixtures from. They are exported so
// that an adapter's own extra tests can reuse them.
const (
	// TaskType is the task type every fixture uses.
	TaskType = "approval"
	// Assignee is the actor that holds a reserved fixture.
	Assignee = "alice"
	// OtherActor is a second eligible actor.
	OtherActor = "bob"
)

// Reference is the instant every fixture is anchored to. It carries a
// microsecond component with nothing below it, which is exactly the precision
// every supported dialect must preserve.
var Reference = time.Date(2026, time.March, 1, 12, 0, 0, 123456000, time.UTC)

// NewTask builds a stored-shape task with everything populated, optionally
// adjusted by mutators.
func NewTask(id hmntsk.TaskID, mutate ...func(task *hmntsk.Task)) hmntsk.Task {
	due := Reference.Add(24 * time.Hour)

	task := hmntsk.Task{
		ID:       id,
		Type:     TaskType,
		Version:  1,
		Status:   hmntsk.StatusReady,
		Priority: hmntsk.PriorityDefault,
		Candidates: hmntsk.CandidatePool{
			Users:    []string{Assignee, OtherActor},
			Groups:   []string{"finance-approvers"},
			Excluded: []string{"mallory"},
		},
		Correlation: hmntsk.CorrelationData{
			OwnerType: "process", OwnerRef: "p-1", ActivityKey: "approve",
		},
		Callback: &hmntsk.CallbackTarget{
			Address:             "https://host.example/hook",
			ReferenceParameters: json.RawMessage(`{"corr":"abc","seq":9007199254740993}`),
		},
		Input:     json.RawMessage(`{"zulu":1,"amount":9007199254740993}`),
		CreatedBy: "system",
		CreatedAt: Reference,
		UpdatedAt: Reference,
		DueAt:     &due,
	}

	for _, apply := range mutate {
		apply(&task)
	}

	return task
}

// NewEvent builds an event for a task.
func NewEvent(task hmntsk.Task, eventType hmntsk.EventType, id string) hmntsk.Event {
	return hmntsk.Event{
		ID:          id,
		Type:        eventType,
		TaskID:      task.ID,
		TaskType:    task.Type,
		Status:      task.Status,
		Version:     task.Version,
		Actor:       Assignee,
		Assignee:    task.Assignee,
		OccurredAt:  Reference,
		Correlation: task.Correlation,
		Callback:    task.Callback,
		Transition: hmntsk.TransitionRecord{
			TaskID: task.ID, Version: task.Version, Operation: hmntsk.OpClaim,
			From: hmntsk.StatusReady, To: task.Status, Actor: Assignee, At: Reference,
		},
	}
}

// NewRecord builds a transition record for a task.
func NewRecord(task hmntsk.Task, op hmntsk.Operation, from, to hmntsk.Status) hmntsk.TransitionRecord {
	return hmntsk.TransitionRecord{
		TaskID:    task.ID,
		Version:   task.Version,
		Operation: op,
		From:      from,
		To:        to,
		Actor:     Assignee,
		Comment:   "recorded by the conformance suite",
		At:        Reference,
	}
}

// ids hands out distinct, ordered task identifiers within one case.
type ids struct{ n int }

// next returns the next identifier. They sort in creation order, as the
// engine's own generator guarantees, so keyset pagination behaves.
func (i *ids) next() hmntsk.TaskID {
	i.n++

	return hmntsk.TaskID(fmt.Sprintf("019243af-9f1c-7000-8000-%012d", i.n))
}

// Seed stores a task through an engine-led transaction and returns it as
// stored.
func Seed(t *testing.T, h Harness, task hmntsk.Task) hmntsk.Task {
	t.Helper()

	require.NoError(t, h.Store.Do(t.Context(), func(ctx context.Context) error {
		return h.Store.Create(ctx, task)
	}))

	stored, err := h.Store.Get(t.Context(), task.ID)
	require.NoError(t, err)

	return stored
}
