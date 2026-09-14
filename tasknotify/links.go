package tasknotify

import (
	"context"

	"github.com/kartaladev/hmntsk"
)

// linksFor builds a notification's links: the host's [LinksFunc] when one is
// configured, and the event's default links otherwise.
func (p *Projector) linksFor(ctx context.Context, in DraftInput, content eventContent) (map[string]string, error) {
	if p.links != nil {
		return p.links(ctx, in)
	}

	return content.links(), nil
}

// defaultLinks builds the task link from the configured template and, when the
// event's type is registered here with a route, the contextual link.
//
// A type that is unregistered on this instance, or that declares no route, gets
// only the task link. That is not an error: an instance serving some task types
// still projects the others.
func (p *Projector) defaultLinks(event hmntsk.Event) map[string]string {
	task := taskOf(event)
	links := map[string]string{RelationTask: hmntsk.ExpandRoute(p.taskLink, task)}

	spec, err := p.engine.Registry().Lookup(event.TaskType)
	if err != nil {
		return links
	}

	if route := spec.Metadata[hmntsk.MetadataRoute]; route != "" {
		links[RelationContext] = hmntsk.ExpandRoute(route, task)
	}

	return links
}

// taskOf is the task an event describes, reduced to what [hmntsk.ExpandRoute]
// reads: its identifier, its type and its correlation. Every placeholder
// ExpandRoute knows is covered, so a route expands from an event exactly as it
// would from the task.
func taskOf(event hmntsk.Event) hmntsk.Task {
	return hmntsk.Task{ID: event.TaskID, Type: event.TaskType, Correlation: event.Correlation}
}
