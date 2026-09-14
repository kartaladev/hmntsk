package notify_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// dispatchStart is when a dispatch test publishes its first notification.
var dispatchStart = time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)

// movableClock is a clock a test moves by hand.
type movableClock struct{ now atomic.Pointer[time.Time] }

func newMovableClock(at time.Time) *movableClock {
	c := &movableClock{}
	c.set(at)

	return c
}

func (c *movableClock) Now() time.Time { return *c.now.Load() }

func (c *movableClock) set(at time.Time) { c.now.Store(&at) }

func (c *movableClock) advance(d time.Duration) { c.set(c.Now().Add(d)) }

// recordingEmailStore decorates a real email store, keeping every record written
// and, once dropped is set, writing none, as a process that stopped would.
type recordingEmailStore struct {
	notify.EmailStore

	mu      sync.Mutex
	records []notify.EmailRecord
	dropped atomic.Bool
}

func (s *recordingEmailStore) RecordEmails(ctx context.Context, record notify.EmailRecord) (int64, error) {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()

	if s.dropped.Load() && record.Status != notify.EmailStatusSending {
		return int64(len(record.IDs)), nil
	}

	return s.EmailStore.RecordEmails(ctx, record)
}

// statusesOf lists, in order, the statuses recorded for a notification.
func (s *recordingEmailStore) statusesOf(id string) []notify.EmailStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []notify.EmailStatus

	for _, record := range s.records {
		if slices.Contains(record.IDs, id) {
			out = append(out, record.Status)
		}
	}

	return out
}

// lastRecordOf returns the last record written for a notification.
func (s *recordingEmailStore) lastRecordOf(t *testing.T, id string) notify.EmailRecord {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.records) - 1; i >= 0; i-- {
		if slices.Contains(s.records[i].IDs, id) {
			return s.records[i]
		}
	}

	t.Fatalf("no record was written for %s", id)

	return notify.EmailRecord{}
}

// deletingStore decorates a notification store so that reading chosen
// notifications finds them gone.
type deletingStore struct {
	notify.Store

	gone sync.Map
}

func (s *deletingStore) Get(ctx context.Context, recipient, id string) (notify.Notification, error) {
	if _, ok := s.gone.Load(id); ok {
		return notify.Notification{}, notify.ErrNotFound
	}

	return s.Store.Get(ctx, recipient, id)
}

// combinedStore is a notification store and an email store built from two
// decorators over one memory store.
type combinedStore struct {
	notify.Store
	notify.EmailStore
}

// dispatchHarness is one dispatch case's service, dispatcher and the host ports
// it observes.
type dispatchHarness struct {
	t         *testing.T
	clock     *movableClock
	svc       *notify.Service
	store     *deletingStore
	email     *recordingEmailStore
	addresses map[string]string

	mu       sync.Mutex
	messages []notify.EmailMessage
	errs     []error

	// send decides a send's outcome; nil sends successfully.
	send func(ctx context.Context, message notify.EmailMessage) error
	// filter decides what is emailed; nil emails everything.
	filter func(ctx context.Context, n notify.Notification) (bool, error)
	// kinds, when set, selects with WithEmailKinds instead of filter.
	kinds []string
	// lookup fails a recipient's address lookup when it returns an error.
	lookup func(recipient string) error
	// render fails a recipient's rendering when it returns an error.
	render func(batch notify.EmailBatch) error

	dispatcher *notify.EmailDispatcher
}

func newDispatchHarness(t *testing.T) *dispatchHarness {
	t.Helper()

	memory := notify.NewMemoryStore()
	h := &dispatchHarness{
		t:         t,
		clock:     newMovableClock(dispatchStart),
		store:     &deletingStore{Store: memory},
		email:     &recordingEmailStore{EmailStore: memory},
		addresses: map[string]string{"alice": "alice@example.com", "bob": "bob@example.com"},
	}

	svc, err := notify.New(combinedStore{Store: h.store, EmailStore: h.email}, notify.WithClock(h.clock))
	require.NoError(t, err)

	h.svc = svc

	return h
}

// build constructs the dispatcher with the harness's ports and options.
func (h *dispatchHarness) build(opts ...notify.EmailOption) *notify.EmailDispatcher {
	h.t.Helper()

	mailer := notify.MailerFunc(func(ctx context.Context, message notify.EmailMessage) error {
		h.mu.Lock()
		h.messages = append(h.messages, message)
		send := h.send
		h.mu.Unlock()

		if send != nil {
			return send(ctx, message)
		}

		return nil
	})

	book := notify.AddressBookFunc(func(_ context.Context, recipient string) (string, bool, error) {
		if h.lookup != nil {
			if err := h.lookup(recipient); err != nil {
				return "", false, err
			}
		}

		address, ok := h.addresses[recipient]

		return address, ok, nil
	})

	template := notify.EmailTemplateFunc(func(_ context.Context, batch notify.EmailBatch) (notify.EmailContent, error) {
		if h.render != nil {
			if err := h.render(batch); err != nil {
				return notify.EmailContent{}, err
			}
		}

		return notify.EmailContent{Subject: fmt.Sprintf("%d notifications", len(batch.Notifications)), TextBody: "body"}, nil
	})

	selection := notify.WithEmailFilter(notify.EmailFilterFunc(func(ctx context.Context, n notify.Notification) (bool, error) {
		if h.filter != nil {
			return h.filter(ctx, n)
		}

		return true, nil
	}))

	if len(h.kinds) > 0 {
		selection = notify.WithEmailKinds(h.kinds...)
	}

	opts = append([]notify.EmailOption{
		notify.WithEmailOwner("dispatcher-" + h.t.Name()),
		notify.WithEmailErrorHandler(func(_ context.Context, err error) {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.errs = append(h.errs, err)
		}),
		selection,
	}, opts...)

	d, err := notify.NewEmailDispatcher(h.svc, mailer, book, template, opts...)
	require.NoError(h.t, err)

	h.dispatcher = d

	return d
}

// publish publishes one notification for a recipient at the clock's instant.
func (h *dispatchHarness) publish(recipient, kind string) notify.Notification {
	h.t.Helper()

	id := fmt.Sprintf("%s-%d", recipient, h.clock.Now().UnixNano())
	result, err := h.svc.Publish(h.t.Context(), notify.Draft{
		Recipient: recipient, SourceID: "event-" + id, Subject: "task-" + id, Kind: kind,
	})
	require.NoError(h.t, err)
	require.Len(h.t, result.Created, 1)

	h.clock.advance(time.Second)

	return result.Created[0]
}

// dispatch runs one pass at the clock's instant.
func (h *dispatchHarness) dispatch() (notify.DispatchResult, error) {
	h.t.Helper()

	return h.dispatcher.Dispatch(h.t.Context())
}

// sent returns the messages the mailer received so far.
func (h *dispatchHarness) sent() []notify.EmailMessage {
	h.mu.Lock()
	defer h.mu.Unlock()

	return slices.Clone(h.messages)
}

// reported returns the errors the error handler received so far.
func (h *dispatchHarness) reported() []error {
	h.mu.Lock()
	defer h.mu.Unlock()

	return slices.Clone(h.errs)
}

// ids lists notifications' identifiers.
func ids(notifications ...notify.Notification) []string {
	out := make([]string, 0, len(notifications))
	for _, n := range notifications {
		out = append(out, n.ID)
	}

	return out
}

// pastGrace moves the clock past the default grace delay.
func (h *dispatchHarness) pastGrace() { h.clock.advance(notify.DefaultEmailGraceDelay + time.Minute) }

func TestEmailDispatcherDispatch(t *testing.T) {
	t.Parallel()

	errTransient := errors.New("connection refused")

	type testCase struct {
		name string
		// run prepares the harness, runs one or more passes, and returns the
		// last pass's result.
		run    func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error)
		assert func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error)
	}

	cases := []testCase{
		{
			name: "each recipient gets one message covering their notifications, oldest first",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("alice", "offer")
				h.publish("bob", "offer")
				h.publish("alice", "assigned")
				h.publish("alice", "offer")
				h.pastGrace()

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 4, Sent: 4, Messages: 2}, result)

				messages := h.sent()
				require.Len(t, messages, 2)

				byRecipient := map[string]notify.EmailMessage{}
				for _, m := range messages {
					byRecipient[m.Recipient] = m
				}

				assert.Len(t, byRecipient["alice"].NotificationIDs, 3)
				assert.Equal(t, "alice@example.com", byRecipient["alice"].To)
				assert.Len(t, byRecipient["bob"].NotificationIDs, 1)
				assert.NotEmpty(t, byRecipient["alice"].IdempotencyKey)
				assert.NotEqual(t, byRecipient["alice"].IdempotencyKey, byRecipient["bob"].IdempotencyKey)
			},
		},
		{
			name: "a burst beyond the batch limit sends the oldest, and a later pass sends the rest at once",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build(notify.WithEmailBatchLimit(2))

				var published []notify.Notification
				for range 3 {
					published = append(published, h.publish("alice", "offer"))
				}

				h.pastGrace()

				first, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 3, Sent: 2, Messages: 1}, first)
				assert.Equal(t, ids(published[:2]...), h.sent()[0].NotificationIDs)
				assert.Equal(t, notify.EmailStatusClaimed, h.email.lastRecordOf(t, published[2].ID).Status, "released")

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Sent: 1, Messages: 1}, result)
				require.Len(t, h.sent(), 2)
			},
		},
		{
			name: "a filtered notification is skipped, never reconsidered, and reports no error",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.kinds = []string{"assigned"}
				h.build()
				h.publish("alice", "taken")
				h.publish("alice", "assigned")
				h.pastGrace()

				first, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 2, Sent: 1, Messages: 1, SkippedFiltered: 1}, first)

				h.clock.advance(time.Hour)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{}, result)
				assert.Empty(t, h.reported())
			},
		},
		{
			name: "a recipient with no address is skipped without an error, and never reconsidered",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("carol", "offer")
				h.pastGrace()

				first, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, SkippedNoAddress: 1}, first)

				h.clock.advance(time.Hour)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{}, result)
				assert.Empty(t, h.sent())
				assert.Empty(t, h.reported())
			},
		},
		{
			name: "a failed address lookup is retried after a jittered backoff and reported",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("alice", "offer")
				h.pastGrace()

				h.lookup = func(string) error { return errTransient }

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Retried: 1}, result)
				require.Len(t, h.reported(), 1)
				assert.ErrorIs(t, h.reported()[0], errTransient)

				page, listErr := h.svc.List(t.Context(), notify.ListQuery{Recipient: "alice"})
				require.NoError(t, listErr)

				record := h.email.lastRecordOf(t, page.Notifications[0].ID)
				assert.Equal(t, notify.EmailStatusRetry, record.Status)
				assert.True(t, record.Attempt)
				require.NotNil(t, record.NextAttemptAt)

				delay := record.NextAttemptAt.Sub(h.clock.Now())
				assert.GreaterOrEqual(t, delay, 48*time.Second)
				assert.LessOrEqual(t, delay, 72*time.Second)

				h.lookup = nil

				early, err := h.dispatch()
				require.NoError(t, err)
				assert.Zero(t, early.Claimed, "not before it is due")

				h.clock.advance(2 * time.Minute)

				due, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, 1, due.Sent)
			},
		},
		{
			name: "a failing filter is retried and reported",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("alice", "offer")
				h.pastGrace()

				h.filter = func(context.Context, notify.Notification) (bool, error) { return false, errTransient }

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Retried: 1}, result)
				require.Len(t, h.reported(), 1)
				assert.ErrorIs(t, h.reported()[0], errTransient)
			},
		},
		{
			name: "notifications read, closed or deleted after claiming are skipped, and an emptied batch sends nothing",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				read := h.publish("alice", "offer")
				closed := h.publish("alice", "offer")
				deleted := h.publish("alice", "offer")
				h.pastGrace()

				h.filter = func(ctx context.Context, n notify.Notification) (bool, error) {
					switch n.ID {
					case read.ID:
						_, err := h.svc.MarkRead(ctx, "alice", n.ID)

						return true, err
					case closed.ID:
						_, err := h.svc.Close(ctx, notify.CloseRequest{Subject: n.Subject, Version: 1, Reason: "taken"})

						return true, err
					case deleted.ID:
						h.store.gone.Store(n.ID, true)
					}

					return true, nil
				}

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 3, SkippedInactive: 3}, result)
				assert.Empty(t, h.sent())
				assert.Empty(t, h.reported())
			},
		},
		{
			name: "SENDING with the message's key is recorded before the send, and SENT after it",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				n := h.publish("alice", "offer")
				h.pastGrace()

				h.send = func(_ context.Context, message notify.EmailMessage) error {
					record := h.email.lastRecordOf(t, n.ID)
					assert.Equal(t, notify.EmailStatusSending, record.Status, "recorded before the send")
					assert.Equal(t, message.IdempotencyKey, record.BatchID)

					return nil
				}

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Sent: 1, Messages: 1}, result)

				n := h.sent()[0].NotificationIDs[0]
				assert.Equal(t, []notify.EmailStatus{notify.EmailStatusSending, notify.EmailStatusSent}, h.email.statusesOf(n))
			},
		},
		{
			name: "a rejected send fails for good and is reported",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("alice", "offer")
				h.pastGrace()

				h.send = func(context.Context, notify.EmailMessage) error {
					return fmt.Errorf("550 mailbox unavailable: %w", notify.ErrMailRejected)
				}

				first, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Failed: 1}, first)

				h.clock.advance(2 * time.Hour)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{}, result)
				require.Len(t, h.reported(), 1)
				assert.ErrorIs(t, h.reported()[0], notify.ErrMailRejected)
			},
		},
		{
			name: "a transient send failure is retried, and fails at the attempt limit",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build(notify.WithEmailMaxAttempts(2), notify.WithEmailBackoff(time.Minute, time.Minute))
				h.publish("alice", "offer")
				h.pastGrace()

				h.send = func(context.Context, notify.EmailMessage) error { return errTransient }

				first, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Retried: 1}, first)

				h.clock.advance(2 * time.Minute)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Failed: 1}, result)
				require.Len(t, h.sent(), 2)
				assert.NotEqual(t, h.sent()[0].IdempotencyKey, h.sent()[1].IdempotencyKey,
					"a message that was not sent is a new message")
				assert.Len(t, h.reported(), 2)
			},
		},
		{
			name: "a render failure fails for good without stopping the pass",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("alice", "offer")
				h.publish("bob", "offer")
				h.pastGrace()

				h.render = func(batch notify.EmailBatch) error {
					if batch.Recipient == "alice" {
						return errors.New("template: missing field")
					}

					return nil
				}

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 2, Sent: 1, Messages: 1, Failed: 1}, result)
				require.Len(t, h.sent(), 1)
				assert.Equal(t, "bob", h.sent()[0].Recipient)
				assert.Len(t, h.reported(), 1)
			},
		},
		{
			name: "at most once: a send in doubt is abandoned and never repeated",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("alice", "offer")
				h.pastGrace()

				h.send = func(context.Context, notify.EmailMessage) error { return notify.ErrMailInDoubt }

				first, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Abandoned: 1}, first)

				h.send = nil
				h.clock.advance(time.Hour)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{}, result)
				assert.Len(t, h.sent(), 1)
			},
		},
		{
			name: "at most once: a pass that stopped after sending is abandoned by the next, not resent",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				h.publish("alice", "offer")
				h.pastGrace()

				h.email.dropped.Store(true)

				_, err := h.dispatch()
				require.NoError(t, err)

				h.email.dropped.Store(false)
				h.clock.advance(notify.DefaultEmailLease + time.Minute)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Abandoned: 1}, result)
				assert.Len(t, h.sent(), 1, "never sent again")
			},
		},
		{
			name: "at least once: a send in doubt is resent with the same key over exactly the same notifications",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build(notify.WithDeliveryGuarantee(notify.AtLeastOnce))
				h.publish("alice", "offer")
				h.pastGrace()

				h.email.dropped.Store(true)

				_, err := h.dispatch()
				require.NoError(t, err)

				h.email.dropped.Store(false)

				h.clock.advance(-notify.DefaultEmailGraceDelay)
				h.publish("alice", "assigned")
				h.clock.advance(notify.DefaultEmailLease + notify.DefaultEmailGraceDelay + time.Minute)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 2, Sent: 2, Messages: 2}, result)

				messages := h.sent()
				require.Len(t, messages, 3)
				assert.Equal(t, messages[0].IdempotencyKey, messages[1].IdempotencyKey, "the resend keeps the key")
				assert.Equal(t, messages[0].NotificationIDs, messages[1].NotificationIDs, "and covers the same notifications")
				assert.NotEqual(t, messages[0].IdempotencyKey, messages[2].IdempotencyKey, "the newer one is a separate message")
				assert.Len(t, messages[2].NotificationIDs, 1)
			},
		},
		{
			name: "at least once: ErrMailInDoubt resends the same message once its lease lapses",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build(notify.WithDeliveryGuarantee(notify.AtLeastOnce))
				h.publish("alice", "offer")
				h.pastGrace()

				h.send = func(context.Context, notify.EmailMessage) error { return notify.ErrMailInDoubt }

				first, err := h.dispatch()
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Retried: 1}, first)

				h.send = nil
				h.clock.advance(notify.DefaultEmailLease + time.Minute)

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 1, Sent: 1, Messages: 1}, result)

				messages := h.sent()
				require.Len(t, messages, 2)
				assert.Equal(t, messages[0].IdempotencyKey, messages[1].IdempotencyKey)
			},
		},
		{
			name: "a mixed pass counts each outcome",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.addresses["dave"] = "dave@example.com"
				h.build()
				h.publish("alice", "offer")
				h.publish("alice", "offer")
				h.publish("carol", "offer")
				h.publish("dave", "offer")
				h.pastGrace()

				h.send = func(_ context.Context, message notify.EmailMessage) error {
					if message.Recipient == "dave" {
						return errTransient
					}

					return nil
				}

				return h.dispatch()
			},
			assert: func(t *testing.T, h *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Claimed: 4, Sent: 2, Messages: 1, SkippedNoAddress: 1, Retried: 1}, result)
			},
		},
		{
			name: "each pass purges delivery records of deleted notifications",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				h.build()
				n := h.publish("alice", "offer")
				h.pastGrace()

				_, err := h.dispatch()
				require.NoError(t, err)

				_, err = h.svc.MarkRead(t.Context(), "alice", n.ID)
				require.NoError(t, err)

				h.clock.advance(48 * time.Hour)

				pruner, err := notify.NewPruner(h.svc, notify.WithMaxAge(time.Hour))
				require.NoError(t, err)

				_, err = pruner.Prune(t.Context())
				require.NoError(t, err)

				return h.dispatch()
			},
			assert: func(t *testing.T, _ *dispatchHarness, result notify.DispatchResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, notify.DispatchResult{Purged: 1}, result)
			},
		},
		{
			name: "a failed claim is returned",
			run: func(t *testing.T, h *dispatchHarness) (notify.DispatchResult, error) {
				return (&failingClaimDispatcher{h: h}).dispatch(t.Context())
			},
			assert: func(t *testing.T, _ *dispatchHarness, result notify.DispatchResult, err error) {
				require.ErrorIs(t, err, errClaim)
				assert.Equal(t, notify.DispatchResult{}, result)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newDispatchHarness(t)
			result, err := tc.run(t, h)
			tc.assert(t, h, result, err)
		})
	}
}

// errClaim is what a failing email store returns from a claim.
var errClaim = errors.New("claim failed")

// failingClaimDispatcher builds a dispatcher over a store whose claims fail.
type failingClaimDispatcher struct{ h *dispatchHarness }

type failingClaims struct{ notify.EmailStore }

func (failingClaims) ClaimEmails(context.Context, notify.EmailClaim) ([]notify.EmailCandidate, error) {
	return nil, errClaim
}

func (f *failingClaimDispatcher) dispatch(ctx context.Context) (notify.DispatchResult, error) {
	svc, err := notify.New(combinedStore{Store: f.h.store, EmailStore: failingClaims{f.h.email}})
	require.NoError(f.h.t, err)

	d, err := notify.NewEmailDispatcher(svc, noopMailer, noopBook, noopTemplate)
	require.NoError(f.h.t, err)

	return d.Dispatch(ctx)
}

func TestEmailDispatcherRun(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		interval time.Duration
		ctx      func(ctx context.Context) context.Context
		assert   func(t *testing.T, h *dispatchHarness, err error)
	}

	cases := []testCase{
		{
			name:     "it passes at once and every interval, reports errors, and returns the context's error when cancelled",
			interval: time.Millisecond,
			assert: func(t *testing.T, h *dispatchHarness, err error) {
				require.ErrorIs(t, err, context.Canceled)
				assert.GreaterOrEqual(t, len(h.reported()), 3, "one reported failure per pass")
			},
		},
		{
			name:     "a context already cancelled returns at once",
			interval: time.Hour,
			ctx: func(ctx context.Context) context.Context {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				return cancelled
			},
			assert: func(t *testing.T, _ *dispatchHarness, err error) {
				require.ErrorIs(t, err, context.Canceled)
			},
		},
		{
			name:     "a non-positive interval is a configuration error",
			interval: 0,
			assert: func(t *testing.T, _ *dispatchHarness, err error) {
				require.ErrorIs(t, err, notify.ErrConfiguration)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newDispatchHarness(t)
			h.build(notify.WithoutEmailGraceDelay())
			h.lookup = func(string) error { return errors.New("directory down") }

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}

			if tc.interval > 0 && tc.ctx == nil {
				var passes atomic.Int32

				h.lookup = func(string) error {
					// Move past the retry so that the next pass claims it again.
					h.clock.advance(time.Second)

					if passes.Add(1) >= 3 {
						cancel()
					}

					return errors.New("directory down")
				}

				// Each pass needs a notification to fail on: one per recipient,
				// retried with no backoff.
				h.build(notify.WithoutEmailGraceDelay(), notify.WithEmailBackoff(time.Nanosecond, time.Nanosecond))

				for range 3 {
					h.publish("alice", "offer")
				}
			}

			tc.assert(t, h, h.dispatcher.Run(ctx, tc.interval))
		})
	}
}
