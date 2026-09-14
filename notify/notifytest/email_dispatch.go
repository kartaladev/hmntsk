package notifytest

import (
	"context"
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

// RunEmailDispatch runs email dispatchers end to end over stores the factory
// builds: concurrent dispatchers, a dispatcher that stops mid-send, and a
// redeploy.
func RunEmailDispatch(t *testing.T, factory EmailFactory) {
	t.Helper()

	parallel(t, "two dispatchers running at once send every notification exactly once", func(t *testing.T) {
		d := newDispatchEnv(t, factory)

		var published []string

		for i := range 24 {
			published = append(published, d.publish(fmt.Sprintf("user-%d", i%6)).ID)
		}

		d.clock.advance(10 * time.Minute)

		opts := []notify.EmailOption{notify.WithEmailClaimLimit(5), notify.WithEmailBatchLimit(2)}
		one, two := d.dispatcher("one", opts...), d.dispatcher("two", opts...)

		for round := 0; ; round++ {
			require.Lessf(t, round, 100, "dispatch did not finish")

			var (
				wg           sync.WaitGroup
				first, other notify.DispatchResult
				errA, errB   error
			)

			wg.Go(func() { first, errA = one.Dispatch(t.Context()) })
			wg.Go(func() { other, errB = two.Dispatch(t.Context()) })
			wg.Wait()

			require.NoError(t, errA)
			require.NoError(t, errB)

			if first.Claimed == 0 && other.Claimed == 0 {
				break
			}
		}

		d.clock.advance(time.Hour)

		late, err := one.Dispatch(t.Context())
		require.NoError(t, err)
		assert.Zero(t, late.Claimed, "no delivery is left claimed or sending once leases lapse")

		sent := d.sentIDs()
		slices.Sort(sent)
		slices.Sort(published)
		assert.Equal(t, published, sent, "every notification is sent exactly once")
		assert.Empty(t, d.reported())
	})

	for _, guarantee := range []notify.DeliveryGuarantee{notify.AtMostOnce, notify.AtLeastOnce} {
		parallel(t, "a dispatcher that stops after sending, under "+string(guarantee), func(t *testing.T) {
			d := newDispatchEnv(t, factory)
			d.publish("alice")
			d.clock.advance(10 * time.Minute)

			ctx, cancel := context.WithCancel(t.Context())

			d.onSend = func(notify.EmailMessage) { cancel() }

			stopping := d.dispatcher("stopping", notify.WithDeliveryGuarantee(guarantee))
			_, err := stopping.Dispatch(ctx)
			require.NoError(t, err)
			require.Len(t, d.sent(), 1)

			d.onSend = nil
			d.clock.advance(notify.DefaultEmailLease + time.Minute)

			next := d.dispatcher("next", notify.WithDeliveryGuarantee(guarantee))
			result, err := next.Dispatch(t.Context())
			require.NoError(t, err)

			if guarantee == notify.AtMostOnce {
				assert.Equal(t, 1, result.Abandoned)
				assert.Len(t, d.sent(), 1, "never sent again")

				return
			}

			assert.Equal(t, 1, result.Sent)

			messages := d.sent()
			require.Len(t, messages, 2, "sent once more")
			assert.Equal(t, messages[0].IdempotencyKey, messages[1].IdempotencyKey)
			assert.Equal(t, messages[0].NotificationIDs, messages[1].NotificationIDs)

			d.clock.advance(time.Hour)

			settled, err := next.Dispatch(t.Context())
			require.NoError(t, err)
			assert.Zero(t, settled.Claimed)
		})
	}

	parallel(t, "a redeployed dispatcher does not resend", func(t *testing.T) {
		d := newDispatchEnv(t, factory)
		d.publish("alice")
		d.clock.advance(10 * time.Minute)

		before, err := d.dispatcher("before").Dispatch(t.Context())
		require.NoError(t, err)
		require.Equal(t, 1, before.Sent)

		d.clock.advance(notify.DefaultEmailLease + time.Hour)

		after, err := d.dispatcher("after").Dispatch(t.Context())
		require.NoError(t, err)
		assert.Zero(t, after.Claimed)
		assert.Len(t, d.sent(), 1)
	})
}

// dispatchClock is a clock a case moves by hand.
type dispatchClock struct{ now atomic.Pointer[time.Time] }

func (c *dispatchClock) Now() time.Time { return *c.now.Load() }

func (c *dispatchClock) advance(d time.Duration) {
	next := c.Now().Add(d)
	c.now.Store(&next)
}

// contextEmailStore makes a store's email records fail once their context is
// cancelled, as a store talking to a database does, so that a cancelled pass
// behaves identically on every store.
type contextEmailStore struct {
	notify.Store
	notify.EmailStore
}

func (s contextEmailStore) RecordEmails(ctx context.Context, record notify.EmailRecord) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	return s.EmailStore.RecordEmails(ctx, record)
}

// dispatchEnv is one dispatch case's service and the ports it observes.
type dispatchEnv struct {
	t     *testing.T
	clock *dispatchClock
	svc   *notify.Service

	mu       sync.Mutex
	messages []notify.EmailMessage
	errs     []error
	onSend   func(notify.EmailMessage)
	seq      int
}

func newDispatchEnv(t *testing.T, factory EmailFactory) *dispatchEnv {
	t.Helper()

	store := factory(t)
	clock := &dispatchClock{}
	start := base
	clock.now.Store(&start)

	svc, err := notify.New(contextEmailStore{Store: store, EmailStore: store}, notify.WithClock(clock))
	require.NoError(t, err)

	return &dispatchEnv{t: t, clock: clock, svc: svc}
}

// publish publishes a notification for a recipient on a subject of its own.
func (d *dispatchEnv) publish(recipient string) notify.Notification {
	d.t.Helper()

	d.mu.Lock()
	d.seq++
	id := d.seq
	d.mu.Unlock()

	result, err := d.svc.Publish(d.t.Context(), notify.Draft{
		Recipient: recipient, SourceID: fmt.Sprintf("event-%d", id), Subject: fmt.Sprintf("task-%d", id), Kind: "offer",
	})
	require.NoError(d.t, err)
	require.Len(d.t, result.Created, 1)

	return result.Created[0]
}

// dispatcher builds a dispatcher with an owner of its own over the case's
// service.
func (d *dispatchEnv) dispatcher(owner string, opts ...notify.EmailOption) *notify.EmailDispatcher {
	d.t.Helper()

	mailer := notify.MailerFunc(func(_ context.Context, message notify.EmailMessage) error {
		d.mu.Lock()
		d.messages = append(d.messages, message)
		onSend := d.onSend
		d.mu.Unlock()

		if onSend != nil {
			onSend(message)
		}

		return nil
	})

	book := notify.AddressBookFunc(func(_ context.Context, recipient string) (string, bool, error) {
		return recipient + "@example.com", true, nil
	})

	template := notify.EmailTemplateFunc(func(context.Context, notify.EmailBatch) (notify.EmailContent, error) {
		return notify.EmailContent{Subject: "work for you", TextBody: "see the application"}, nil
	})

	opts = append([]notify.EmailOption{
		notify.WithEmailOwner(owner),
		notify.WithEmailErrorHandler(func(_ context.Context, err error) {
			d.mu.Lock()
			defer d.mu.Unlock()

			d.errs = append(d.errs, err)
		}),
	}, opts...)

	dispatcher, err := notify.NewEmailDispatcher(d.svc, mailer, book, template, opts...)
	require.NoError(d.t, err)

	return dispatcher
}

// sent returns the messages sent so far.
func (d *dispatchEnv) sent() []notify.EmailMessage {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.messages)
}

// sentIDs lists every notification identifier a sent message covered.
func (d *dispatchEnv) sentIDs() []string {
	var out []string

	for _, message := range d.sent() {
		out = append(out, message.NotificationIDs...)
	}

	return out
}

// reported returns the errors reported so far.
func (d *dispatchEnv) reported() []error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.errs)
}
