package notify

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"time"
)

// Service is the only entry point a publisher, a handler or a pruner uses. It
// validates, stamps and stores notifications, and signals every change to the
// recipients it touches once the change is durable.
//
// A Service is safe for concurrent use.
type Service struct {
	store         Store
	clock         Clock
	ids           IDGenerator
	broadcaster   Broadcaster
	onSignalError func(ctx context.Context, err error)
}

// Option configures a [Service] at construction.
type Option func(*Service)

// WithClock replaces the default [SystemClock]. A nil clock is ignored.
func WithClock(clock Clock) Option {
	return func(s *Service) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// WithIDGenerator replaces the default [UUIDv7Generator]. A nil generator is
// ignored.
func WithIDGenerator(generator IDGenerator) Option {
	return func(s *Service) {
		if generator != nil {
			s.ids = generator
		}
	}
}

// WithBroadcaster replaces the default [InProcessBroadcaster], which reaches
// only the process it runs in. A deployment of more than one instance needs a
// broadcaster that crosses instances. A nil broadcaster is ignored.
func WithBroadcaster(broadcaster Broadcaster) Option {
	return func(s *Service) {
		if broadcaster != nil {
			s.broadcaster = broadcaster
		}
	}
}

// WithSignalErrorHandler receives errors from broadcasting a change. Such an
// error never fails the operation: the change is already stored, and a client
// re-reads the store when it reconnects. The default handler does nothing,
// which is safe but silent; a host should supply one that logs. A nil handler
// is ignored.
func WithSignalErrorHandler(handler func(ctx context.Context, err error)) Option {
	return func(s *Service) {
		if handler != nil {
			s.onSignalError = handler
		}
	}
}

// New builds a service over a store. With no options it reads the system clock,
// mints UUIDv7 identifiers and broadcasts in process. A nil store is a
// [ConfigurationError].
func New(store Store, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, &ConfigurationError{Detail: "a store is required; NewMemoryStore serves a single process"}
	}

	svc := &Service{
		store:         store,
		clock:         SystemClock{},
		ids:           NewUUIDv7Generator(),
		broadcaster:   NewInProcessBroadcaster(),
		onSignalError: func(context.Context, error) {},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}

	return svc, nil
}

// Broadcaster returns the broadcaster the service signals changes through, so
// that a [Hub] can listen on the same one.
func (s *Service) Broadcaster() Broadcaster { return s.broadcaster }

// PublishResult is what a publish did.
type PublishResult struct {
	// Created lists the notifications created.
	Created []Notification
	// Duplicates counts drafts whose source and recipient already had one.
	Duplicates int
	// Suppressed counts drafts below their subject's watermark.
	Suppressed int
	// Coalesced counts coalescing drafts an open notification absorbed.
	Coalesced int
}

// Publish creates notifications from drafts.
//
// Every draft is validated before anything is written; one invalid draft
// publishes nothing. Drafts are grouped by subject and each subject is written
// in its own transaction, in subject order, so that concurrent publishes lock
// subjects in the same order. When a subject fails, the subjects already
// written stay written, are reported and signalled, and the error is returned.
func (s *Service) Publish(ctx context.Context, drafts ...Draft) (PublishResult, error) {
	var issues []ValidationIssue

	for i, draft := range drafts {
		issues = append(issues, prefixed("/drafts/"+strconv.Itoa(i), draft.Validate())...)
	}

	if len(issues) > 0 {
		return PublishResult{}, &ValidationError{Subject: "publish", Issues: issues}
	}

	if len(drafts) == 0 {
		return PublishResult{}, nil
	}

	now := normalizeTime(s.clock.Now())
	bySubject := make(map[string][]Insertion)

	for _, draft := range drafts {
		id, err := s.ids.NewID()
		if err != nil {
			return PublishResult{}, err
		}

		bySubject[draft.Subject] = append(bySubject[draft.Subject], Insertion{
			// Cloned, so that a caller mutating its links or payload after
			// publishing cannot change what is stored.
			Notification: Notification{
				ID: id, Recipient: draft.Recipient, SourceID: draft.SourceID, Subject: draft.Subject,
				SubjectVersion: draft.SubjectVersion, Kind: draft.Kind, State: StateActive,
				Title: draft.Title, Links: draft.Links, Data: draft.Data, CreatedAt: now,
			}.Clone(),
			Coalesce: draft.Coalesce,
		})
	}

	var (
		inserted InsertResult
		failure  error
	)

	for _, subject := range slices.Sorted(maps.Keys(bySubject)) {
		result, err := s.store.Insert(ctx, subject, bySubject[subject])
		if err != nil {
			failure = err

			break
		}

		inserted.add(result)
	}

	s.signal(ctx, signalsFor(recipientsOf(inserted.Created), ChangeCreated, now))

	return PublishResult(inserted), failure
}

// Close closes a subject's notifications by kind and version, raises its
// watermark, and publishes the request's successor to each recipient it closed,
// in one store transaction. It signals a close to every recipient closed and a
// creation to every recipient given a successor.
func (s *Service) Close(ctx context.Context, req CloseRequest) (CloseResult, error) {
	if err := req.Validate(); err != nil {
		return CloseResult{}, err
	}

	at := normalizeTime(s.clock.Now())

	result, err := s.store.Close(ctx, req, at, s.ids)
	if err != nil {
		return CloseResult{}, err
	}

	signals := signalsFor(result.Recipients, ChangeClosed, at)
	signals = append(signals, signalsFor(recipientsOf(result.Successors), ChangeCreated, at)...)
	s.signal(ctx, signals)

	return result, nil
}

// Get reads one of a recipient's notifications. Another recipient's
// notification is [ErrNotFound], exactly like a missing one.
func (s *Service) Get(ctx context.Context, recipient, id string) (Notification, error) {
	issues := validateIdentifier("/recipient", recipient)
	if id == "" {
		issues = append(issues, ValidationIssue{Pointer: "/id", Detail: "is required"})
	}

	if err := invalid("request", issues); err != nil {
		return Notification{}, err
	}

	return s.store.Get(ctx, recipient, id)
}

// List returns a page of a recipient's notifications, newest first.
func (s *Service) List(ctx context.Context, q ListQuery) (Page, error) {
	if err := q.Validate(); err != nil {
		return Page{}, err
	}

	return s.store.List(ctx, q)
}

// CountActive counts a recipient's ACTIVE notifications.
func (s *Service) CountActive(ctx context.Context, recipient string) (int64, error) {
	if err := invalid("request", validateIdentifier("/recipient", recipient)); err != nil {
		return 0, err
	}

	return s.store.CountActive(ctx, recipient)
}

// MarkRead marks a recipient's notifications read. Any identifier that is not
// the recipient's is [ErrNotFound], and nothing is marked.
func (s *Service) MarkRead(ctx context.Context, recipient string, ids ...string) (MarkResult, error) {
	issues := validateIdentifier("/recipient", recipient)
	if len(ids) == 0 {
		issues = append(issues, ValidationIssue{Pointer: "/ids", Detail: "at least one identifier is required"})
	}

	if err := invalid("request", issues); err != nil {
		return MarkResult{}, err
	}

	at := normalizeTime(s.clock.Now())

	result, err := s.store.MarkRead(ctx, recipient, ids, at)
	if err != nil {
		return MarkResult{}, err
	}

	if result.Marked > 0 {
		s.signal(ctx, signalsFor([]string{recipient}, ChangeRead, at))
	}

	return result, nil
}

// MarkAllRead marks every ACTIVE notification of a recipient created at or
// before through as read. A zero through means now. Passing the instant a
// client loaded its list keeps anything that arrived afterwards unread.
func (s *Service) MarkAllRead(ctx context.Context, recipient string, through time.Time) (MarkResult, error) {
	if err := invalid("request", validateIdentifier("/recipient", recipient)); err != nil {
		return MarkResult{}, err
	}

	at := normalizeTime(s.clock.Now())
	if through.IsZero() {
		through = at
	}

	result, err := s.store.MarkAllRead(ctx, recipient, normalizeTime(through), at)
	if err != nil {
		return MarkResult{}, err
	}

	if result.Marked > 0 {
		s.signal(ctx, signalsFor([]string{recipient}, ChangeRead, at))
	}

	return result, nil
}

// signal broadcasts signals, if there are any, after the change they describe
// is stored.
//
// It detaches from the request's cancellation while keeping its values: a
// client hanging up in the instant after a commit must not drop the signal for
// every other connection of the recipient. A broadcast error reaches the signal
// error handler and nothing else.
func (s *Service) signal(ctx context.Context, signals []Signal) {
	if len(signals) == 0 {
		return
	}

	detached := context.WithoutCancel(ctx)

	if err := s.broadcaster.Broadcast(detached, signals); err != nil {
		s.onSignalError(detached, err)
	}
}

// signalsFor builds one signal per distinct recipient, in recipient order.
func signalsFor(recipients []string, change Change, at time.Time) []Signal {
	distinct := slices.Compact(slices.Sorted(slices.Values(recipients)))
	signals := make([]Signal, 0, len(distinct))

	for _, recipient := range distinct {
		signals = append(signals, Signal{Recipient: recipient, Change: change, At: at})
	}

	return signals
}

// recipientsOf lists the recipients of notifications.
func recipientsOf(notifications []Notification) []string {
	out := make([]string, 0, len(notifications))
	for _, n := range notifications {
		out = append(out, n.Recipient)
	}

	return out
}

// prefixed returns a validation error's issues with their pointers under a
// prefix, or nothing when err is nil.
func prefixed(prefix string, err error) []ValidationIssue {
	var validation *ValidationError
	if !errors.As(err, &validation) {
		return nil
	}

	out := make([]ValidationIssue, 0, len(validation.Issues))
	for _, issue := range validation.Issues {
		issue.Pointer = prefix + issue.Pointer
		out = append(out, issue)
	}

	return out
}
