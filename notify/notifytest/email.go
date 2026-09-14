package notifytest

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// EmailStore is a store that also records email deliveries.
type EmailStore interface {
	notify.Store
	notify.EmailStore
}

// EmailFactory returns a fresh, empty store that records email deliveries. It is
// called once per case and registers any cleanup the store needs on t.
type EmailFactory func(t *testing.T) EmailStore

// The claim window every email case uses: a five-minute grace delay, a one-day
// lag and a five-minute lease, measured from emailNow.
var (
	emailNow   = base.Add(48 * time.Hour)
	emailLease = 5 * time.Minute
)

// minutes returns a duration of n minutes.
func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }

// RunEmail executes every email delivery conformance case against stores the
// factory builds.
func RunEmail(t *testing.T, factory EmailFactory) {
	t.Helper()

	t.Run("eligibility", func(t *testing.T) { runEmailEligibility(t, factory) })
	t.Run("leases", func(t *testing.T) { runEmailLeases(t, factory) })
	t.Run("outcomes", func(t *testing.T) { runEmailOutcomes(t, factory) })
	t.Run("purge", func(t *testing.T) { runEmailPurge(t, factory) })
	t.Run("concurrency", func(t *testing.T) { runEmailConcurrency(t, factory) })
}

// emailEnv is one email case's store and the helpers that drive it.
type emailEnv struct {
	*env
	email EmailStore
}

// newEmailEnv builds an email case's store.
func newEmailEnv(t *testing.T, factory EmailFactory) *emailEnv {
	t.Helper()

	store := factory(t)

	return &emailEnv{env: &env{t: t, store: store, ids: notify.NewUUIDv7Generator()}, email: store}
}

// published inserts one ACTIVE notification for a recipient, created age before
// emailNow, on a subject of its own.
func (e *emailEnv) published(recipient string, age time.Duration) notify.Notification {
	e.t.Helper()

	id, err := e.ids.NewID()
	require.NoError(e.t, err)

	n := e.note(recipient, "event-"+id, "task-"+id, "offer", 1, emailNow.Add(-age))
	result := e.insert(false, n)
	require.Len(e.t, result.Created, 1)

	return result.Created[0]
}

// claimAt claims for an owner at an instant, with the standard window measured
// from that instant.
func (e *emailEnv) claimAt(owner string, now time.Time, limit int) []notify.EmailCandidate {
	e.t.Helper()

	candidates, err := e.email.ClaimEmails(e.t.Context(), notify.EmailClaim{
		Now: now, Owner: owner, Lease: emailLease,
		CreatedUntil: now.Add(-minutes(5)), CreatedFrom: now.Add(-24 * time.Hour), Limit: limit,
	})
	require.NoError(e.t, err)

	return candidates
}

// claim claims for an owner at emailNow.
func (e *emailEnv) claim(owner string) []notify.EmailCandidate {
	e.t.Helper()

	return e.claimAt(owner, emailNow, 100)
}

// record writes an outcome and returns how many deliveries it changed.
func (e *emailEnv) record(record notify.EmailRecord) int64 {
	e.t.Helper()

	if record.At.IsZero() {
		record.At = emailNow
	}

	changed, err := e.email.RecordEmails(e.t.Context(), record)
	require.NoError(e.t, err)

	return changed
}

// candidateIDs lists candidates' notification identifiers.
func candidateIDs(candidates []notify.EmailCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Notification.ID)
	}

	return out
}

func runEmailEligibility(t *testing.T, factory EmailFactory) {
	parallel(t, "an active notification past the grace delay is claimed once, as CLAIMED", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		claimed := e.claim("owner-a")
		require.Len(t, claimed, 1)

		got := claimed[0]
		assert.Equal(t, n.ID, got.Notification.ID)
		assert.Equal(t, "alice", got.Notification.Recipient)
		assert.Equal(t, notify.StateActive, got.Notification.State)
		assert.True(t, n.CreatedAt.Equal(got.Notification.CreatedAt))
		assert.Equal(t, notify.EmailStatusClaimed, got.Status)
		assert.Empty(t, got.BatchID)
		assert.Zero(t, got.Attempts)
	})

	parallel(t, "a notification inside the grace delay is not claimed", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		e.published("alice", minutes(1))

		assert.Empty(t, e.claim("owner-a"))
	})

	parallel(t, "a notification older than the lag is not claimed", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		e.published("alice", 25*time.Hour)

		assert.Empty(t, e.claim("owner-a"))
	})

	parallel(t, "a read notification is not claimed", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))
		e.markRead("alice", emailNow.Add(-minutes(2)), n.ID)

		assert.Empty(t, e.claim("owner-a"))
	})

	parallel(t, "a closed notification is not claimed", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))
		e.close(notify.CloseRequest{Subject: n.Subject, Version: 1, Reason: "taken"}, emailNow.Add(-minutes(2)))

		assert.Empty(t, e.claim("owner-a"))
	})

	parallel(t, "a claim returns at most its limit, oldest first", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		oldest := e.published("alice", minutes(30))
		middle := e.published("bob", minutes(20))
		e.published("carol", minutes(10))

		claimed := e.claimAt("owner-a", emailNow, 2)
		assert.Equal(t, []string{oldest.ID, middle.ID}, candidateIDs(claimed))
	})
}

func runEmailLeases(t *testing.T, factory EmailFactory) {
	parallel(t, "a held lease keeps a notification from every other claim", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)
		assert.Empty(t, e.claim("owner-b"), "another owner")
		assert.Empty(t, e.claimAt("owner-a", emailNow.Add(time.Minute), 100), "the same owner, later in the lease")
	})

	parallel(t, "an expired lease is taken over, and the old owner can no longer record", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)

		later := emailNow.Add(emailLease + time.Second)
		taken := e.claimAt("owner-b", later, 100)
		require.Len(t, taken, 1)
		assert.Equal(t, notify.EmailStatusClaimed, taken[0].Status)

		assert.Zero(t, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusSent, At: later,
		}), "a lost lease records nothing")
		assert.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-b", IDs: []string{n.ID}, Status: notify.EmailStatusSent, At: later,
		}))
	})

	parallel(t, "a released claim is claimable at once by another owner", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)
		assert.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusClaimed,
		}))

		taken := e.claimAt("owner-b", emailNow.Add(time.Second), 100)
		assert.Equal(t, []string{n.ID}, candidateIDs(taken))
	})

	parallel(t, "a sending delivery is not taken over while its lease holds", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)
		require.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusSending, BatchID: "batch-1", Attempt: true,
		}))

		assert.Empty(t, e.claimAt("owner-b", emailNow.Add(time.Minute), 100))
	})

	parallel(t, "an expired sending delivery is taken over in doubt, with its batch and attempts", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)
		require.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusSending, BatchID: "batch-1", Attempt: true,
		}))

		taken := e.claimAt("owner-b", emailNow.Add(emailLease+time.Second), 100)
		require.Len(t, taken, 1)
		assert.Equal(t, notify.EmailStatusSending, taken[0].Status)
		assert.Equal(t, "batch-1", taken[0].BatchID)
		assert.Equal(t, 1, taken[0].Attempts)
	})

	parallel(t, "an expired sending delivery is taken over even after its notification stopped being active", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)
		require.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusSending, BatchID: "batch-1", Attempt: true,
		}))
		e.markRead("alice", emailNow.Add(time.Minute), n.ID)

		taken := e.claimAt("owner-b", emailNow.Add(emailLease+time.Second), 100)
		require.Len(t, taken, 1, "an in-doubt send is resolved whatever became of the notification")
		assert.Equal(t, notify.StateRead, taken[0].Notification.State)
	})
}

func runEmailOutcomes(t *testing.T, factory EmailFactory) {
	parallel(t, "a retry is not claimed before it is due, and is claimed with its batch and attempts after", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)
		require.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusSending, BatchID: "batch-1", Attempt: true,
		}))

		due := emailNow.Add(minutes(2))
		require.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusRetry, Reason: "connection refused",
			NextAttemptAt: &due,
		}))

		assert.Empty(t, e.claimAt("owner-b", emailNow.Add(time.Minute), 100), "not yet due")

		retried := e.claimAt("owner-b", due, 100)
		require.Len(t, retried, 1)
		assert.Equal(t, notify.EmailStatusClaimed, retried[0].Status)
		assert.Equal(t, "batch-1", retried[0].BatchID)
		assert.Equal(t, 1, retried[0].Attempts)
	})

	parallel(t, "an attempt recorded without a send still counts", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)
		require.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{n.ID}, Status: notify.EmailStatusRetry, Attempt: true,
		}))

		retried := e.claimAt("owner-b", emailNow.Add(time.Second), 100)
		require.Len(t, retried, 1)
		assert.Empty(t, retried[0].BatchID)
		assert.Equal(t, 1, retried[0].Attempts)
	})

	for _, status := range []notify.EmailStatus{
		notify.EmailStatusSent, notify.EmailStatusSkipped, notify.EmailStatusFailed, notify.EmailStatusAbandoned,
	} {
		parallel(t, "a "+string(status)+" delivery is never claimed again", func(t *testing.T) {
			e := newEmailEnv(t, factory)
			n := e.published("alice", minutes(10))

			require.Len(t, e.claim("owner-a"), 1)
			require.EqualValues(t, 1, e.record(notify.EmailRecord{
				Owner: "owner-a", IDs: []string{n.ID}, Status: status, BatchID: "batch-1",
			}))

			assert.Empty(t, e.claimAt("owner-b", emailNow.Add(time.Second), 100), "at once")
			assert.Empty(t, e.claimAt("owner-b", emailNow.Add(time.Hour), 100), "after any lease")
		})
	}

	parallel(t, "another owner's record changes nothing", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		n := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 1)

		assert.Zero(t, e.record(notify.EmailRecord{
			Owner: "owner-b", IDs: []string{n.ID}, Status: notify.EmailStatusSent,
		}))
		assert.Empty(t, e.claim("owner-b"), "owner-a still holds it")
	})

	parallel(t, "a record changes only the named notifications, and counts them", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		one := e.published("alice", minutes(12))
		two := e.published("alice", minutes(11))
		three := e.published("alice", minutes(10))

		require.Len(t, e.claim("owner-a"), 3)

		assert.EqualValues(t, 2, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{one.ID, two.ID}, Status: notify.EmailStatusSent,
		}))
		assert.EqualValues(t, 1, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{three.ID}, Status: notify.EmailStatusClaimed,
		}))

		assert.Equal(t, []string{three.ID}, candidateIDs(e.claimAt("owner-b", emailNow.Add(time.Second), 100)))
	})
}

func runEmailPurge(t *testing.T, factory EmailFactory) {
	parallel(t, "purging removes delivery records of deleted notifications, and only those", func(t *testing.T) {
		e := newEmailEnv(t, factory)
		kept := e.published("alice", minutes(11))
		deleted := e.published("bob", minutes(10))

		require.Len(t, e.claim("owner-a"), 2)
		require.EqualValues(t, 2, e.record(notify.EmailRecord{
			Owner: "owner-a", IDs: []string{kept.ID, deleted.ID}, Status: notify.EmailStatusSent,
		}))

		e.markRead("bob", emailNow, deleted.ID)
		pruned := e.prune(notify.PruneRequest{Now: emailNow.Add(days(2)), MaxAge: days(1)})
		require.EqualValues(t, 1, pruned.DeletedForAge)

		purged, err := e.email.PurgeEmailRecords(t.Context(), 100)
		require.NoError(t, err)
		assert.EqualValues(t, 1, purged)

		again, err := e.email.PurgeEmailRecords(t.Context(), 100)
		require.NoError(t, err)
		assert.Zero(t, again, "nothing is left to purge")

		assert.Empty(t, e.claimAt("owner-b", emailNow.Add(time.Hour), 100), "the kept delivery stays sent")
	})

	parallel(t, "purging deletes at most its limit", func(t *testing.T) {
		e := newEmailEnv(t, factory)

		var ids []string

		for range 3 {
			n := e.published("alice", minutes(10))
			ids = append(ids, n.ID)
		}

		require.Len(t, e.claim("owner-a"), 3)
		require.EqualValues(t, 3, e.record(notify.EmailRecord{Owner: "owner-a", IDs: ids, Status: notify.EmailStatusSent}))

		e.markRead("alice", emailNow, ids...)
		e.prune(notify.PruneRequest{Now: emailNow.Add(days(2)), MaxAge: days(1)})

		first, err := e.email.PurgeEmailRecords(t.Context(), 2)
		require.NoError(t, err)
		assert.EqualValues(t, 2, first)

		second, err := e.email.PurgeEmailRecords(t.Context(), 2)
		require.NoError(t, err)
		assert.EqualValues(t, 1, second)
	})
}

func runEmailConcurrency(t *testing.T, factory EmailFactory) {
	parallel(t, "two owners claiming concurrently never both receive a notification", func(t *testing.T) {
		e := newEmailEnv(t, factory)

		for i := range Iterations {
			var published []string

			for range 3 {
				published = append(published, e.published(fmt.Sprintf("user-%d", i), minutes(10)).ID)
			}

			now := emailNow.Add(time.Duration(i) * time.Second)

			var (
				wg         sync.WaitGroup
				mu         sync.Mutex
				claims     = map[string][]string{}
				errA, errB error
			)

			claimAs := func(owner string, errp *error) {
				candidates, err := e.email.ClaimEmails(t.Context(), notify.EmailClaim{
					Now: now, Owner: owner, Lease: emailLease,
					CreatedUntil: now.Add(-minutes(5)), CreatedFrom: now.Add(-24 * time.Hour), Limit: 3,
				})

				mu.Lock()
				defer mu.Unlock()

				*errp = err
				claims[owner] = candidateIDs(candidates)
			}

			wg.Go(func() { claimAs(fmt.Sprintf("a-%d", i), &errA) })
			wg.Go(func() { claimAs(fmt.Sprintf("b-%d", i), &errB) })
			wg.Wait()

			require.NoError(t, errA)
			require.NoError(t, errB)

			a, b := claims[fmt.Sprintf("a-%d", i)], claims[fmt.Sprintf("b-%d", i)]

			for _, id := range a {
				require.NotContainsf(t, b, id, "iteration %d: both owners claimed %s", i, id)
			}

			union := slices.Concat(a, b)
			slices.Sort(union)
			slices.Sort(published)
			require.Equalf(t, published, union, "iteration %d: every notification is claimed by exactly one owner", i)

			for _, owner := range []string{fmt.Sprintf("a-%d", i), fmt.Sprintf("b-%d", i)} {
				if ids := claims[owner]; len(ids) > 0 {
					e.record(notify.EmailRecord{Owner: owner, IDs: ids, Status: notify.EmailStatusSent, At: now})
				}
			}
		}
	})
}
