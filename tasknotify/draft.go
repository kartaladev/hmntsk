package tasknotify

import (
	"context"
	"encoding/json"

	"github.com/kartaladev/hmntsk"
)

// DraftInput is what a title, a data payload or a set of links is built from.
type DraftInput struct {
	// Event is the event being projected.
	Event hmntsk.Event
	// Kind is the kind of notification being drafted.
	Kind string
	// Recipient is who it is for. It is empty for a successor, such as the
	// taken notice a claim publishes, which is drafted once and published to
	// every recipient the close reaches.
	Recipient string
}

// TitleFunc builds a notification's title.
type TitleFunc func(ctx context.Context, in DraftInput) (string, error)

// DataFunc builds a notification's JSON payload.
type DataFunc func(ctx context.Context, in DraftInput) (json.RawMessage, error)

// LinksFunc builds a notification's links, by relation name.
type LinksFunc func(ctx context.Context, in DraftInput) (map[string]string, error)
