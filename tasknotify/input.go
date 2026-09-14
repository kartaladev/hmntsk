package tasknotify

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/notify"
)

// eventContent is the content of an event's drafts that depends on the event
// alone: the default links and the default data. It is computed at most once
// per event, so that a fan-out to many recipients does not repeat the registry
// lookup, the route expansion and the payload encoding for each of them.
type eventContent struct {
	links func() map[string]string
	data  func() (json.RawMessage, error)
}

// contentFor defers an event's default content until a draft first needs it.
func (p *Projector) contentFor(event hmntsk.Event) eventContent {
	return eventContent{
		links: sync.OnceValue(func() map[string]string { return p.defaultLinks(event) }),
		data:  sync.OnceValues(func() (json.RawMessage, error) { return defaultData(event) }),
	}
}

// input builds the [Input] a plan is made from.
//
// Its Eligible expands the pool through the engine at most once, and only when
// a rule asks for it, so that an event whose rules need no candidates never
// consults the directory.
func (p *Projector) input(event hmntsk.Event) Input {
	var (
		once     sync.Once
		eligible []string
		failure  error
	)

	content := p.contentFor(event)

	return Input{
		Event: event,
		Eligible: func(ctx context.Context) ([]string, error) {
			once.Do(func() {
				resolved, err := p.engine.ResolveCandidates(ctx, event.Candidates)
				if err != nil {
					failure = err

					return
				}

				eligible = slices.DeleteFunc(resolved, func(actor string) bool { return actor == event.Actor })
			})

			return slices.Clone(eligible), failure
		},
		Draft: func(ctx context.Context, recipient, kind string) (notify.Draft, error) {
			return p.draft(ctx, DraftInput{Event: event, Kind: kind, Recipient: recipient}, content)
		},
		Closing: p.closes(event.Status),
	}
}

// draft builds one draft on the event's task, at the event's version, sourced
// from the event, with the projector's title, links and data.
func (p *Projector) draft(ctx context.Context, in DraftInput, content eventContent) (notify.Draft, error) {
	title, err := p.titleFor(ctx, in)
	if err != nil {
		return notify.Draft{}, err
	}

	links, err := p.linksFor(ctx, in, content)
	if err != nil {
		return notify.Draft{}, err
	}

	data, err := p.dataFor(ctx, in, content)
	if err != nil {
		return notify.Draft{}, err
	}

	return notify.Draft{
		Recipient:      in.Recipient,
		SourceID:       in.Event.ID,
		Subject:        string(in.Event.TaskID),
		Kind:           in.Kind,
		Title:          title,
		SubjectVersion: in.Event.Version,
		Links:          links,
		Data:           data,
	}, nil
}
