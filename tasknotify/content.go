package tasknotify

import (
	"context"
	"encoding/json"

	"github.com/kartaladev/hmntsk"
)

// titleFor builds a notification's title: the host's [TitleFunc] when one is
// configured, and the default English title otherwise.
func (p *Projector) titleFor(ctx context.Context, in DraftInput) (string, error) {
	if p.titles != nil {
		return p.titles(ctx, in)
	}

	return defaultTitle(in), nil
}

// defaultTitle is the English title of the default kinds. A kind of the host's
// own has none; a client can still render it from its kind and data.
func defaultTitle(in DraftInput) string {
	switch in.Kind {
	case KindOffer:
		return "Task available: " + in.Event.TaskType
	case KindTaken:
		return "Task taken by " + in.Event.Actor + ": " + in.Event.TaskType
	case KindAssigned:
		return "Task assigned to you: " + in.Event.TaskType
	default:
		return ""
	}
}

// dataFor builds a notification's payload: the host's [DataFunc] when one is
// configured, and the event's default payload otherwise.
func (p *Projector) dataFor(ctx context.Context, in DraftInput, content eventContent) (json.RawMessage, error) {
	if p.data != nil {
		return p.data(ctx, in)
	}

	return content.data()
}

// payload is the default notification data.
//
// It describes the event and nothing the consumer owns beyond the three
// correlation fields the engine already filters on: no input, no output, no
// correlation extra and no candidate pool, so a notification never becomes a
// copy of a task's payload.
type payload struct {
	TaskID           string `json:"taskId"`
	TaskType         string `json:"taskType"`
	EventType        string `json:"eventType"`
	Status           string `json:"status"`
	Actor            string `json:"actor"`
	PreviousAssignee string `json:"previousAssignee,omitempty"`
	OwnerType        string `json:"ownerType,omitempty"`
	OwnerRef         string `json:"ownerRef,omitempty"`
	ActivityKey      string `json:"activityKey,omitempty"`
}

// defaultData renders the default payload, which depends on the event alone.
func defaultData(event hmntsk.Event) (json.RawMessage, error) {
	return json.Marshal(payload{
		TaskID:           string(event.TaskID),
		TaskType:         event.TaskType,
		EventType:        string(event.Type),
		Status:           string(event.Status),
		Actor:            event.Actor,
		PreviousAssignee: event.PreviousAssignee,
		OwnerType:        event.Correlation.OwnerType,
		OwnerRef:         event.Correlation.OwnerRef,
		ActivityKey:      event.Correlation.ActivityKey,
	})
}
