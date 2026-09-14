package tasknotify

import (
	"context"
	"strings"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/notify"
)

// Kinds are the notification kinds the default rules publish. They are
// conventions a client matches on; a host using its own [Rules] may publish
// others.
const (
	// KindOffer tells a candidate that a task in its pool is theirs to claim.
	KindOffer = "offer"
	// KindTaken tells a candidate whose offer closed that someone claimed the
	// task.
	KindTaken = "taken"
	// KindAssigned tells an actor that a task is reserved for them.
	KindAssigned = "assigned"
)

// Reasons are recorded on the notifications the default rules close. A task
// reaching a closing status closes its notifications with the status's name in
// lower case instead, such as "completed".
const (
	// ReasonTaken closes an offer because someone claimed the task.
	ReasonTaken = "taken"
	// ReasonReleased closes taken and assigned notifications because the task
	// returned to its pool.
	ReasonReleased = "released"
	// ReasonReassigned closes an assigned notification because the task was
	// delegated to someone else.
	ReasonReassigned = "reassigned"
)

// Relations name the links a notification carries.
const (
	// RelationTask links to the task's details.
	RelationTask = "task"
	// RelationContext links to where the task's work is done, expanded from its
	// type's [hmntsk.MetadataRoute].
	RelationContext = "context"
)

// Defaults a projector applies when no option replaces them.
const (
	// DefaultSinkName is the name the relay records the projector's acceptance
	// under.
	DefaultSinkName = "tasknotify"
	// DefaultPublishBatch is the most drafts one Publish call carries.
	DefaultPublishBatch = 500
	// DefaultTaskLinkTemplate links to the task in the REST contract served at
	// its default base path.
	DefaultTaskLinkTemplate = "/v1/tasks/{task.id}"
)

// defaultClosingStatuses is every final status.
var defaultClosingStatuses = []hmntsk.Status{
	hmntsk.StatusCompleted, hmntsk.StatusFailed, hmntsk.StatusError, hmntsk.StatusExited, hmntsk.StatusObsolete,
}

// Projector turns task events into notifications. It is a [relay.Sink]: add it
// to a relay with [relay.WithSinks], and every event the engine records is
// projected at least once, whatever crashes or retries in between.
//
// A Projector is safe for concurrent use.
type Projector struct {
	engine   *hmntsk.Service
	notifier *notify.Service
	name     string
	batch    int
	taskLink string
	closing  map[hmntsk.Status]struct{}
	rules    Rules
	titles   TitleFunc
	data     DataFunc
	links    LinksFunc
	onError  func(ctx context.Context, err error)

	// problems collects the configuration mistakes options report, so that New
	// refuses them all at once.
	problems []string
}

// Option configures a [Projector] at construction.
type Option func(*Projector)

// WithSinkName replaces [DefaultSinkName]. The relay records acceptance by sink
// name, so renaming a projector re-projects every event the relay still holds;
// idempotency makes that harmless, but not free. An empty name is a
// configuration error.
func WithSinkName(name string) Option {
	return func(p *Projector) {
		if name == "" {
			p.problem("WithSinkName was given an empty name; omit it to use " + DefaultSinkName)

			return
		}

		p.name = name
	}
}

// WithPublishBatch replaces [DefaultPublishBatch], the most drafts one Publish
// call carries when an event fans out to a large pool. A batch below one is a
// configuration error.
func WithPublishBatch(n int) Option {
	return func(p *Projector) {
		if n < 1 {
			p.problem("WithPublishBatch needs a batch of at least one")

			return
		}

		p.batch = n
	}
}

// WithTaskLinkTemplate replaces [DefaultTaskLinkTemplate], for a host whose task
// page lives elsewhere, such as its own web application. The template is
// expanded with [hmntsk.ExpandRoute]. An empty template is a configuration
// error; use [WithLinks] to leave the task link out deliberately.
func WithTaskLinkTemplate(template string) Option {
	return func(p *Projector) {
		if template == "" {
			p.problem("WithTaskLinkTemplate was given an empty template; use WithLinks to omit the task link")

			return
		}

		p.taskLink = template
	}
}

// WithClosingStatuses replaces the statuses whose events close every
// notification of a task. The default is every final status. The set must
// include completed and cancelled ([hmntsk.StatusExited]), and may contain only
// final statuses; anything else is a configuration error. A status listed twice
// counts once.
func WithClosingStatuses(statuses ...hmntsk.Status) Option {
	return func(p *Projector) {
		if len(statuses) == 0 {
			p.problem("WithClosingStatuses was given no statuses; omit it to close on every final status")

			return
		}

		closing := make(map[hmntsk.Status]struct{}, len(statuses))

		for _, status := range statuses {
			if !status.IsTerminal() {
				p.problem("WithClosingStatuses was given " + string(status) + ", which is not a final status")

				return
			}

			closing[status] = struct{}{}
		}

		for _, required := range []hmntsk.Status{hmntsk.StatusCompleted, hmntsk.StatusExited} {
			if _, ok := closing[required]; !ok {
				p.problem("WithClosingStatuses must include " + string(required))

				return
			}
		}

		p.closing = closing
	}
}

// WithRules replaces [DefaultRules], the rules that decide what each event
// opens and closes. A host extends the defaults by calling DefaultRules.Plan
// and appending steps. A nil Rules is a configuration error.
func WithRules(rules Rules) Option {
	return func(p *Projector) {
		if rules == nil {
			p.problem("WithRules was given no rules; omit it to use DefaultRules")

			return
		}

		p.rules = rules
	}
}

// WithTitles replaces the default English titles. A nil func is a
// configuration error.
func WithTitles(titles TitleFunc) Option {
	return func(p *Projector) {
		if titles == nil {
			p.problem("WithTitles was given no func; omit it to use the default titles")

			return
		}

		p.titles = titles
	}
}

// WithData replaces the default data payload. A nil func is a configuration
// error.
func WithData(data DataFunc) Option {
	return func(p *Projector) {
		if data == nil {
			p.problem("WithData was given no func; omit it to use the default data")

			return
		}

		p.data = data
	}
}

// WithLinks replaces how both links are built, wholesale: the task link from
// the template and the contextual link from the type's route. A nil func is a
// configuration error.
func WithLinks(links LinksFunc) Option {
	return func(p *Projector) {
		if links == nil {
			p.problem("WithLinks was given no func; omit it to use the default links")

			return
		}

		p.links = links
	}
}

// WithErrorHandler receives the failures a projection reports instead of
// retrying: those no further attempt can change, such as an invalid title from a
// host's [TitleFunc]. The event is still recorded as delivered, so that it is not
// dead-lettered for every other sink. The default handler does nothing, which
// is silent; a host should supply one that logs. A nil handler is a
// configuration error.
func WithErrorHandler(handler func(ctx context.Context, err error)) Option {
	return func(p *Projector) {
		if handler == nil {
			p.problem("WithErrorHandler was given no handler; omit it to discard reported failures")

			return
		}

		p.onError = handler
	}
}

// New builds a projector from an engine and a notification service.
//
// With no options it is named [DefaultSinkName], applies [DefaultRules],
// closes a task's notifications on every final status, publishes at most
// [DefaultPublishBatch] drafts per call, and links to [DefaultTaskLinkTemplate].
// A nil engine or notifier, and every invalid option, is an error matching
// [hmntsk.ErrConfiguration].
func New(engine *hmntsk.Service, notifier *notify.Service, opts ...Option) (*Projector, error) {
	p := &Projector{
		engine:   engine,
		notifier: notifier,
		name:     DefaultSinkName,
		batch:    DefaultPublishBatch,
		taskLink: DefaultTaskLinkTemplate,
		closing:  make(map[hmntsk.Status]struct{}, len(defaultClosingStatuses)),
		rules:    DefaultRules,
		onError:  func(context.Context, error) {},
	}

	for _, status := range defaultClosingStatuses {
		p.closing[status] = struct{}{}
	}

	if engine == nil {
		p.problem("an engine is required")
	}

	if notifier == nil {
		p.problem("a notification service is required")
	}

	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	if len(p.problems) > 0 {
		return nil, &hmntsk.ConfigurationError{Detail: "tasknotify: " + strings.Join(p.problems, "; ")}
	}

	return p, nil
}

// problem records a configuration mistake.
func (p *Projector) problem(detail string) { p.problems = append(p.problems, detail) }

// Name implements [relay.Sink]. It must stay stable across restarts: the relay
// records acceptance under it.
func (p *Projector) Name() string { return p.name }

// closes reports whether a status closes a task's notifications.
func (p *Projector) closes(status hmntsk.Status) bool {
	_, ok := p.closing[status]

	return ok
}
