// Email delivery: the store port, the host ports and the values that cross them.
//
//go:generate mockgen -source=email.go -package=notify -destination=email_mock_test.go -typed

package notify

import (
	"context"
	"errors"
	"slices"
	"time"
)

// The sentinels a [Mailer] returns to classify a failed send. Any other error
// means the message was not sent.
var (
	// ErrMailRejected reports a message the sender refused for good, such as an
	// address the provider will never accept. It is not retried.
	ErrMailRejected = errors.New("notify: mail rejected")
	// ErrMailInDoubt reports a send the sender cannot say happened or not, such
	// as a timeout after the provider accepted the request.
	ErrMailInDoubt = errors.New("notify: mail in doubt")
)

// EmailStatus is where one notification's email delivery stands.
type EmailStatus string

// The delivery statuses.
const (
	// EmailStatusClaimed is a delivery a dispatcher holds a lease on and has not
	// attempted yet.
	EmailStatusClaimed EmailStatus = "CLAIMED"
	// EmailStatusSending is a delivery whose message was handed to the sender
	// and whose outcome is not recorded yet. Found with an expired lease, it is
	// in doubt.
	EmailStatusSending EmailStatus = "SENDING"
	// EmailStatusSent is a delivery the sender accepted. It is never attempted
	// again.
	EmailStatusSent EmailStatus = "SENT"
	// EmailStatusRetry is a delivery that failed and waits for its next attempt.
	EmailStatusRetry EmailStatus = "RETRY"
	// EmailStatusSkipped is a notification that is not emailed, for good. The
	// reason is one of the EmailSkip constants.
	EmailStatusSkipped EmailStatus = "SKIPPED"
	// EmailStatusFailed is a delivery that failed permanently, or used up its
	// attempts. It is never attempted again.
	EmailStatusFailed EmailStatus = "FAILED"
	// EmailStatusAbandoned is an in-doubt delivery that is not repeated, under
	// [AtMostOnce]. It is never attempted again.
	EmailStatusAbandoned EmailStatus = "ABANDONED"
)

// Valid reports whether s is one of the defined statuses.
func (s EmailStatus) Valid() bool {
	switch s {
	case EmailStatusClaimed, EmailStatusSending, EmailStatusSent, EmailStatusRetry,
		EmailStatusSkipped, EmailStatusFailed, EmailStatusAbandoned:
		return true
	default:
		return false
	}
}

// The reasons a notification is skipped. A skip is final: a skipped notification
// is never reconsidered.
const (
	// EmailSkipNoAddress is a recipient the host's address book has no address
	// for.
	EmailSkipNoAddress = "no_address"
	// EmailSkipFiltered is a notification the email filter rejected.
	EmailSkipFiltered = "filtered"
	// EmailSkipInactive is a notification read or closed after it was claimed.
	EmailSkipInactive = "inactive"
	// EmailSkipDeleted is a notification deleted after it was claimed.
	EmailSkipDeleted = "deleted"
)

// EmailStore records email delivery state, one row per notification.
//
// [NewMemoryStore] and notify/sqlstore implement it; a host store may too,
// provided it passes notifytest.RunEmail. Every method is its own transaction.
type EmailStore interface {
	// ClaimEmails takes a lease on notifications due for email and returns them.
	// A notification another claim holds is never returned, so two dispatchers
	// never receive the same one.
	ClaimEmails(ctx context.Context, claim EmailClaim) ([]EmailCandidate, error)
	// RecordEmails writes an outcome for the claim's notifications, and returns
	// how many it changed. A notification whose lease the owner no longer holds
	// is not changed.
	RecordEmails(ctx context.Context, record EmailRecord) (int64, error)
	// PurgeEmailRecords deletes up to limit delivery records whose notification
	// no longer exists, and returns how many it deleted.
	PurgeEmailRecords(ctx context.Context, limit int) (int64, error)
}

// EmailClaim asks for notifications due for email.
type EmailClaim struct {
	// Now is the instant leases, retries and the window are measured from.
	Now time.Time
	// Owner identifies the dispatcher, and is recorded in the lease.
	Owner string
	// Lease is how long the claim holds.
	Lease time.Duration
	// CreatedUntil is the newest creation time claimed: now minus the grace
	// delay.
	CreatedUntil time.Time
	// CreatedFrom is the oldest creation time claimed: now minus the maximum
	// lag.
	CreatedFrom time.Time
	// Limit caps how many notifications one claim returns.
	Limit int
}

// EmailCandidate is one claimed notification.
type EmailCandidate struct {
	// Notification is the notification as it was when claimed.
	Notification Notification
	// Status is [EmailStatusClaimed], or [EmailStatusSending] for a send in doubt
	// from an earlier attempt.
	Status EmailStatus
	// BatchID is the message the notification was last attempted in, empty when
	// it never was. It is the message's idempotency key.
	BatchID string
	// Attempts counts the attempts made so far.
	Attempts int
}

// EmailRecord is an outcome to write for some of a claim's notifications.
type EmailRecord struct {
	// Owner is the dispatcher that holds the claim.
	Owner string
	// IDs are the notifications the outcome is for.
	IDs []string
	// Status is the outcome. [EmailStatusSending] keeps the lease;
	// [EmailStatusClaimed] releases the notifications to the next claim; every
	// other status releases them too.
	Status EmailStatus
	// BatchID, when set, replaces the recorded message.
	BatchID string
	// Reason is a skip reason or the last error.
	Reason string
	// NextAttemptAt is when a [EmailStatusRetry] delivery is due.
	NextAttemptAt *time.Time
	// Attempt counts one more attempt.
	Attempt bool
	// At is when the outcome happened.
	At time.Time
}

// AddressBook finds a recipient's email address. It belongs to the host.
type AddressBook interface {
	// AddressOf returns ok=false when the recipient has no address; that is not
	// an error.
	AddressOf(ctx context.Context, recipient string) (address string, ok bool, err error)
}

// AddressBookFunc adapts a function to the [AddressBook] interface.
type AddressBookFunc func(ctx context.Context, recipient string) (string, bool, error)

// AddressOf implements [AddressBook].
func (f AddressBookFunc) AddressOf(ctx context.Context, recipient string) (address string, ok bool, err error) {
	return f(ctx, recipient)
}

// EmailBatch is one recipient's notifications to render into one message,
// oldest first.
type EmailBatch struct {
	// Recipient is who the message is for.
	Recipient string
	// Address is where it goes.
	Address string
	// Notifications are the notifications it covers.
	Notifications []Notification
}

// EmailContent is a rendered message.
type EmailContent struct {
	// Subject is the message's subject line.
	Subject string
	// TextBody is the plain-text body.
	TextBody string
	// HTMLBody is the HTML body, empty for a text-only message.
	HTMLBody string
	// Headers are extra headers.
	Headers map[string]string
}

// EmailTemplate renders a batch into a message. It belongs to the host.
type EmailTemplate interface {
	// Render renders a batch. An error is permanent: the notifications are
	// recorded as failed and not retried.
	Render(ctx context.Context, batch EmailBatch) (EmailContent, error)
}

// EmailTemplateFunc adapts a function to the [EmailTemplate] interface.
type EmailTemplateFunc func(ctx context.Context, batch EmailBatch) (EmailContent, error)

// Render implements [EmailTemplate].
func (f EmailTemplateFunc) Render(ctx context.Context, batch EmailBatch) (EmailContent, error) {
	return f(ctx, batch)
}

// EmailMessage is a message to send.
type EmailMessage struct {
	// To is the recipient's address.
	To string
	// Subject is the subject line.
	Subject string
	// TextBody is the plain-text body.
	TextBody string
	// HTMLBody is the HTML body.
	HTMLBody string
	// Headers are extra headers.
	Headers map[string]string
	// IdempotencyKey identifies the message, and is the same for every attempt
	// of it. A sender that deduplicates on it delivers a repeat once.
	IdempotencyKey string
	// Recipient is who the message is for.
	Recipient string
	// NotificationIDs are the notifications the message covers.
	NotificationIDs []string
}

// Mailer sends messages. It belongs to the host.
type Mailer interface {
	// Send returns nil when the message was sent, an error matching
	// [ErrMailRejected] for a permanent refusal, one matching [ErrMailInDoubt]
	// when it cannot tell, and any other error when the message was not sent.
	Send(ctx context.Context, message EmailMessage) error
}

// MailerFunc adapts a function to the [Mailer] interface.
type MailerFunc func(ctx context.Context, message EmailMessage) error

// Send implements [Mailer].
func (f MailerFunc) Send(ctx context.Context, message EmailMessage) error { return f(ctx, message) }

// EmailFilter decides which notifications are emailed. It is where a host's
// preferences, quiet hours and unsubscribes plug in.
type EmailFilter interface {
	// ShouldEmail reports whether a notification is emailed. A rejected
	// notification is skipped for good; an error is retried.
	ShouldEmail(ctx context.Context, n Notification) (bool, error)
}

// EmailFilterFunc adapts a function to the [EmailFilter] interface.
type EmailFilterFunc func(ctx context.Context, n Notification) (bool, error)

// ShouldEmail implements [EmailFilter].
func (f EmailFilterFunc) ShouldEmail(ctx context.Context, n Notification) (bool, error) {
	return f(ctx, n)
}

// EmailKinds is a filter that emails notifications of the named kinds only. With
// no kinds it emails nothing; [WithEmailKinds] refuses that at construction.
func EmailKinds(kinds ...string) EmailFilter {
	kinds = slices.Clone(kinds)

	return EmailFilterFunc(func(_ context.Context, n Notification) (bool, error) {
		return slices.Contains(kinds, n.Kind), nil
	})
}

// DeliveryGuarantee decides what happens to a send in doubt.
type DeliveryGuarantee string

// The delivery guarantees.
const (
	// AtMostOnce is the default. A send in doubt is recorded as abandoned and
	// never repeated, so an email can be lost but is never duplicated.
	AtMostOnce DeliveryGuarantee = "AT_MOST_ONCE"
	// AtLeastOnce repeats a send in doubt with the same idempotency key over
	// exactly the same notifications, so an email is duplicated unless the sender
	// honours the key.
	AtLeastOnce DeliveryGuarantee = "AT_LEAST_ONCE"
)

// Valid reports whether g is one of the defined guarantees.
func (g DeliveryGuarantee) Valid() bool { return g == AtMostOnce || g == AtLeastOnce }

// DispatchResult is what one dispatch pass did. Counts are per notification,
// except Messages.
type DispatchResult struct {
	// Claimed counts the notifications the pass claimed.
	Claimed int
	// Sent counts the notifications covered by a message the sender accepted.
	Sent int
	// Messages counts the messages the sender accepted.
	Messages int
	// SkippedNoAddress counts notifications whose recipient has no address.
	SkippedNoAddress int
	// SkippedFiltered counts notifications the filter rejected.
	SkippedFiltered int
	// SkippedInactive counts notifications read, closed or deleted after they
	// were claimed.
	SkippedInactive int
	// Retried counts notifications scheduled for another attempt.
	Retried int
	// Failed counts notifications that failed permanently or used up their
	// attempts.
	Failed int
	// Abandoned counts notifications in doubt that are not repeated.
	Abandoned int
	// Purged counts delivery records removed because their notification was
	// deleted.
	Purged int64
	// Unrecorded counts notifications whose outcome could not be written because
	// the pass no longer held their lease.
	Unrecorded int
}
