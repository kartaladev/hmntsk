package notifytest

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// Factory returns a fresh, empty store. It is called once per case, so that
// cases run in parallel without seeing one another's notifications, and it
// registers any cleanup the store needs on t.
type Factory func(t *testing.T) notify.Store

// Iterations is how many times each concurrency case repeats. A race that
// loses one time in fifty is found in almost every run.
const Iterations = 200

// Run executes every conformance case against stores the factory builds.
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("inbox", func(t *testing.T) { runInbox(t, factory) })
	t.Run("watermark", func(t *testing.T) { runWatermark(t, factory) })
	t.Run("successors", func(t *testing.T) { runSuccessors(t, factory) })
	t.Run("coalescing", func(t *testing.T) { runCoalescing(t, factory) })
	t.Run("concurrency", func(t *testing.T) { runConcurrency(t, factory) })
	t.Run("retention", func(t *testing.T) { runRetention(t, factory) })
}

// base is the instant cases measure from. Every time a case writes is base
// plus a whole number of seconds, so ordering never depends on the wall clock.
var base = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// at returns base plus n seconds.
func at(n int) time.Time { return base.Add(time.Duration(n) * time.Second) }

// days returns a duration of n days.
func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// env is one case's store and the helpers that drive it.
type env struct {
	t     *testing.T
	store notify.Store
	ids   *notify.UUIDv7Generator
}

// newEnv builds a case's store.
func newEnv(t *testing.T, factory Factory) *env {
	t.Helper()

	return &env{t: t, store: factory(t), ids: notify.NewUUIDv7Generator()}
}

// note builds a stamped ACTIVE notification.
func (e *env) note(recipient, source, subject, kind string, version int64, created time.Time) notify.Notification {
	e.t.Helper()

	id, err := e.ids.NewID()
	require.NoError(e.t, err)

	return notify.Notification{
		ID: id, Recipient: recipient, SourceID: source, Subject: subject, SubjectVersion: version,
		Kind: kind, State: notify.StateActive, CreatedAt: created,
	}
}

// insert inserts notifications for their shared subject and fails the case on
// an error.
func (e *env) insert(coalesce bool, notifications ...notify.Notification) notify.InsertResult {
	e.t.Helper()

	result, err := e.tryInsert(coalesce, notifications...)
	require.NoError(e.t, err)

	return result
}

// tryInsert inserts notifications for their shared subject.
func (e *env) tryInsert(coalesce bool, notifications ...notify.Notification) (notify.InsertResult, error) {
	insertions := make([]notify.Insertion, 0, len(notifications))
	for _, n := range notifications {
		insertions = append(insertions, notify.Insertion{Notification: n, Coalesce: coalesce})
	}

	return e.store.Insert(e.t.Context(), notifications[0].Subject, insertions)
}

// close closes and fails the case on an error.
func (e *env) close(req notify.CloseRequest, when time.Time) notify.CloseResult {
	e.t.Helper()

	result, err := e.store.Close(e.t.Context(), req, when, e.ids)
	require.NoError(e.t, err)

	return result
}

// get reads a notification and fails the case on an error.
func (e *env) get(recipient, id string) notify.Notification {
	e.t.Helper()

	n, err := e.store.Get(e.t.Context(), recipient, id)
	require.NoError(e.t, err)

	return n
}

// markRead marks notifications read and fails the case on an error.
func (e *env) markRead(recipient string, when time.Time, ids ...string) notify.MarkResult {
	e.t.Helper()

	result, err := e.store.MarkRead(e.t.Context(), recipient, ids, when)
	require.NoError(e.t, err)

	return result
}

// list lists and fails the case on an error.
func (e *env) list(q notify.ListQuery) notify.Page {
	e.t.Helper()

	page, err := e.store.List(e.t.Context(), q)
	require.NoError(e.t, err)

	return page
}

// all lists every notification of a recipient, across pages.
func (e *env) all(recipient string) []notify.Notification {
	e.t.Helper()

	var (
		out []notify.Notification
		q   = notify.ListQuery{Recipient: recipient, Limit: notify.MaxListLimit}
	)

	for {
		page := e.list(q)
		out = append(out, page.Notifications...)

		if page.NextCursor == "" {
			return out
		}

		q.Cursor = page.NextCursor
	}
}

// count counts a recipient's ACTIVE notifications and fails the case on an
// error.
func (e *env) count(recipient string) int64 {
	e.t.Helper()

	n, err := e.store.CountActive(e.t.Context(), recipient)
	require.NoError(e.t, err)

	return n
}

// prune prunes and fails the case on an error.
func (e *env) prune(req notify.PruneRequest) notify.PruneResult {
	e.t.Helper()

	if req.Batch == 0 {
		req.Batch = 1000
	}

	if req.Strategy == "" {
		req.Strategy = notify.EvictOldestActive
	}

	result, err := e.store.Prune(e.t.Context(), req)
	require.NoError(e.t, err)

	return result
}

// ids returns the identifiers of notifications, in order.
func idsOf(notifications []notify.Notification) []string {
	out := make([]string, 0, len(notifications))
	for _, n := range notifications {
		out = append(out, n.ID)
	}

	return out
}

// sameInstant asserts that an optional instant equals an expected one.
func sameInstant(t *testing.T, want time.Time, got *time.Time, what string) {
	t.Helper()

	if assert.NotNilf(t, got, "%s is set", what) {
		assert.Truef(t, want.Equal(*got), "%s is %s, got %s", what, want, *got)
	}
}

// parallel runs a named case in parallel.
func parallel(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Parallel()
		fn(t)
	})
}

func runInbox(t *testing.T, factory Factory) {
	parallel(t, "a new notification is active with no read or closed time", func(t *testing.T) {
		e := newEnv(t, factory)
		n := e.note("alice", "event-1", "task-1", "offer", 1, at(0))

		result := e.insert(false, n)
		require.Len(t, result.Created, 1)
		assert.Equal(t, n.ID, result.Created[0].ID)

		got := e.get("alice", n.ID)
		assert.Equal(t, notify.StateActive, got.State)
		assert.Nil(t, got.ReadAt)
		assert.Nil(t, got.ClosedAt)
		assert.Nil(t, got.InactiveAt)
		assert.Empty(t, got.ClosedReason)
	})

	parallel(t, "opaque content is returned unchanged", func(t *testing.T) {
		e := newEnv(t, factory)
		n := e.note("alice", "event-1", "task-1", "offer", 7, at(0).Add(123456*time.Microsecond))
		n.Title = "Approve invoice INV-42 — ünïcode"
		n.Links = map[string]string{"task": "/v1/tasks/task-1", "context": "/invoices/INV-42?task=task-1"}
		n.Data = json.RawMessage(`{"z":1,"a":[2.50,1e3],"nested":{"b":true}}`)

		e.insert(false, n)

		got := e.get("alice", n.ID)
		assert.Equal(t, n.Recipient, got.Recipient)
		assert.Equal(t, n.SourceID, got.SourceID)
		assert.Equal(t, n.Subject, got.Subject)
		assert.Equal(t, n.SubjectVersion, got.SubjectVersion)
		assert.Equal(t, n.Kind, got.Kind)
		assert.Equal(t, n.Title, got.Title)
		assert.Equal(t, n.Links, got.Links)
		assert.Equal(t, string(n.Data), string(got.Data), "data byte for byte")
		assert.True(t, n.CreatedAt.Equal(got.CreatedAt), "created at %s, got %s", n.CreatedAt, got.CreatedAt)
	})

	parallel(t, "a redelivered source creates no duplicate", func(t *testing.T) {
		e := newEnv(t, factory)
		alice := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		bob := e.note("bob", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, alice, bob)

		again := e.insert(false,
			e.note("alice", "event-1", "task-1", "offer", 1, at(1)),
			e.note("bob", "event-1", "task-1", "offer", 1, at(1)))

		assert.Empty(t, again.Created)
		assert.Equal(t, 2, again.Duplicates)
		assert.Len(t, e.all("alice"), 1)
		assert.Len(t, e.all("bob"), 1)
	})

	parallel(t, "a duplicate within one insert is created once", func(t *testing.T) {
		e := newEnv(t, factory)

		result := e.insert(false,
			e.note("alice", "event-1", "task-1", "offer", 1, at(0)),
			e.note("alice", "event-1", "task-1", "offer", 1, at(0)))

		assert.Len(t, result.Created, 1)
		assert.Equal(t, 1, result.Duplicates)
		assert.Len(t, e.all("alice"), 1)
	})

	parallel(t, "a redelivery does not undo a read", func(t *testing.T) {
		e := newEnv(t, factory)
		n := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, n)
		e.markRead("alice", at(5), n.ID)

		e.insert(false, e.note("alice", "event-1", "task-1", "offer", 1, at(9)))

		got := e.get("alice", n.ID)
		assert.Equal(t, notify.StateRead, got.State)
		sameInstant(t, at(5), got.ReadAt, "read at")
	})

	parallel(t, "one source addresses many recipients independently", func(t *testing.T) {
		e := newEnv(t, factory)
		alice := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		bob := e.note("bob", "event-1", "task-1", "offer", 1, at(0))
		carol := e.note("carol", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, alice, bob, carol)

		e.markRead("alice", at(1), alice.ID)

		assert.Equal(t, notify.StateRead, e.get("alice", alice.ID).State)
		assert.Equal(t, notify.StateActive, e.get("bob", bob.ID).State)
		assert.Equal(t, notify.StateActive, e.get("carol", carol.ID).State)
	})

	parallel(t, "closing one kind leaves the others", func(t *testing.T) {
		e := newEnv(t, factory)
		offer := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		assigned := e.note("bob", "event-2", "task-1", "assigned", 1, at(0))
		e.insert(false, offer, assigned)

		result := e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 1, Reason: "taken"}, at(3))
		assert.Equal(t, int64(1), result.Closed)
		assert.Equal(t, []string{"alice"}, result.Recipients)

		closed := e.get("alice", offer.ID)
		assert.Equal(t, notify.StateClosed, closed.State)
		assert.Equal(t, "taken", closed.ClosedReason)
		sameInstant(t, at(3), closed.ClosedAt, "closed at")
		sameInstant(t, at(3), closed.InactiveAt, "inactive at")

		assert.Equal(t, notify.StateActive, e.get("bob", assigned.ID).State)
	})

	parallel(t, "closing every kind closes them all", func(t *testing.T) {
		e := newEnv(t, factory)
		offer := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		assigned := e.note("bob", "event-2", "task-1", "assigned", 1, at(0))
		other := e.note("bob", "event-3", "task-2", "assigned", 1, at(0))
		e.insert(false, offer, assigned)
		e.insert(false, other)

		result := e.close(notify.CloseRequest{Subject: "task-1", Version: 2, Reason: "completed"}, at(3))
		assert.Equal(t, int64(2), result.Closed)
		assert.Equal(t, []string{"alice", "bob"}, result.Recipients)
		assert.Equal(t, notify.StateClosed, e.get("alice", offer.ID).State)
		assert.Equal(t, notify.StateClosed, e.get("bob", assigned.ID).State)
		assert.Equal(t, notify.StateActive, e.get("bob", other.ID).State, "another subject is untouched")
	})

	parallel(t, "sparing one recipient", func(t *testing.T) {
		e := newEnv(t, factory)
		alice := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		bob := e.note("bob", "event-1", "task-1", "offer", 1, at(0))
		carol := e.note("carol", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, alice, bob, carol)

		result := e.close(notify.CloseRequest{
			Subject: "task-1", Kinds: []string{"offer"}, Version: 1, Reason: "taken", Except: "carol",
		}, at(2))

		assert.Equal(t, []string{"alice", "bob"}, result.Recipients)
		assert.Equal(t, notify.StateClosed, e.get("alice", alice.ID).State)
		assert.Equal(t, notify.StateClosed, e.get("bob", bob.ID).State)
		assert.Equal(t, notify.StateActive, e.get("carol", carol.ID).State)
	})

	parallel(t, "a newer notification survives an older close", func(t *testing.T) {
		e := newEnv(t, factory)
		n := e.note("alice", "event-7", "task-1", "assigned", 7, at(0))
		e.insert(false, n)

		result := e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"assigned"}, Version: 6, Reason: "x"}, at(1))

		assert.Zero(t, result.Closed)
		assert.Empty(t, result.Recipients)
		assert.Equal(t, notify.StateActive, e.get("alice", n.ID).State)
	})

	parallel(t, "a read notification can still be closed and keeps its read time", func(t *testing.T) {
		e := newEnv(t, factory)
		n := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, n)
		e.markRead("alice", at(2), n.ID)

		e.close(notify.CloseRequest{Subject: "task-1", Version: 1, Reason: "completed"}, at(8))

		got := e.get("alice", n.ID)
		assert.Equal(t, notify.StateClosed, got.State)
		sameInstant(t, at(2), got.ReadAt, "read at")
		sameInstant(t, at(8), got.ClosedAt, "closed at")
		sameInstant(t, at(2), got.InactiveAt, "inactive at stays when it first went inactive")
	})

	parallel(t, "a closed notification is left alone by a later close", func(t *testing.T) {
		e := newEnv(t, factory)
		n := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, n)
		e.close(notify.CloseRequest{Subject: "task-1", Version: 1, Reason: "taken"}, at(2))

		result := e.close(notify.CloseRequest{Subject: "task-1", Version: 3, Reason: "completed"}, at(4))

		assert.Zero(t, result.Closed)
		got := e.get("alice", n.ID)
		assert.Equal(t, "taken", got.ClosedReason)
		sameInstant(t, at(2), got.ClosedAt, "closed at")
	})

	parallel(t, "newest first with exact paging while notifications arrive", func(t *testing.T) {
		e := newEnv(t, factory)

		var published []notify.Notification

		for i := range 5 {
			n := e.note("alice", fmt.Sprintf("event-%d", i), fmt.Sprintf("task-%d", i), "offer", 1, at(i))
			e.insert(false, n)
			published = append(published, n)
		}

		e.insert(false, e.note("bob", "event-bob", "task-9", "offer", 1, at(3)))

		first := e.list(notify.ListQuery{Recipient: "alice", Limit: 2})
		require.NotEmpty(t, first.NextCursor)

		e.insert(false, e.note("alice", "event-late", "task-late", "offer", 1, at(10)))

		second := e.list(notify.ListQuery{Recipient: "alice", Limit: 2, Cursor: first.NextCursor})
		require.NotEmpty(t, second.NextCursor)

		third := e.list(notify.ListQuery{Recipient: "alice", Limit: 2, Cursor: second.NextCursor})
		assert.Empty(t, third.NextCursor)

		var got []notify.Notification
		for _, page := range []notify.Page{first, second, third} {
			got = append(got, page.Notifications...)
		}

		want := slices.Clone(published)
		slices.Reverse(want)
		assert.Equal(t, idsOf(want), idsOf(got))
	})

	parallel(t, "notifications created at the same instant page exactly", func(t *testing.T) {
		e := newEnv(t, factory)

		var published []notify.Notification

		for i := range 7 {
			n := e.note("alice", fmt.Sprintf("event-%d", i), "task-1", "offer", int64(i), at(0))
			published = append(published, n)
		}

		e.insert(false, published...)

		var got []notify.Notification

		q := notify.ListQuery{Recipient: "alice", Limit: 3}

		for {
			page := e.list(q)
			got = append(got, page.Notifications...)

			if page.NextCursor == "" {
				break
			}

			q.Cursor = page.NextCursor
		}

		want := slices.Clone(published)
		slices.Reverse(want)
		assert.Equal(t, idsOf(want), idsOf(got), "newest identifier first among equal instants")
	})

	parallel(t, "listing filters by state, kind and subject", func(t *testing.T) {
		e := newEnv(t, factory)
		offer1 := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		taken1 := e.note("alice", "event-2", "task-1", "taken", 1, at(1))
		offer2 := e.note("alice", "event-3", "task-2", "offer", 1, at(2))
		e.insert(false, offer1, taken1)
		e.insert(false, offer2)
		e.markRead("alice", at(3), offer2.ID)

		byState := e.list(notify.ListQuery{Recipient: "alice", States: []notify.State{notify.StateActive}})
		assert.Equal(t, []string{taken1.ID, offer1.ID}, idsOf(byState.Notifications))

		byKind := e.list(notify.ListQuery{Recipient: "alice", Kinds: []string{"offer"}})
		assert.Equal(t, []string{offer2.ID, offer1.ID}, idsOf(byKind.Notifications))

		bySubject := e.list(notify.ListQuery{Recipient: "alice", Subject: "task-1", Kinds: []string{"taken", "offer"}})
		assert.Equal(t, []string{taken1.ID, offer1.ID}, idsOf(bySubject.Notifications))
	})

	parallel(t, "counting counts only active notifications", func(t *testing.T) {
		e := newEnv(t, factory)

		var all []notify.Notification
		for i := range 6 {
			all = append(all, e.note("alice", fmt.Sprintf("event-%d", i), "task-1", fmt.Sprintf("k%d", i), 1, at(i)))
		}

		e.insert(false, all...)
		e.insert(false, e.note("bob", "event-bob", "task-1", "k0", 1, at(0)))
		e.markRead("alice", at(10), all[3].ID, all[4].ID)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"k5"}, Version: 1}, at(11))

		assert.Equal(t, int64(3), e.count("alice"))
		assert.Equal(t, int64(1), e.count("bob"))
		assert.Zero(t, e.count("nobody"))
	})

	parallel(t, "marking read moves active to read, and leaves read and closed as they are", func(t *testing.T) {
		e := newEnv(t, factory)
		active := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		closed := e.note("alice", "event-2", "task-2", "offer", 1, at(0))
		e.insert(false, active)
		e.insert(false, closed)
		e.close(notify.CloseRequest{Subject: "task-2", Version: 1, Reason: "taken"}, at(1))

		first := e.markRead("alice", at(2), active.ID)
		assert.Equal(t, int64(1), first.Marked)

		again := e.markRead("alice", at(5), active.ID)
		assert.Zero(t, again.Marked)

		read := e.get("alice", active.ID)
		assert.Equal(t, notify.StateRead, read.State)
		sameInstant(t, at(2), read.ReadAt, "read at is the first read")
		sameInstant(t, at(2), read.InactiveAt, "inactive at")

		closedResult := e.markRead("alice", at(6), closed.ID)
		assert.Zero(t, closedResult.Marked)

		got := e.get("alice", closed.ID)
		assert.Equal(t, notify.StateClosed, got.State)
		sameInstant(t, at(6), got.ReadAt, "a closed notification records when it was read")
		sameInstant(t, at(1), got.InactiveAt, "inactive at")
	})

	parallel(t, "mark all read does not swallow what arrived later", func(t *testing.T) {
		e := newEnv(t, factory)
		early := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		loaded := e.note("alice", "event-2", "task-2", "offer", 1, at(2))
		later := e.note("alice", "event-3", "task-3", "offer", 1, at(4))
		bobs := e.note("bob", "event-4", "task-1", "offer", 1, at(0))
		e.insert(false, early, bobs)
		e.insert(false, loaded)
		e.insert(false, later)

		result, err := e.store.MarkAllRead(t.Context(), "alice", at(2), at(9))
		require.NoError(t, err)

		assert.Equal(t, int64(2), result.Marked)
		assert.Equal(t, notify.StateRead, e.get("alice", early.ID).State)
		assert.Equal(t, notify.StateRead, e.get("alice", loaded.ID).State)
		assert.Equal(t, notify.StateActive, e.get("alice", later.ID).State)
		assert.Equal(t, notify.StateActive, e.get("bob", bobs.ID).State)
	})

	parallel(t, "another recipient's notification is not found", func(t *testing.T) {
		e := newEnv(t, factory)
		alices := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		bobs := e.note("bob", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, alices, bobs)

		_, err := e.store.Get(t.Context(), "bob", alices.ID)
		require.ErrorIs(t, err, notify.ErrNotFound)

		_, err = e.store.Get(t.Context(), "bob", "no-such-id")
		require.ErrorIs(t, err, notify.ErrNotFound)

		_, err = e.store.MarkRead(t.Context(), "bob", []string{alices.ID}, at(1))
		require.ErrorIs(t, err, notify.ErrNotFound)

		_, err = e.store.MarkRead(t.Context(), "bob", []string{bobs.ID, alices.ID}, at(1))
		require.ErrorIs(t, err, notify.ErrNotFound)

		assert.Equal(t, notify.StateActive, e.get("alice", alices.ID).State)
		assert.Equal(t, notify.StateActive, e.get("bob", bobs.ID).State, "nothing is marked when any id is not found")
	})
}

func runWatermark(t *testing.T, factory Factory) {
	parallel(t, "a retried older source is suppressed", func(t *testing.T) {
		e := newEnv(t, factory)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 5}, at(0))

		result := e.insert(false, e.note("alice", "event-1", "task-1", "offer", 1, at(1)))

		assert.Empty(t, result.Created)
		assert.Equal(t, 1, result.Suppressed)
		assert.Empty(t, e.all("alice"))
	})

	parallel(t, "a newer source still publishes after a close", func(t *testing.T) {
		e := newEnv(t, factory)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 5}, at(0))

		result := e.insert(false, e.note("alice", "event-6", "task-1", "offer", 6, at(1)))

		require.Len(t, result.Created, 1)
		assert.Equal(t, notify.StateActive, e.get("alice", result.Created[0].ID).State)
	})

	parallel(t, "a close and a publish at the same version can both stand", func(t *testing.T) {
		e := newEnv(t, factory)
		e.insert(false, e.note("alice", "event-1", "task-1", "assigned", 1, at(0)))
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"assigned"}, Version: 4, Except: "bob"}, at(1))

		result := e.insert(false, e.note("bob", "event-4", "task-1", "assigned", 4, at(1)))

		assert.Len(t, result.Created, 1, "suppression is strictly below the close version")
	})

	parallel(t, "a close of one kind does not suppress another kind", func(t *testing.T) {
		e := newEnv(t, factory)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 5}, at(0))

		result := e.insert(false, e.note("alice", "event-1", "task-1", "assigned", 1, at(1)))

		assert.Len(t, result.Created, 1)
	})

	parallel(t, "closing every kind suppresses every kind", func(t *testing.T) {
		e := newEnv(t, factory)
		e.close(notify.CloseRequest{Subject: "task-1", Version: 9}, at(0))

		older := e.insert(false, e.note("alice", "event-8", "task-1", "taken", 8, at(1)))
		assert.Empty(t, older.Created)
		assert.Equal(t, 1, older.Suppressed)

		same := e.insert(false, e.note("alice", "event-9", "task-1", "taken", 9, at(2)))
		assert.Len(t, same.Created, 1)
	})

	parallel(t, "a watermark is never lowered by an older close", func(t *testing.T) {
		e := newEnv(t, factory)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 8}, at(0))
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 3}, at(1))

		result := e.insert(false, e.note("alice", "event-5", "task-1", "offer", 5, at(2)))

		assert.Equal(t, 1, result.Suppressed)
	})

	parallel(t, "suppression applies per notification within one insert", func(t *testing.T) {
		e := newEnv(t, factory)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 5}, at(0))

		result := e.insert(false,
			e.note("alice", "event-1", "task-1", "offer", 1, at(1)),
			e.note("bob", "event-9", "task-1", "offer", 9, at(1)))

		assert.Equal(t, 1, result.Suppressed)
		require.Len(t, result.Created, 1)
		assert.Equal(t, "bob", result.Created[0].Recipient)
	})
}

func runSuccessors(t *testing.T, factory Factory) {
	offers := func(e *env) {
		e.insert(false,
			e.note("alice", "event-1", "task-1", "offer", 1, at(0)),
			e.note("bob", "event-1", "task-1", "offer", 1, at(0)),
			e.note("carol", "event-1", "task-1", "offer", 1, at(0)))
	}

	taken := func(skip ...string) notify.CloseRequest {
		return notify.CloseRequest{
			Subject: "task-1", Kinds: []string{"offer"}, Version: 5, Reason: "taken",
			Successor: &notify.Successor{
				SourceID: "event-5", Kind: "taken", Title: "Taken by carol", SubjectVersion: 5,
				Links: map[string]string{"task": "/v1/tasks/task-1"}, Data: json.RawMessage(`{"by":"carol"}`),
			},
			SuccessorSkip: skip,
		}
	}

	parallel(t, "closing offers tells every other recipient", func(t *testing.T) {
		e := newEnv(t, factory)
		offers(e)

		result := e.close(taken("carol"), at(4))

		assert.Equal(t, int64(3), result.Closed)
		assert.Equal(t, []string{"alice", "bob", "carol"}, result.Recipients)
		require.Len(t, result.Successors, 2)
		assert.Zero(t, result.SuccessorsSuppressed)

		for _, recipient := range []string{"alice", "bob"} {
			page := e.list(notify.ListQuery{Recipient: recipient, States: []notify.State{notify.StateActive}})
			require.Lenf(t, page.Notifications, 1, "%s has one active notification", recipient)

			got := page.Notifications[0]
			assert.Equal(t, "taken", got.Kind)
			assert.Equal(t, "task-1", got.Subject)
			assert.Equal(t, int64(5), got.SubjectVersion)
			assert.Equal(t, "event-5", got.SourceID)
			assert.Equal(t, "Taken by carol", got.Title)
			assert.JSONEq(t, `{"by":"carol"}`, string(got.Data))
			assert.True(t, at(4).Equal(got.CreatedAt))
			assert.Contains(t, idsOf(result.Successors), got.ID, "the result lists exactly what was created")
		}

		assert.Empty(t, e.list(notify.ListQuery{Recipient: "carol", States: []notify.State{notify.StateActive}}).Notifications)
	})

	parallel(t, "a retried close creates no further successors", func(t *testing.T) {
		e := newEnv(t, factory)
		offers(e)

		first := e.close(taken("carol"), at(4))
		second := e.close(taken("carol"), at(6))

		assert.Len(t, first.Successors, 2)
		assert.Zero(t, second.Closed)
		assert.Empty(t, second.Successors)

		for _, recipient := range []string{"alice", "bob"} {
			page := e.list(notify.ListQuery{Recipient: recipient, Kinds: []string{"taken"}})
			assert.Lenf(t, page.Notifications, 1, "%s keeps exactly one successor", recipient)
		}
	})

	parallel(t, "a successor below a newer watermark is suppressed", func(t *testing.T) {
		e := newEnv(t, factory)
		offers(e)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"taken"}, Version: 8}, at(2))

		result := e.close(taken(), at(4))

		assert.Equal(t, int64(3), result.Closed)
		assert.Empty(t, result.Successors)
		assert.Equal(t, 3, result.SuccessorsSuppressed)
		assert.Empty(t, e.list(notify.ListQuery{Recipient: "alice", Kinds: []string{"taken"}}).Notifications)
	})

	parallel(t, "only recipients this close closed get a successor", func(t *testing.T) {
		e := newEnv(t, factory)
		offers(e)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 1, Except: "alice"}, at(1))
		e.insert(false, e.note("alice", "event-2", "task-1", "offer", 2, at(2)))

		result := e.close(taken(), at(4))

		assert.Equal(t, []string{"alice"}, result.Recipients)
		require.Len(t, result.Successors, 1)
		assert.Equal(t, "alice", result.Successors[0].Recipient)
	})
}

func runCoalescing(t *testing.T, factory Factory) {
	parallel(t, "an open offer absorbs a coalescing offer", func(t *testing.T) {
		e := newEnv(t, factory)
		existing := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, existing)

		result := e.insert(true,
			e.note("alice", "event-2", "task-1", "offer", 2, at(1)),
			e.note("bob", "event-2", "task-1", "offer", 2, at(1)))

		assert.Equal(t, 1, result.Coalesced)
		require.Len(t, result.Created, 1)
		assert.Equal(t, "bob", result.Created[0].Recipient)
		assert.Equal(t, []string{existing.ID}, idsOf(e.all("alice")))
	})

	parallel(t, "a read notification also absorbs it", func(t *testing.T) {
		e := newEnv(t, factory)
		existing := e.note("alice", "event-1", "task-1", "offer", 1, at(0))
		e.insert(false, existing)
		e.markRead("alice", at(1), existing.ID)

		result := e.insert(true, e.note("alice", "event-2", "task-1", "offer", 2, at(2)))

		assert.Equal(t, 1, result.Coalesced)
		assert.Empty(t, result.Created)
	})

	parallel(t, "a closed notification does not absorb it", func(t *testing.T) {
		e := newEnv(t, factory)
		e.insert(false, e.note("alice", "event-1", "task-1", "offer", 1, at(0)))
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 3}, at(1))

		result := e.insert(true, e.note("alice", "event-4", "task-1", "offer", 4, at(2)))

		assert.Zero(t, result.Coalesced)
		assert.Len(t, result.Created, 1)
	})

	parallel(t, "a notification of another kind or subject does not absorb it", func(t *testing.T) {
		e := newEnv(t, factory)
		e.insert(false, e.note("alice", "event-1", "task-1", "assigned", 1, at(0)))
		e.insert(false, e.note("alice", "event-2", "task-2", "offer", 1, at(0)))

		result := e.insert(true, e.note("alice", "event-3", "task-1", "offer", 2, at(1)))

		assert.Zero(t, result.Coalesced)
		assert.Len(t, result.Created, 1)
	})

	parallel(t, "a non-coalescing notification is unaffected", func(t *testing.T) {
		e := newEnv(t, factory)
		e.insert(false, e.note("alice", "event-1", "task-1", "offer", 1, at(0)))

		result := e.insert(false, e.note("alice", "event-2", "task-1", "offer", 2, at(1)))

		assert.Zero(t, result.Coalesced)
		assert.Len(t, result.Created, 1)
		assert.Len(t, e.all("alice"), 2)
	})

	parallel(t, "two coalescing notifications for one recipient in one insert create one", func(t *testing.T) {
		e := newEnv(t, factory)

		result := e.insert(true,
			e.note("alice", "event-1", "task-1", "offer", 1, at(0)),
			e.note("alice", "event-2", "task-1", "offer", 1, at(0)))

		assert.Len(t, result.Created, 1)
		assert.Equal(t, 1, result.Coalesced)
		assert.Len(t, e.all("alice"), 1)
	})
}

func runConcurrency(t *testing.T, factory Factory) {
	parallel(t, "concurrent close and late publish never leave active below the watermark", func(t *testing.T) {
		e := newEnv(t, factory)

		for i := range Iterations {
			subject := fmt.Sprintf("task-%d", i)
			late := e.note("alice", "event-late-"+subject, subject, "offer", 3, at(1))

			var (
				wg                 sync.WaitGroup
				closeErr, insertEr error
			)

			wg.Go(func() {
				_, closeErr = e.store.Close(t.Context(), notify.CloseRequest{
					Subject: subject, Kinds: []string{"offer"}, Version: 5, Reason: "taken",
				}, at(2), e.ids)
			})
			wg.Go(func() {
				_, insertEr = e.tryInsert(false, late)
			})
			wg.Wait()

			require.NoError(t, closeErr)
			require.NoError(t, insertEr)

			page := e.list(notify.ListQuery{
				Recipient: "alice", Subject: subject, Kinds: []string{"offer"},
				States: []notify.State{notify.StateActive},
			})

			for _, n := range page.Notifications {
				require.GreaterOrEqualf(t, n.SubjectVersion, int64(5),
					"iteration %d left an active offer at version %d below the watermark", i, n.SubjectVersion)
			}
		}
	})

	parallel(t, "concurrent coalescing publishes for one recipient create one notification", func(t *testing.T) {
		e := newEnv(t, factory)

		for i := range Iterations {
			subject := fmt.Sprintf("task-%d", i)
			one := e.note("alice", "event-a-"+subject, subject, "offer", 1, at(1))
			two := e.note("alice", "event-b-"+subject, subject, "offer", 1, at(1))

			var (
				wg         sync.WaitGroup
				errA, errB error
			)

			wg.Go(func() { _, errA = e.tryInsert(true, one) })
			wg.Go(func() { _, errB = e.tryInsert(true, two) })
			wg.Wait()

			require.NoError(t, errA)
			require.NoError(t, errB)

			page := e.list(notify.ListQuery{Recipient: "alice", Subject: subject})
			require.Lenf(t, page.Notifications, 1, "iteration %d", i)
		}
	})
}

func runRetention(t *testing.T, factory Factory) {
	now := base.Add(days(365))

	// seed inserts n notifications for a recipient, created a second apart
	// starting at from, each on its own subject.
	seed := func(e *env, recipient, prefix string, n int, from time.Time) []notify.Notification {
		out := make([]notify.Notification, 0, n)

		for i := range n {
			subject := fmt.Sprintf("%s-%s-%d", recipient, prefix, i)
			note := e.note(recipient, "event-"+subject, subject, "offer", 1, from.Add(time.Duration(i)*time.Second))
			e.insert(false, note)
			out = append(out, note)
		}

		return out
	}

	readAll := func(e *env, recipient string, when time.Time, notifications []notify.Notification) {
		e.markRead(recipient, when, idsOf(notifications)...)
	}

	parallel(t, "age counts from becoming inactive", func(t *testing.T) {
		e := newEnv(t, factory)
		recent := seed(e, "alice", "recent", 1, now.Add(-days(120)))
		old := seed(e, "alice", "old", 1, now.Add(-days(100)))
		readAll(e, "alice", now.Add(-days(10)), recent)
		readAll(e, "alice", now.Add(-days(95)), old)

		result := e.prune(notify.PruneRequest{Now: now, MaxAge: days(90)})

		assert.Equal(t, int64(1), result.DeletedForAge)
		assert.Equal(t, idsOf(recent), idsOf(e.all("alice")))
	})

	parallel(t, "age never removes an active notification", func(t *testing.T) {
		e := newEnv(t, factory)
		active := seed(e, "alice", "active", 1, now.Add(-days(200)))

		result := e.prune(notify.PruneRequest{Now: now, MaxAge: days(90)})

		assert.Zero(t, result.DeletedForAge)
		assert.Equal(t, idsOf(active), idsOf(e.all("alice")))
	})

	parallel(t, "both bounds apply together", func(t *testing.T) {
		e := newEnv(t, factory)
		old := seed(e, "alice", "old", 10, now.Add(-days(60)))
		kept := seed(e, "alice", "kept", 40, now.Add(-days(50)))
		readAll(e, "alice", now.Add(-days(40)), old)

		result := e.prune(notify.PruneRequest{Now: now, MaxAge: days(30), MaxPerRecipient: 100, Batch: 3})

		assert.Equal(t, int64(10), result.DeletedForAge)
		assert.Zero(t, result.DeletedForCount)
		assert.ElementsMatch(t, idsOf(kept), idsOf(e.all("alice")))
	})

	parallel(t, "inactive notifications go first", func(t *testing.T) {
		e := newEnv(t, factory)
		read := seed(e, "alice", "read", 3, now.Add(-days(5)))
		active := seed(e, "alice", "active", 4, now.Add(-days(6)))
		readAll(e, "alice", now.Add(-days(1)), read)

		result := e.prune(notify.PruneRequest{Now: now, MaxPerRecipient: 5})

		assert.Equal(t, int64(2), result.DeletedForCount)
		assert.Zero(t, result.EvictedActive)
		assert.Empty(t, result.Recipients)
		assert.ElementsMatch(t, append(idsOf(active), read[2].ID), idsOf(e.all("alice")),
			"the two oldest read notifications are the ones deleted")
	})

	parallel(t, "the default strategy evicts the oldest active notifications when it must", func(t *testing.T) {
		e := newEnv(t, factory)
		active := seed(e, "alice", "active", 8, now.Add(-days(3)))

		result := e.prune(notify.PruneRequest{Now: now, MaxPerRecipient: 5, Strategy: notify.EvictOldestActive, Batch: 2})

		assert.Equal(t, int64(3), result.EvictedActive)
		assert.Zero(t, result.DeletedForCount)
		assert.Equal(t, []string{"alice"}, result.Recipients)
		assert.ElementsMatch(t, idsOf(active[3:]), idsOf(e.all("alice")))
	})

	parallel(t, "retain active never deletes an unread notification", func(t *testing.T) {
		e := newEnv(t, factory)
		active := seed(e, "alice", "active", 8, now.Add(-days(3)))

		result := e.prune(notify.PruneRequest{Now: now, MaxPerRecipient: 5, Strategy: notify.RetainActive})

		assert.Zero(t, result.EvictedActive)
		assert.Zero(t, result.DeletedForCount)
		assert.Empty(t, result.Recipients)
		assert.ElementsMatch(t, idsOf(active), idsOf(e.all("alice")))
	})

	parallel(t, "evictions are reported separately and other recipients are untouched", func(t *testing.T) {
		e := newEnv(t, factory)
		old := seed(e, "alice", "old", 7, now.Add(-days(60)))
		recent := seed(e, "alice", "recent", 3, now.Add(-days(20)))
		active := seed(e, "alice", "active", 7, now.Add(-days(10)))
		bobs := seed(e, "bob", "active", 5, now.Add(-days(10)))
		readAll(e, "alice", now.Add(-days(40)), old)
		readAll(e, "alice", now.Add(-days(2)), recent)

		result := e.prune(notify.PruneRequest{Now: now, MaxAge: days(30), MaxPerRecipient: 5, Batch: 2})

		assert.Equal(t, int64(7), result.DeletedForAge)
		assert.Equal(t, int64(3), result.DeletedForCount)
		assert.Equal(t, int64(2), result.EvictedActive)
		assert.Equal(t, []string{"alice"}, result.Recipients)
		assert.ElementsMatch(t, idsOf(active[2:]), idsOf(e.all("alice")))
		assert.ElementsMatch(t, idsOf(bobs), idsOf(e.all("bob")))
	})

	parallel(t, "a pass with nothing to do deletes nothing", func(t *testing.T) {
		e := newEnv(t, factory)
		kept := seed(e, "alice", "active", 3, now.Add(-days(1)))

		result := e.prune(notify.PruneRequest{
			Now: now, MaxAge: days(30), MaxPerRecipient: 5, WatermarkRetention: days(7),
		})

		assert.Equal(t, notify.PruneResult{}, withoutNilRecipients(result))
		assert.ElementsMatch(t, idsOf(kept), idsOf(e.all("alice")))
	})

	parallel(t, "close records expire only for empty subjects past retention", func(t *testing.T) {
		e := newEnv(t, factory)

		expired := "expired"
		recent := "recent"
		occupied := "occupied"

		e.close(notify.CloseRequest{Subject: expired, Kinds: []string{"offer"}, Version: 5}, now.Add(-days(8)))
		e.close(notify.CloseRequest{Subject: recent, Kinds: []string{"offer"}, Version: 5}, now.Add(-days(2)))
		e.insert(false, e.note("bob", "event-occupied", occupied, "assigned", 6, now.Add(-days(9))))
		e.close(notify.CloseRequest{Subject: occupied, Kinds: []string{"offer"}, Version: 5}, now.Add(-days(8)))

		result := e.prune(notify.PruneRequest{Now: now, MaxAge: days(90), WatermarkRetention: days(7)})

		assert.Positive(t, result.WatermarksDeleted)

		afterExpiry := e.insert(false, e.note("alice", "event-x", expired, "offer", 1, now))
		assert.Len(t, afterExpiry.Created, 1, "an expired close record no longer suppresses")

		stillRecent := e.insert(false, e.note("alice", "event-y", recent, "offer", 1, now))
		assert.Equal(t, 1, stillRecent.Suppressed, "a recent close record is kept")

		stillOccupied := e.insert(false, e.note("alice", "event-z", occupied, "offer", 1, now))
		assert.Equal(t, 1, stillOccupied.Suppressed, "a subject with notifications keeps its close record")
	})

	parallel(t, "close records never expire without a retention", func(t *testing.T) {
		e := newEnv(t, factory)
		e.close(notify.CloseRequest{Subject: "task-1", Kinds: []string{"offer"}, Version: 5}, now.Add(-days(300)))

		result := e.prune(notify.PruneRequest{Now: now, MaxAge: days(1)})

		assert.Zero(t, result.WatermarksDeleted)
		assert.Equal(t, 1, e.insert(false, e.note("alice", "event-1", "task-1", "offer", 1, now)).Suppressed)
	})
}

// withoutNilRecipients normalises an empty recipient list, so that a store
// returning an empty slice and one returning nil compare equal.
func withoutNilRecipients(result notify.PruneResult) notify.PruneResult {
	if len(result.Recipients) == 0 {
		result.Recipients = nil
	}

	return result
}
