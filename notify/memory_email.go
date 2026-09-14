package notify

import (
	"context"
	"slices"
	"time"
)

var _ EmailStore = (*MemoryStore)(nil)

// emailDelivery is one notification's email delivery state.
type emailDelivery struct {
	status        EmailStatus
	batchID       string
	owner         string
	leaseUntil    *time.Time
	attempts      int
	nextAttemptAt *time.Time
	reason        string
	sentAt        *time.Time
	updatedAt     time.Time
}

// ClaimEmails implements [EmailStore].
func (s *MemoryStore) ClaimEmails(_ context.Context, claim EmailClaim) ([]EmailCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := normalizeTime(claim.Now)
	until := normalizeTime(now.Add(claim.Lease))

	var due []Notification

	for id, n := range s.notifications {
		delivery, recorded := s.deliveries[id]
		if claimable(n, delivery, recorded, claim, now) {
			due = append(due, n)
		}
	}

	slices.SortFunc(due, oldestFirst)

	if claim.Limit > 0 && len(due) > claim.Limit {
		due = due[:claim.Limit]
	}

	candidates := make([]EmailCandidate, 0, len(due))

	for _, n := range due {
		delivery := s.deliveries[n.ID]
		if delivery.status != EmailStatusSending {
			delivery.status = EmailStatusClaimed
		}

		delivery.owner = claim.Owner
		delivery.leaseUntil = timePtr(until)
		delivery.updatedAt = now
		s.deliveries[n.ID] = delivery

		candidates = append(candidates, EmailCandidate{
			Notification: n.Clone(), Status: delivery.status, BatchID: delivery.batchID, Attempts: delivery.attempts,
		})
	}

	return candidates, nil
}

// claimable reports whether a claim may take a notification: an unrecorded or
// released one that is ACTIVE and inside the claim's window, a retry that is due
// and still qualifies, or a send in doubt whose lease lapsed, whatever became of
// the notification since.
func claimable(n Notification, d emailDelivery, recorded bool, claim EmailClaim, now time.Time) bool {
	leaseFree := d.leaseUntil == nil || !d.leaseUntil.After(now)

	qualifies := n.State == StateActive &&
		!n.CreatedAt.After(claim.CreatedUntil) && !n.CreatedAt.Before(claim.CreatedFrom)

	switch {
	case !recorded:
		return qualifies
	case d.status == EmailStatusSending:
		return leaseFree
	case d.status == EmailStatusClaimed, d.status == EmailStatusRetry:
		due := d.nextAttemptAt == nil || !d.nextAttemptAt.After(now)

		return leaseFree && due && qualifies
	default:
		return false
	}
}

// RecordEmails implements [EmailStore].
func (s *MemoryStore) RecordEmails(_ context.Context, record EmailRecord) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	at := normalizeTime(record.At)

	var changed int64

	for _, id := range slices.Compact(slices.Sorted(slices.Values(record.IDs))) {
		delivery, ok := s.deliveries[id]
		if !ok || delivery.owner != record.Owner {
			continue
		}

		s.deliveries[id] = applyEmailRecord(delivery, record, at)
		changed++
	}

	return changed, nil
}

// applyEmailRecord writes an outcome over a delivery held by the record's owner.
// SENDING keeps the lease; every other status releases it.
func applyEmailRecord(d emailDelivery, record EmailRecord, at time.Time) emailDelivery {
	d.status = record.Status
	d.updatedAt = at
	d.nextAttemptAt = nil

	if record.BatchID != "" {
		d.batchID = record.BatchID
	}

	if record.Reason != "" {
		d.reason = record.Reason
	}

	if record.Attempt {
		d.attempts++
	}

	switch record.Status {
	case EmailStatusSending:
		return d
	case EmailStatusRetry:
		d.nextAttemptAt = clonePtr(record.NextAttemptAt)
	case EmailStatusSent:
		d.sentAt = timePtr(at)
	case EmailStatusClaimed, EmailStatusSkipped, EmailStatusFailed, EmailStatusAbandoned:
	}

	d.owner = ""
	d.leaseUntil = nil

	return d
}

// PurgeEmailRecords implements [EmailStore].
func (s *MemoryStore) PurgeEmailRecords(_ context.Context, limit int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var orphans []string

	for id := range s.deliveries {
		if _, exists := s.notifications[id]; !exists {
			orphans = append(orphans, id)
		}
	}

	slices.Sort(orphans)

	if limit > 0 && len(orphans) > limit {
		orphans = orphans[:limit]
	}

	for _, id := range orphans {
		delete(s.deliveries, id)
	}

	return int64(len(orphans)), nil
}
