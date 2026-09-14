package notify_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

func TestMailSentinels(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		err    error
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "a wrapped rejection matches ErrMailRejected and not ErrMailInDoubt",
			err:  fmt.Errorf("provider said 550: %w", notify.ErrMailRejected),
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, notify.ErrMailRejected)
				assert.NotErrorIs(t, err, notify.ErrMailInDoubt)
			},
		},
		{
			name: "a wrapped doubt matches ErrMailInDoubt and not ErrMailRejected",
			err:  fmt.Errorf("timed out after accept: %w", notify.ErrMailInDoubt),
			assert: func(t *testing.T, err error) {
				require.ErrorIs(t, err, notify.ErrMailInDoubt)
				assert.NotErrorIs(t, err, notify.ErrMailRejected)
			},
		},
		{
			name: "any other error matches neither",
			err:  errors.New("connection refused"),
			assert: func(t *testing.T, err error) {
				assert.NotErrorIs(t, err, notify.ErrMailRejected)
				assert.NotErrorIs(t, err, notify.ErrMailInDoubt)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.err)
		})
	}
}

func TestEmailStatusAndSkipReasons(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		status notify.EmailStatus
		assert func(t *testing.T, status notify.EmailStatus)
	}

	valid := func(wire string) func(t *testing.T, status notify.EmailStatus) {
		return func(t *testing.T, status notify.EmailStatus) {
			assert.True(t, status.Valid())
			assert.Equal(t, wire, string(status))
		}
	}

	cases := []testCase{
		{name: "claimed", status: notify.EmailStatusClaimed, assert: valid("CLAIMED")},
		{name: "sending", status: notify.EmailStatusSending, assert: valid("SENDING")},
		{name: "sent", status: notify.EmailStatusSent, assert: valid("SENT")},
		{name: "retry", status: notify.EmailStatusRetry, assert: valid("RETRY")},
		{name: "skipped", status: notify.EmailStatusSkipped, assert: valid("SKIPPED")},
		{name: "failed", status: notify.EmailStatusFailed, assert: valid("FAILED")},
		{name: "abandoned", status: notify.EmailStatusAbandoned, assert: valid("ABANDONED")},
		{
			name:   "an unknown status is not valid",
			status: "PENDING",
			assert: func(t *testing.T, status notify.EmailStatus) { assert.False(t, status.Valid()) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.status)
		})
	}

	assert.Equal(t,
		[]string{"no_address", "filtered", "inactive", "deleted"},
		[]string{notify.EmailSkipNoAddress, notify.EmailSkipFiltered, notify.EmailSkipInactive, notify.EmailSkipDeleted},
	)
}

func TestEmailKinds(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		filter notify.EmailFilter
		kind   string
		assert func(t *testing.T, ok bool, err error)
	}

	cases := []testCase{
		{
			name:   "a listed kind is emailed",
			filter: notify.EmailKinds("offer", "assigned"),
			kind:   "assigned",
			assert: func(t *testing.T, ok bool, err error) {
				require.NoError(t, err)
				assert.True(t, ok)
			},
		},
		{
			name:   "another kind is not",
			filter: notify.EmailKinds("offer", "assigned"),
			kind:   "taken",
			assert: func(t *testing.T, ok bool, err error) {
				require.NoError(t, err)
				assert.False(t, ok)
			},
		},
		{
			name:   "no kinds emails nothing",
			filter: notify.EmailKinds(),
			kind:   "offer",
			assert: func(t *testing.T, ok bool, err error) {
				require.NoError(t, err)
				assert.False(t, ok)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ok, err := tc.filter.ShouldEmail(t.Context(), notify.Notification{Kind: tc.kind})
			tc.assert(t, ok, err)
		})
	}
}

// storeWithoutEmail is a store that records no email deliveries.
type storeWithoutEmail struct{ notify.Store }

// noopPorts are host ports that do nothing.
var (
	noopMailer   = notify.MailerFunc(func(context.Context, notify.EmailMessage) error { return nil })
	noopBook     = notify.AddressBookFunc(func(context.Context, string) (string, bool, error) { return "", false, nil })
	noopTemplate = notify.EmailTemplateFunc(func(context.Context, notify.EmailBatch) (notify.EmailContent, error) {
		return notify.EmailContent{}, nil
	})
)

func TestNewEmailDispatcher(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name        string
		store       notify.Store
		nilSvc      bool
		nilMailer   bool
		nilBook     bool
		nilTemplate bool
		opts        []notify.EmailOption
		assert      func(t *testing.T, d *notify.EmailDispatcher, err error)
	}

	refused := func(t *testing.T, d *notify.EmailDispatcher, err error) {
		t.Helper()

		require.ErrorIs(t, err, notify.ErrConfiguration)
		assert.Nil(t, d)
	}

	cases := []testCase{
		{
			name: "with no options it builds, with a generated owner",
			assert: func(t *testing.T, d *notify.EmailDispatcher, err error) {
				require.NoError(t, err)
				require.NotNil(t, d)
				assert.True(t, strings.HasPrefix(d.Owner(), "email-"), "owner %q", d.Owner())
			},
		},
		{
			name: "every option is accepted together",
			opts: []notify.EmailOption{
				notify.WithEmailGraceDelay(time.Minute), notify.WithEmailMaxLag(time.Hour),
				notify.WithEmailBatchLimit(5), notify.WithEmailClaimLimit(50), notify.WithEmailLease(time.Minute),
				notify.WithEmailMaxAttempts(3), notify.WithEmailBackoff(time.Second, time.Minute),
				notify.WithEmailKinds("offer"), notify.WithDeliveryGuarantee(notify.AtLeastOnce),
				notify.WithEmailOwner("dispatcher-1"),
				notify.WithEmailErrorHandler(func(context.Context, error) {}),
			},
			assert: func(t *testing.T, d *notify.EmailDispatcher, err error) {
				require.NoError(t, err)
				assert.Equal(t, "dispatcher-1", d.Owner())
			},
		},
		{
			name: "no grace delay and a lag is accepted",
			opts: []notify.EmailOption{notify.WithoutEmailGraceDelay(), notify.WithEmailMaxLag(time.Second)},
			assert: func(t *testing.T, d *notify.EmailDispatcher, err error) {
				require.NoError(t, err)
				assert.NotNil(t, d)
			},
		},
		{name: "a nil service", nilSvc: true, assert: refused},
		{name: "a nil mailer", nilMailer: true, assert: refused},
		{name: "a nil address book", nilBook: true, assert: refused},
		{name: "a nil template", nilTemplate: true, assert: refused},
		{name: "a store that records no email deliveries", store: storeWithoutEmail{notify.NewMemoryStore()}, assert: refused},
		{name: "a zero grace delay", opts: []notify.EmailOption{notify.WithEmailGraceDelay(0)}, assert: refused},
		{name: "a negative grace delay", opts: []notify.EmailOption{notify.WithEmailGraceDelay(-time.Minute)}, assert: refused},
		{
			name:   "a grace delay both set and removed",
			opts:   []notify.EmailOption{notify.WithEmailGraceDelay(time.Minute), notify.WithoutEmailGraceDelay()},
			assert: refused,
		},
		{name: "a zero max lag", opts: []notify.EmailOption{notify.WithEmailMaxLag(0)}, assert: refused},
		{
			name:   "a max lag no longer than the grace delay",
			opts:   []notify.EmailOption{notify.WithEmailGraceDelay(time.Hour), notify.WithEmailMaxLag(time.Hour)},
			assert: refused,
		},
		{name: "a zero batch limit", opts: []notify.EmailOption{notify.WithEmailBatchLimit(0)}, assert: refused},
		{name: "a negative claim limit", opts: []notify.EmailOption{notify.WithEmailClaimLimit(-1)}, assert: refused},
		{name: "a zero lease", opts: []notify.EmailOption{notify.WithEmailLease(0)}, assert: refused},
		{name: "zero attempts", opts: []notify.EmailOption{notify.WithEmailMaxAttempts(0)}, assert: refused},
		{name: "a zero backoff base", opts: []notify.EmailOption{notify.WithEmailBackoff(0, time.Minute)}, assert: refused},
		{
			name:   "a backoff ceiling below its base",
			opts:   []notify.EmailOption{notify.WithEmailBackoff(time.Minute, time.Second)},
			assert: refused,
		},
		{name: "a nil filter", opts: []notify.EmailOption{notify.WithEmailFilter(nil)}, assert: refused},
		{name: "no kinds", opts: []notify.EmailOption{notify.WithEmailKinds()}, assert: refused},
		{
			name:   "a filter and kinds together",
			opts:   []notify.EmailOption{notify.WithEmailFilter(notify.EmailKinds("offer")), notify.WithEmailKinds("offer")},
			assert: refused,
		},
		{name: "an unknown guarantee", opts: []notify.EmailOption{notify.WithDeliveryGuarantee("EXACTLY_ONCE")}, assert: refused},
		{name: "an empty owner", opts: []notify.EmailOption{notify.WithEmailOwner("")}, assert: refused},
		{name: "a nil error handler", opts: []notify.EmailOption{notify.WithEmailErrorHandler(nil)}, assert: refused},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := tc.store
			if store == nil {
				store = notify.NewMemoryStore()
			}

			var svc *notify.Service

			if !tc.nilSvc {
				var err error

				svc, err = notify.New(store)
				require.NoError(t, err)
			}

			mailer, book, template := notify.Mailer(noopMailer), notify.AddressBook(noopBook), notify.EmailTemplate(noopTemplate)

			if tc.nilMailer {
				mailer = nil
			}

			if tc.nilBook {
				book = nil
			}

			if tc.nilTemplate {
				template = nil
			}

			d, err := notify.NewEmailDispatcher(svc, mailer, book, template, tc.opts...)
			tc.assert(t, d, err)
		})
	}
}
