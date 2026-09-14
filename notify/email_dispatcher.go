package notify

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// The email defaults an [EmailDispatcher] applies when an option does not
// replace them.
const (
	// DefaultEmailGraceDelay is how long a notification must stay ACTIVE before
	// it is emailed, so that one read in the application meanwhile never is.
	DefaultEmailGraceDelay = 5 * time.Minute
	// DefaultEmailMaxLag is the oldest a notification may be and still be
	// emailed, so that enabling email, or resuming after an outage, does not
	// send a backlog.
	DefaultEmailMaxLag = 24 * time.Hour
	// DefaultEmailBatchLimit is the most notifications one message covers.
	DefaultEmailBatchLimit = 20
	// DefaultEmailClaimLimit is the most notifications one pass claims.
	DefaultEmailClaimLimit = 500
	// DefaultEmailLease is how long a pass holds what it claims.
	DefaultEmailLease = 5 * time.Minute
	// DefaultEmailMaxAttempts is how many attempts a delivery gets before it is
	// recorded as failed.
	DefaultEmailMaxAttempts = 5
	// DefaultEmailBackoff is the delay before a first retry, doubling with each
	// attempt.
	DefaultEmailBackoff = time.Minute
	// DefaultEmailBackoffCeiling is the longest delay between attempts.
	DefaultEmailBackoffCeiling = time.Hour
)

// emailJitter is the share of a retry delay that is randomised, so that
// deliveries failing together do not retry in lockstep.
const emailJitter = 0.2

// EmailDispatcher emails ACTIVE notifications to recipients who have not read
// them.
//
// Nothing emails on its own. The host runs a pass once with
// [EmailDispatcher.Dispatch], in a loop with [EmailDispatcher.Run], or from its
// own scheduler. Dispatchers on several instances at once never send the same
// notification twice.
type EmailDispatcher struct {
	service   *Service
	store     EmailStore
	mailer    Mailer
	book      AddressBook
	template  EmailTemplate
	filter    EmailFilter
	guarantee DeliveryGuarantee
	owner     string
	grace     time.Duration
	maxLag    time.Duration
	batch     int
	claim     int
	lease     time.Duration
	attempts  int
	backoff   time.Duration
	ceiling   time.Duration
	onError   func(ctx context.Context, err error)
}

// EmailOption configures an [EmailDispatcher].
type EmailOption func(*emailConfig)

// emailConfig is what the options set. Values are pointers because an explicit
// zero is a wiring mistake to report, while an absent value is the default.
type emailConfig struct {
	grace        *time.Duration
	withoutGrace bool
	maxLag       *time.Duration
	batch        *int
	claim        *int
	lease        *time.Duration
	attempts     *int
	backoff      *time.Duration
	ceiling      *time.Duration
	filter       *EmailFilter
	kinds        *[]string
	guarantee    *DeliveryGuarantee
	owner        *string
	onError      *func(ctx context.Context, err error)
}

// WithEmailGraceDelay replaces [DefaultEmailGraceDelay]. It must be positive;
// [WithoutEmailGraceDelay] removes the delay.
func WithEmailGraceDelay(d time.Duration) EmailOption {
	return func(c *emailConfig) { c.grace = &d }
}

// WithoutEmailGraceDelay removes the grace delay, so that a notification may be
// emailed by the first pass after it is created. It cannot be combined with
// [WithEmailGraceDelay].
func WithoutEmailGraceDelay() EmailOption {
	return func(c *emailConfig) { c.withoutGrace = true }
}

// WithEmailMaxLag replaces [DefaultEmailMaxLag]. It must be longer than the
// grace delay.
func WithEmailMaxLag(d time.Duration) EmailOption {
	return func(c *emailConfig) { c.maxLag = &d }
}

// WithEmailBatchLimit replaces [DefaultEmailBatchLimit]. It must be positive.
func WithEmailBatchLimit(n int) EmailOption {
	return func(c *emailConfig) { c.batch = &n }
}

// WithEmailClaimLimit replaces [DefaultEmailClaimLimit]. It must be positive.
func WithEmailClaimLimit(n int) EmailOption {
	return func(c *emailConfig) { c.claim = &n }
}

// WithEmailLease replaces [DefaultEmailLease]. It must be positive, and should
// outlast one pass: a lease that lapses mid-send puts the send in doubt.
func WithEmailLease(d time.Duration) EmailOption {
	return func(c *emailConfig) { c.lease = &d }
}

// WithEmailMaxAttempts replaces [DefaultEmailMaxAttempts]. It must be positive.
func WithEmailMaxAttempts(n int) EmailOption {
	return func(c *emailConfig) { c.attempts = &n }
}

// WithEmailBackoff replaces [DefaultEmailBackoff] and
// [DefaultEmailBackoffCeiling]: the first retry waits base, each later one twice
// the last, never longer than ceiling, with 20% jitter. base must be positive
// and ceiling no shorter than base.
func WithEmailBackoff(base, ceiling time.Duration) EmailOption {
	return func(c *emailConfig) { c.backoff, c.ceiling = &base, &ceiling }
}

// WithEmailFilter replaces the default, which emails every kind. A notification
// the filter rejects is skipped for good. It cannot be nil or combined with
// [WithEmailKinds].
func WithEmailFilter(filter EmailFilter) EmailOption {
	return func(c *emailConfig) { c.filter = &filter }
}

// WithEmailKinds emails notifications of the named kinds only, through
// [EmailKinds]. It needs at least one kind, and cannot be combined with
// [WithEmailFilter].
func WithEmailKinds(kinds ...string) EmailOption {
	return func(c *emailConfig) { c.kinds = &kinds }
}

// WithDeliveryGuarantee replaces the default, [AtMostOnce].
func WithDeliveryGuarantee(g DeliveryGuarantee) EmailOption {
	return func(c *emailConfig) { c.guarantee = &g }
}

// WithEmailOwner replaces the generated owner, "email-" followed by a UUIDv7,
// that the dispatcher records in its leases. Two dispatchers must never share
// one. It must not be empty.
func WithEmailOwner(owner string) EmailOption {
	return func(c *emailConfig) { c.owner = &owner }
}

// WithEmailErrorHandler receives every error a pass reports: a failed send,
// render, lookup or record. Such an error never stops the pass. The default
// handler does nothing, which is safe but silent; a host should supply one that
// logs. It must not be nil.
func WithEmailErrorHandler(handler func(ctx context.Context, err error)) EmailOption {
	return func(c *emailConfig) { c.onError = &handler }
}

// NewEmailDispatcher builds a dispatcher over a service's store, which must
// record email deliveries.
//
// With no options it waits [DefaultEmailGraceDelay], never emails a
// notification older than [DefaultEmailMaxLag], covers at most
// [DefaultEmailBatchLimit] notifications per message and [DefaultEmailClaimLimit]
// per pass, leases for [DefaultEmailLease], gives up after
// [DefaultEmailMaxAttempts], emails every kind, and delivers [AtMostOnce].
// Constructing a dispatcher starts nothing.
//
// A nil service or port, a store that does not implement [EmailStore], or a
// contradictory or non-positive option is a [ConfigurationError].
func NewEmailDispatcher(
	svc *Service, mailer Mailer, book AddressBook, template EmailTemplate, opts ...EmailOption,
) (*EmailDispatcher, error) {
	refuse := func(detail string) (*EmailDispatcher, error) {
		return nil, &ConfigurationError{Detail: detail}
	}

	switch {
	case svc == nil:
		return refuse("an email dispatcher needs a service")
	case mailer == nil:
		return refuse("an email dispatcher needs a Mailer")
	case book == nil:
		return refuse("an email dispatcher needs an AddressBook")
	case template == nil:
		return refuse("an email dispatcher needs an EmailTemplate")
	}

	store, ok := svc.store.(EmailStore)
	if !ok {
		return refuse("the service's store does not record email deliveries; NewMemoryStore and notify/sqlstore do")
	}

	var cfg emailConfig

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	d := &EmailDispatcher{
		service: svc, store: store, mailer: mailer, book: book, template: template,
		filter:    EmailFilterFunc(func(context.Context, Notification) (bool, error) { return true, nil }),
		guarantee: AtMostOnce,
		grace:     DefaultEmailGraceDelay, maxLag: DefaultEmailMaxLag,
		batch: DefaultEmailBatchLimit, claim: DefaultEmailClaimLimit, lease: DefaultEmailLease,
		attempts: DefaultEmailMaxAttempts, backoff: DefaultEmailBackoff, ceiling: DefaultEmailBackoffCeiling,
		onError: func(context.Context, error) {},
	}

	if err := cfg.apply(d); err != nil {
		return nil, err
	}

	if d.owner == "" {
		id, err := svc.ids.NewID()
		if err != nil {
			return nil, err
		}

		d.owner = "email-" + id
	}

	return d, nil
}

// apply validates the configuration and writes it over the defaults.
func (c emailConfig) apply(d *EmailDispatcher) error {
	if err := c.validate(); err != nil {
		return err
	}

	setDuration(&d.grace, c.grace)
	setDuration(&d.maxLag, c.maxLag)
	setDuration(&d.lease, c.lease)
	setDuration(&d.backoff, c.backoff)
	setDuration(&d.ceiling, c.ceiling)
	setInt(&d.batch, c.batch)
	setInt(&d.claim, c.claim)
	setInt(&d.attempts, c.attempts)

	if c.withoutGrace {
		d.grace = 0
	}

	switch {
	case c.filter != nil:
		d.filter = *c.filter
	case c.kinds != nil:
		d.filter = EmailKinds(*c.kinds...)
	}

	if c.guarantee != nil {
		d.guarantee = *c.guarantee
	}

	if c.owner != nil {
		d.owner = *c.owner
	}

	if c.onError != nil {
		d.onError = *c.onError
	}

	if d.maxLag <= d.grace {
		return &ConfigurationError{Detail: "the email max lag must be longer than the grace delay"}
	}

	return nil
}

// validate reports a contradictory or meaningless configuration.
func (c emailConfig) validate() error {
	refuse := func(detail string) error { return &ConfigurationError{Detail: detail} }

	switch {
	case c.grace != nil && c.withoutGrace:
		return refuse("the email grace delay is both set and removed; choose WithEmailGraceDelay or WithoutEmailGraceDelay")
	case nonPositive(c.grace):
		return refuse("the email grace delay must be positive; use WithoutEmailGraceDelay to remove it")
	case nonPositive(c.maxLag):
		return refuse("the email max lag must be positive")
	case nonPositiveInt(c.batch):
		return refuse("the email batch limit must be positive")
	case nonPositiveInt(c.claim):
		return refuse("the email claim limit must be positive")
	case nonPositive(c.lease):
		return refuse("the email lease must be positive")
	case nonPositiveInt(c.attempts):
		return refuse("the email attempt limit must be positive")
	case nonPositive(c.backoff):
		return refuse("the email backoff must be positive")
	case c.backoff != nil && *c.ceiling < *c.backoff:
		return refuse("the email backoff ceiling must be no shorter than its base")
	case c.filter != nil && c.kinds != nil:
		return refuse("an email filter and email kinds are both set; choose WithEmailFilter or WithEmailKinds")
	case c.filter != nil && *c.filter == nil:
		return refuse("the email filter must not be nil")
	case c.kinds != nil && len(*c.kinds) == 0:
		return refuse("WithEmailKinds needs at least one kind")
	case c.guarantee != nil && !c.guarantee.Valid():
		return refuse("the delivery guarantee " + string(*c.guarantee) + " is not AtMostOnce or AtLeastOnce")
	case c.owner != nil && *c.owner == "":
		return refuse("the email owner must not be empty")
	case c.onError != nil && *c.onError == nil:
		return refuse("the email error handler must not be nil")
	default:
		return nil
	}
}

// nonPositive reports whether a set duration is not positive, which is a mistake.
func nonPositive(d *time.Duration) bool { return d != nil && *d <= 0 }

// nonPositiveInt reports whether a set limit is not positive, which is a mistake.
func nonPositiveInt(n *int) bool { return n != nil && *n <= 0 }

// setDuration overwrites a default with a set value.
func setDuration(dst, src *time.Duration) {
	if src != nil {
		*dst = *src
	}
}

// setInt overwrites a default with a set value.
func setInt(dst, src *int) {
	if src != nil {
		*dst = *src
	}
}

// Owner returns the identity the dispatcher records in its leases.
func (d *EmailDispatcher) Owner() string { return d.owner }

// Dispatch runs one pass: it claims the notifications due for email, resolves
// sends left in doubt by an earlier pass, sends each recipient one message, and
// removes delivery records whose notification was deleted.
//
// It returns an error only when claiming fails. Every later failure is counted
// in the result and reported to the email error handler, and never stops the
// pass.
func (d *EmailDispatcher) Dispatch(ctx context.Context) (DispatchResult, error) {
	now := normalizeTime(d.service.clock.Now())

	candidates, err := d.store.ClaimEmails(ctx, EmailClaim{
		Now: now, Owner: d.owner, Lease: d.lease,
		CreatedUntil: now.Add(-d.grace), CreatedFrom: now.Add(-d.maxLag), Limit: d.claim,
	})
	if err != nil {
		return DispatchResult{}, err
	}

	pass := &emailPass{d: d, ctx: ctx, now: now}
	pass.result.Claimed = len(candidates)

	var (
		doubts      = map[string][]EmailCandidate{}
		batches     []string
		byRecipient = map[string][]EmailCandidate{}
		recipients  []string
	)

	// Candidates arrive oldest first; grouping keeps that order within a group.
	for _, c := range candidates {
		if c.Status == EmailStatusSending {
			if _, seen := doubts[c.BatchID]; !seen {
				batches = append(batches, c.BatchID)
			}

			doubts[c.BatchID] = append(doubts[c.BatchID], c)

			continue
		}

		recipient := c.Notification.Recipient
		if _, seen := byRecipient[recipient]; !seen {
			recipients = append(recipients, recipient)
		}

		byRecipient[recipient] = append(byRecipient[recipient], c)
	}

	for _, batch := range batches {
		pass.resolveDoubt(doubts[batch])
	}

	for _, recipient := range recipients {
		pass.deliver(recipient, byRecipient[recipient])
	}

	purged, err := d.store.PurgeEmailRecords(ctx, d.claim)
	pass.result.Purged = purged

	if err != nil {
		d.onError(ctx, fmt.Errorf("notify: purge email records: %w", err))
	}

	return pass.result, nil
}

// Run dispatches every interval until ctx is cancelled, starting with a pass at
// once, and returns ctx's error.
//
// It blocks. The host decides whether that is a goroutine of its own, a
// scheduled job or a command; the library starts nothing by itself. A failed
// claim is reported to the email error handler and the loop carries on. A
// non-positive interval is a [ConfigurationError].
func (d *EmailDispatcher) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return &ConfigurationError{Detail: "an email dispatch interval must be positive"}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := d.Dispatch(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			d.onError(ctx, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// emailPass is one dispatch pass's state.
type emailPass struct {
	d      *EmailDispatcher
	ctx    context.Context
	now    time.Time
	result DispatchResult
}

// report sends an error, naming the recipient, to the email error handler.
func (p *emailPass) report(recipient, batch string, err error) {
	if batch != "" {
		err = fmt.Errorf("notify: email for %s in batch %s: %w", recipient, batch, err)
	} else {
		err = fmt.Errorf("notify: email for %s: %w", recipient, err)
	}

	p.d.onError(p.ctx, err)
}

// record writes an outcome for candidates and reports whether every one was
// written. A candidate whose lease the pass lost, or a failed write, counts as
// unrecorded.
func (p *emailPass) record(candidates []EmailCandidate, record EmailRecord) bool {
	record.Owner = p.d.owner
	record.At = p.now
	record.IDs = candidateIDs(candidates)

	changed, err := p.d.store.RecordEmails(p.ctx, record)
	if err != nil {
		p.report(candidates[0].Notification.Recipient, record.BatchID, fmt.Errorf("record %s: %w", record.Status, err))
		p.result.Unrecorded += len(candidates)

		return false
	}

	if lost := len(candidates) - int(changed); lost > 0 {
		p.result.Unrecorded += lost

		return false
	}

	return true
}

// candidateIDs lists candidates' notification identifiers.
func candidateIDs(candidates []EmailCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Notification.ID)
	}

	return out
}

// skip records candidates as skipped for good, and counts them.
func (p *emailPass) skip(candidates []EmailCandidate, reason string) {
	if len(candidates) == 0 {
		return
	}

	p.record(candidates, EmailRecord{Status: EmailStatusSkipped, Reason: reason})

	switch reason {
	case EmailSkipNoAddress:
		p.result.SkippedNoAddress += len(candidates)
	case EmailSkipFiltered:
		p.result.SkippedFiltered += len(candidates)
	default:
		p.result.SkippedInactive += len(candidates)
	}
}

// fail records candidates as failed for good, counts them and reports the error.
func (p *emailPass) fail(candidates []EmailCandidate, batch string, cause error) {
	p.record(candidates, EmailRecord{Status: EmailStatusFailed, Reason: cause.Error()})
	p.result.Failed += len(candidates)
	p.report(candidates[0].Notification.Recipient, batch, cause)
}

// retry schedules candidates for another attempt, or fails the ones that used
// up their attempts, and reports the error. countAttempt adds this attempt to
// the record, for a failure that happened before anything was recorded as
// SENDING.
func (p *emailPass) retry(candidates []EmailCandidate, batch string, cause error, countAttempt bool) {
	byAttempts := map[int][]EmailCandidate{}

	var counts []int

	for _, c := range candidates {
		made := c.Attempts
		if countAttempt {
			made++
		}

		if _, seen := byAttempts[made]; !seen {
			counts = append(counts, made)
		}

		byAttempts[made] = append(byAttempts[made], c)
	}

	for _, made := range counts {
		group := byAttempts[made]

		if made >= p.d.attempts {
			p.record(group, EmailRecord{Status: EmailStatusFailed, Reason: cause.Error(), Attempt: countAttempt})
			p.result.Failed += len(group)

			continue
		}

		next := p.now.Add(p.d.delay(made))
		p.record(group, EmailRecord{Status: EmailStatusRetry, Reason: cause.Error(), NextAttemptAt: &next, Attempt: countAttempt})
		p.result.Retried += len(group)
	}

	p.report(candidates[0].Notification.Recipient, batch, cause)
}

// delay is how long to wait after an attempt: the backoff doubled for each
// earlier attempt, capped, with jitter.
func (d *EmailDispatcher) delay(attempts int) time.Duration {
	wait := d.backoff

	for i := 1; i < attempts && wait < d.ceiling; i++ {
		wait *= 2
	}

	wait = min(wait, d.ceiling)

	spread := 1 + emailJitter*(2*rand.Float64()-1)

	return time.Duration(float64(wait) * spread)
}

// resolveDoubt settles a message a previous pass may or may not have sent.
func (p *emailPass) resolveDoubt(candidates []EmailCandidate) {
	batch := candidates[0].BatchID
	recipient := candidates[0].Notification.Recipient

	if p.d.guarantee == AtMostOnce {
		p.record(candidates, EmailRecord{Status: EmailStatusAbandoned, Reason: "the send is in doubt"})
		p.result.Abandoned += len(candidates)

		return
	}

	address, ok, err := p.d.book.AddressOf(p.ctx, recipient)

	switch {
	case err != nil:
		// The record stays SENDING, so the batch keeps its key; the lease lapses
		// and a later pass tries again.
		p.result.Retried += len(candidates)
		p.report(recipient, batch, err)
	case !ok:
		p.skip(candidates, EmailSkipNoAddress)
	default:
		p.send(recipient, address, candidates, batch)
	}
}

// deliver filters one recipient's fresh candidates, looks their address up, and
// sends one message covering at most the batch limit of them, releasing the
// rest to the next pass.
func (p *emailPass) deliver(recipient string, candidates []EmailCandidate) {
	var (
		selected []EmailCandidate
		filtered []EmailCandidate
	)

	for _, c := range candidates {
		ok, err := p.d.filter.ShouldEmail(p.ctx, c.Notification.Clone())

		switch {
		case err != nil:
			p.retry([]EmailCandidate{c}, "", err, true)
		case ok:
			selected = append(selected, c)
		default:
			filtered = append(filtered, c)
		}
	}

	p.skip(filtered, EmailSkipFiltered)

	if len(selected) == 0 {
		return
	}

	address, ok, err := p.d.book.AddressOf(p.ctx, recipient)

	switch {
	case err != nil:
		p.retry(selected, "", err, true)

		return
	case !ok:
		p.skip(selected, EmailSkipNoAddress)

		return
	}

	if len(selected) > p.d.batch {
		p.record(selected[p.d.batch:], EmailRecord{Status: EmailStatusClaimed})
		selected = selected[:p.d.batch]
	}

	p.send(recipient, address, selected, "")
}

// send rechecks candidates, renders and sends one message, and records the
// outcome. batch is the message's key for a resend, and empty for a new
// message, which gets a fresh one.
func (p *emailPass) send(recipient, address string, candidates []EmailCandidate, batch string) {
	live, notifications := p.recheck(recipient, candidates)
	if len(live) == 0 {
		return
	}

	content, err := p.d.template.Render(p.ctx, EmailBatch{Recipient: recipient, Address: address, Notifications: notifications})
	if err != nil {
		p.fail(live, batch, fmt.Errorf("render: %w", err))

		return
	}

	if batch == "" {
		batch, err = p.d.service.ids.NewID()
		if err != nil {
			p.retry(live, "", err, true)

			return
		}
	}

	// Committed before the send, so that a pass that stops mid-send leaves the
	// message in doubt rather than unsent.
	if !p.record(live, EmailRecord{Status: EmailStatusSending, BatchID: batch, Attempt: true}) {
		return
	}

	err = p.d.mailer.Send(p.ctx, EmailMessage{
		To: address, Subject: content.Subject, TextBody: content.TextBody, HTMLBody: content.HTMLBody,
		Headers: content.Headers, IdempotencyKey: batch, Recipient: recipient, NotificationIDs: candidateIDs(live),
	})

	switch {
	case err == nil:
		p.record(live, EmailRecord{Status: EmailStatusSent})
		p.result.Sent += len(live)
		p.result.Messages++
	case errors.Is(err, ErrMailRejected):
		p.fail(live, batch, err)
	case errors.Is(err, ErrMailInDoubt) && p.d.guarantee == AtMostOnce:
		p.record(live, EmailRecord{Status: EmailStatusAbandoned, Reason: err.Error()})
		p.result.Abandoned += len(live)
		p.report(recipient, batch, err)
	case errors.Is(err, ErrMailInDoubt):
		// Left SENDING under this batch, so a later pass resends the same message
		// once the lease lapses.
		p.result.Retried += len(live)
		p.report(recipient, batch, err)
	default:
		// The SENDING record already counted this attempt.
		for i := range live {
			live[i].Attempts++
		}

		p.retry(live, batch, err, false)
	}
}

// recheck reads each candidate again, skipping the ones read, closed or deleted
// since they were claimed, and returns the ones still ACTIVE.
func (p *emailPass) recheck(recipient string, candidates []EmailCandidate) ([]EmailCandidate, []Notification) {
	var (
		live          []EmailCandidate
		notifications []Notification
		inactive      []EmailCandidate
		deleted       []EmailCandidate
	)

	for _, c := range candidates {
		n, err := p.d.service.store.Get(p.ctx, recipient, c.Notification.ID)

		switch {
		case errors.Is(err, ErrNotFound):
			deleted = append(deleted, c)
		case err != nil:
			p.retry([]EmailCandidate{c}, c.BatchID, err, true)
		case n.State != StateActive:
			inactive = append(inactive, c)
		default:
			live = append(live, c)
			notifications = append(notifications, n)
		}
	}

	p.skip(inactive, EmailSkipInactive)
	p.skip(deleted, EmailSkipDeleted)

	return live, notifications
}
