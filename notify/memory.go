package notify

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// allKinds is the watermark key for a close of every kind.
const allKinds = "*"

// MemoryStore is the default [Store]: notifications held in process memory.
//
// It suits tests and a single-process host that can afford to lose its
// notifications on restart. It keeps every guarantee of the Store contract, and
// passes the same conformance suite as the SQL store. A MemoryStore is safe for
// concurrent use; every method runs under one lock, which is the whole of its
// serialisation.
type MemoryStore struct {
	mu            sync.Mutex
	notifications map[string]Notification
	watermarks    map[watermarkKey]watermark
	// sources indexes notifications by source and recipient, for idempotency.
	sources map[sourceKey]string
	// subjects indexes notification identifiers by subject, so that closing,
	// coalescing and expiring watermarks touch one subject's notifications
	// rather than every notification.
	subjects map[string]map[string]struct{}
	// deliveries holds email delivery state by notification identifier.
	deliveries map[string]emailDelivery
}

// watermarkKey identifies a subject's close record for one kind, or for every
// kind.
type watermarkKey struct {
	subject string
	kind    string
}

// sourceKey identifies a notification by the source and recipient that make
// publishing idempotent.
type sourceKey struct {
	source    string
	recipient string
}

// watermark is the highest version a subject was closed at, and when it last
// changed.
type watermark struct {
	version   int64
	updatedAt time.Time
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		notifications: make(map[string]Notification),
		watermarks:    make(map[watermarkKey]watermark),
		sources:       make(map[sourceKey]string),
		subjects:      make(map[string]map[string]struct{}),
		deliveries:    make(map[string]emailDelivery),
	}
}

// Insert implements [Store].
func (s *MemoryStore) Insert(_ context.Context, subject string, insertions []Insertion) (InsertResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.insert(subject, insertions), nil
}

// insert applies suppression, coalescing and idempotency, in that order, to
// notifications on one subject. The caller holds the lock.
func (s *MemoryStore) insert(subject string, insertions []Insertion) InsertResult {
	var result InsertResult

	for _, insertion := range insertions {
		n := insertion.Notification
		n.Subject = subject

		if n.SubjectVersion < s.floor(subject, n.Kind) {
			result.Suppressed++

			continue
		}

		if insertion.Coalesce && s.hasOpen(n.Recipient, subject, n.Kind) {
			result.Coalesced++

			continue
		}

		if _, duplicate := s.sources[sourceKey{source: n.SourceID, recipient: n.Recipient}]; duplicate {
			result.Duplicates++

			continue
		}

		s.put(n.Clone())
		result.Created = append(result.Created, n.Clone())

		s.ensureWatermark(watermarkKey{subject: subject, kind: allKinds}, n.CreatedAt)
	}

	return result
}

// put stores a notification and indexes it. The caller holds the lock.
func (s *MemoryStore) put(n Notification) {
	s.notifications[n.ID] = n
	s.sources[sourceKey{source: n.SourceID, recipient: n.Recipient}] = n.ID

	ids := s.subjects[n.Subject]
	if ids == nil {
		ids = make(map[string]struct{})
		s.subjects[n.Subject] = ids
	}

	ids[n.ID] = struct{}{}
}

// remove deletes a notification and its index entries. The caller holds the
// lock.
func (s *MemoryStore) remove(n Notification) {
	delete(s.notifications, n.ID)
	delete(s.sources, sourceKey{source: n.SourceID, recipient: n.Recipient})

	ids := s.subjects[n.Subject]
	delete(ids, n.ID)

	if len(ids) == 0 {
		delete(s.subjects, n.Subject)
	}
}

// floor is the version below which a notification of a kind on a subject is
// suppressed. The caller holds the lock.
func (s *MemoryStore) floor(subject, kind string) int64 {
	floor := int64(-1)

	for _, key := range []watermarkKey{{subject, kind}, {subject, allKinds}} {
		if mark, ok := s.watermarks[key]; ok && mark.version > floor {
			floor = mark.version
		}
	}

	return floor
}

// hasOpen reports whether a recipient has an ACTIVE or READ notification of a
// kind on a subject. The caller holds the lock.
func (s *MemoryStore) hasOpen(recipient, subject, kind string) bool {
	for id := range s.subjects[subject] {
		if n := s.notifications[id]; n.Recipient == recipient && n.Kind == kind && n.State != StateClosed {
			return true
		}
	}

	return false
}

// ensureWatermark creates a subject's close record at version -1 when it has
// none, leaving an existing one unchanged. The caller holds the lock.
func (s *MemoryStore) ensureWatermark(key watermarkKey, at time.Time) {
	if _, ok := s.watermarks[key]; !ok {
		s.watermarks[key] = watermark{version: -1, updatedAt: at}
	}
}

// Close implements [Store].
func (s *MemoryStore) Close(_ context.Context, req CloseRequest, at time.Time, ids IDGenerator) (CloseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	at = normalizeTime(at)

	s.ensureWatermark(watermarkKey{subject: req.Subject, kind: allKinds}, at)

	keys := []watermarkKey{{subject: req.Subject, kind: allKinds}}
	if len(req.Kinds) > 0 {
		keys = keys[:0]
		for _, kind := range req.Kinds {
			keys = append(keys, watermarkKey{subject: req.Subject, kind: kind})
		}
	}

	for _, key := range keys {
		mark, ok := s.watermarks[key]
		if !ok || mark.version < req.Version {
			mark.version = req.Version
		}

		mark.updatedAt = at
		s.watermarks[key] = mark
	}

	var result CloseResult

	recipients := make(map[string]struct{})

	for id := range s.subjects[req.Subject] {
		n := s.notifications[id]

		switch {
		case n.State == StateClosed || n.SubjectVersion > req.Version:
			continue
		case len(req.Kinds) > 0 && !slices.Contains(req.Kinds, n.Kind):
			continue
		case req.Except != "" && n.Recipient == req.Except:
			continue
		}

		n.State = StateClosed
		n.ClosedReason = req.Reason
		n.ClosedAt = timePtr(at)

		if n.InactiveAt == nil {
			n.InactiveAt = timePtr(at)
		}

		s.notifications[id] = n
		result.Closed++
		recipients[n.Recipient] = struct{}{}
	}

	result.Recipients = slices.Sorted(maps.Keys(recipients))

	successors, err := req.SuccessorInsertions(result.Recipients, at, ids)
	if err != nil {
		return CloseResult{}, err
	}

	if len(successors) > 0 {
		inserted := s.insert(req.Subject, successors)
		result.Successors = inserted.Created
		result.SuccessorsSuppressed = inserted.Suppressed
	}

	return result, nil
}

// Get implements [Store].
func (s *MemoryStore) Get(_ context.Context, recipient, id string) (Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n, ok := s.notifications[id]
	if !ok || n.Recipient != recipient {
		return Notification{}, ErrNotFound
	}

	return n.Clone(), nil
}

// List implements [Store].
func (s *MemoryStore) List(_ context.Context, q ListQuery) (Page, error) {
	position, continued, err := DecodeCursor(q)
	if err != nil {
		return Page{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var matched []Notification

	for _, n := range s.notifications {
		if !matches(n, q) {
			continue
		}

		if continued && !before(n, position) {
			continue
		}

		matched = append(matched, n)
	}

	slices.SortFunc(matched, newestFirst)

	limit := q.EffectiveLimit()
	page := Page{}

	if len(matched) > limit {
		last := matched[limit-1]
		page.NextCursor = EncodeCursor(q, CursorPosition{CreatedAt: last.CreatedAt, ID: last.ID})
		matched = matched[:limit]
	}

	for _, n := range matched {
		page.Notifications = append(page.Notifications, n.Clone())
	}

	return page, nil
}

// matches reports whether a notification is the query's recipient's and passes
// its filters.
func matches(n Notification, q ListQuery) bool {
	switch {
	case n.Recipient != q.Recipient:
		return false
	case len(q.States) > 0 && !slices.Contains(q.States, n.State):
		return false
	case len(q.Kinds) > 0 && !slices.Contains(q.Kinds, n.Kind):
		return false
	case q.Subject != "" && n.Subject != q.Subject:
		return false
	default:
		return true
	}
}

// before reports whether a notification comes after a cursor position in a
// newest-first listing.
func before(n Notification, position CursorPosition) bool {
	if n.CreatedAt.Equal(position.CreatedAt) {
		return n.ID < position.ID
	}

	return n.CreatedAt.Before(position.CreatedAt)
}

// newestFirst orders notifications newest first, then by identifier, highest
// first.
func newestFirst(a, b Notification) int {
	if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
		return c
	}

	return strings.Compare(b.ID, a.ID)
}

// CountActive implements [Store].
func (s *MemoryStore) CountActive(_ context.Context, recipient string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64

	for _, n := range s.notifications {
		if n.Recipient == recipient && n.State == StateActive {
			count++
		}
	}

	return count, nil
}

// MarkRead implements [Store].
func (s *MemoryStore) MarkRead(_ context.Context, recipient string, ids []string, at time.Time) (MarkResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		if n, ok := s.notifications[id]; !ok || n.Recipient != recipient {
			return MarkResult{}, ErrNotFound
		}
	}

	at = normalizeTime(at)

	var result MarkResult

	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		n := s.notifications[id]

		switch n.State {
		case StateActive:
			n.State = StateRead
			n.ReadAt = timePtr(at)
			n.InactiveAt = timePtr(at)
			result.Marked++
		case StateClosed:
			if n.ReadAt == nil {
				n.ReadAt = timePtr(at)
			}
		case StateRead:
		}

		s.notifications[id] = n
	}

	return result, nil
}

// MarkAllRead implements [Store].
func (s *MemoryStore) MarkAllRead(_ context.Context, recipient string, through, at time.Time) (MarkResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	at = normalizeTime(at)

	var result MarkResult

	for id, n := range s.notifications {
		if n.Recipient != recipient || n.State != StateActive || n.CreatedAt.After(through) {
			continue
		}

		n.State = StateRead
		n.ReadAt = timePtr(at)
		n.InactiveAt = timePtr(at)
		s.notifications[id] = n
		result.Marked++
	}

	return result, nil
}

// Prune implements [Store].
func (s *MemoryStore) Prune(_ context.Context, req PruneRequest) (PruneResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result PruneResult

	if req.MaxAge > 0 {
		cutoff := req.Now.Add(-req.MaxAge)

		for _, n := range s.notifications {
			if n.State != StateActive && n.InactiveAt != nil && n.InactiveAt.Before(cutoff) {
				s.remove(n)
				result.DeletedForAge++
			}
		}
	}

	if req.MaxPerRecipient > 0 {
		s.pruneCount(req, &result)
	}

	if req.WatermarkRetention > 0 {
		s.pruneWatermarks(req.Now.Add(-req.WatermarkRetention), &result)
	}

	return result, nil
}

// pruneCount brings each recipient within the count bound, inactive
// notifications first. The caller holds the lock.
func (s *MemoryStore) pruneCount(req PruneRequest, result *PruneResult) {
	byRecipient := make(map[string][]Notification)
	for _, n := range s.notifications {
		byRecipient[n.Recipient] = append(byRecipient[n.Recipient], n)
	}

	for _, recipient := range slices.Sorted(maps.Keys(byRecipient)) {
		held := byRecipient[recipient]
		excess := len(held) - req.MaxPerRecipient

		if excess <= 0 {
			continue
		}

		slices.SortFunc(held, oldestFirst)

		excess -= s.evict(held, excess, false, &result.DeletedForCount)

		if req.Strategy != EvictOldestActive || excess == 0 {
			continue
		}

		s.evict(held, excess, true, &result.EvictedActive)
		result.Recipients = append(result.Recipients, recipient)
	}
}

// evict removes up to n of held, oldest first, that are ACTIVE when active is
// true and inactive otherwise, adding each to count and returning how many it
// removed. The caller holds the lock.
func (s *MemoryStore) evict(held []Notification, n int, active bool, count *int64) int {
	removed := 0

	for _, candidate := range held {
		if removed == n {
			break
		}

		if (candidate.State == StateActive) == active {
			s.remove(candidate)
			*count++
			removed++
		}
	}

	return removed
}

// oldestFirst orders notifications oldest first, then by identifier.
func oldestFirst(a, b Notification) int { return -newestFirst(a, b) }

// pruneWatermarks deletes close records last changed before cutoff whose
// subject has no notifications left. The caller holds the lock.
func (s *MemoryStore) pruneWatermarks(cutoff time.Time, result *PruneResult) {
	for key, mark := range s.watermarks {
		if _, occupied := s.subjects[key.subject]; occupied || !mark.updatedAt.Before(cutoff) {
			continue
		}

		delete(s.watermarks, key)
		result.WatermarksDeleted++
	}
}
