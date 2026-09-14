package notify_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// ExampleNewEmailDispatcher wires email as docs/email.md describes: the host's
// address book, template and mailer, narrowed to the kinds that ask someone to
// act.
func ExampleNewEmailDispatcher() {
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	now := start

	svc, _ := notify.New(notify.NewMemoryStore(), notify.WithClock(notify.ClockFunc(func() time.Time { return now })))

	addressBook := notify.AddressBookFunc(func(_ context.Context, recipient string) (string, bool, error) {
		return recipient + "@example.com", true, nil
	})
	template := notify.EmailTemplateFunc(func(_ context.Context, batch notify.EmailBatch) (notify.EmailContent, error) {
		return notify.EmailContent{Subject: fmt.Sprintf("%d tasks need you", len(batch.Notifications))}, nil
	})
	mailer := notify.MailerFunc(func(_ context.Context, message notify.EmailMessage) error {
		fmt.Println(message.To, "-", message.Subject)

		return nil
	})

	dispatcher, err := notify.NewEmailDispatcher(svc, mailer, addressBook, template,
		notify.WithEmailKinds("offer", "assigned"))
	if err != nil {
		fmt.Println(err)

		return
	}

	ctx := context.Background()

	_, _ = svc.Publish(ctx,
		notify.Draft{Recipient: "alice", SourceID: "event-1", Subject: "task-1", Kind: "offer"},
		notify.Draft{Recipient: "alice", SourceID: "event-2", Subject: "task-2", Kind: "assigned"},
		notify.Draft{Recipient: "alice", SourceID: "event-3", Subject: "task-3", Kind: "taken"},
	)

	now = start.Add(notify.DefaultEmailGraceDelay + time.Minute)

	result, _ := dispatcher.Dispatch(ctx)
	fmt.Println("sent", result.Sent, "skipped", result.SkippedFiltered)

	// Output:
	// alice@example.com - 2 tasks need you
	// sent 2 skipped 1
}

// TestTheEmailDocumentMatchesTheImplementation keeps docs/email.md from drifting
// away from the code: every default it states must be the value the code
// applies, every option it names must exist, and every stated limit must be
// there.
func TestTheEmailDocumentMatchesTheImplementation(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("docs/email.md")
	require.NoError(t, err, "docs/email.md is where email is documented")

	document := "\n" + string(raw)

	type testCase struct {
		name    string
		section string
		needles []string
	}

	cases := []testCase{
		{
			name:    "wiring and ports",
			section: "Wiring",
			needles: []string{
				funcName(notify.NewEmailDispatcher), "AddressBook", "EmailTemplate", "Mailer", "EmailFilter",
				"ErrMailRejected", "ErrMailInDoubt", "EmailSchema", "VerifyEmailSchema", "ddl/email",
			},
		},
		{
			name:    "defaults and options",
			section: "Defaults and overrides",
			needles: []string{
				"5 minutes", "24 hours", strconv.Itoa(notify.DefaultEmailBatchLimit), strconv.Itoa(notify.DefaultEmailClaimLimit),
				strconv.Itoa(notify.DefaultEmailMaxAttempts), "1 minute", "1 hour", "20%",
				funcName(notify.WithEmailGraceDelay), funcName(notify.WithoutEmailGraceDelay), funcName(notify.WithEmailMaxLag),
				funcName(notify.WithEmailBatchLimit), funcName(notify.WithEmailClaimLimit), funcName(notify.WithEmailLease),
				funcName(notify.WithEmailMaxAttempts), funcName(notify.WithEmailBackoff), funcName(notify.WithEmailKinds),
				funcName(notify.WithEmailFilter), funcName(notify.WithDeliveryGuarantee), funcName(notify.WithEmailOwner),
				funcName(notify.WithEmailErrorHandler), "AtMostOnce",
			},
		},
		{
			name:    "both delivery guarantees",
			section: "Delivery guarantees",
			needles: []string{"AtMostOnce", "AtLeastOnce", "IdempotencyKey", "ABANDONED", "never merged"},
		},
		{
			name:    "the stated limits",
			section: "Stated limits",
			needles: []string{"loses that message's email", "twice", "between the recheck and the send", "skip is final"},
		},
	}

	assert.Equal(t, 5*time.Minute, notify.DefaultEmailGraceDelay)
	assert.Equal(t, 24*time.Hour, notify.DefaultEmailMaxLag)
	assert.Equal(t, 5*time.Minute, notify.DefaultEmailLease)
	assert.Equal(t, time.Minute, notify.DefaultEmailBackoff)
	assert.Equal(t, time.Hour, notify.DefaultEmailBackoffCeiling)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			section := docSection(t, document, tc.section)

			for _, needle := range tc.needles {
				assert.Containsf(t, section, needle, "the %q section mentions %s", tc.section, needle)
			}
		})
	}
}
