package notify

import (
	"context"
	"time"
)

// The retention defaults a [Pruner] applies when an option does not replace
// them.
const (
	// DefaultMaxPerRecipient is how many notifications each recipient keeps.
	// A user receiving about five a working day keeps roughly five months.
	DefaultMaxPerRecipient = 500
	// DefaultMaxAge is how long a notification is kept after it stopped being
	// ACTIVE, by being read or closed. It never applies to an ACTIVE one.
	DefaultMaxAge = 90 * 24 * time.Hour
	// DefaultWatermarkRetention is how long a subject's close record outlives
	// its last notification. The relay's default retries finish within about
	// half an hour; this covers a host that raised them a hundredfold.
	DefaultWatermarkRetention = 7 * 24 * time.Hour
	// DefaultPruneBatch is the most rows one delete removes, which keeps each
	// transaction short.
	DefaultPruneBatch = 1000
)

// Pruner keeps stored notifications within retention bounds.
//
// Nothing prunes on its own. The host runs a pass once with [Pruner.Prune], in
// a loop with [Pruner.Run], or from its own scheduler. Bounds hold as of the end
// of a pass: between passes a recipient may exceed the count bound and an
// inactive notification may outlive the age bound. Passes on several instances
// at once are safe.
type Pruner struct {
	service *Service
	request PruneRequest
	onError func(ctx context.Context, err error)
}

// PruneOption configures a [Pruner].
type PruneOption func(*pruneConfig)

// pruneConfig is what the options set. The bounds are pointers because an
// explicit zero is a wiring mistake to report, while an absent bound is the
// default.
type pruneConfig struct {
	maxPerRecipient    *int
	withoutCount       bool
	maxAge             *time.Duration
	withoutAge         bool
	strategy           *RetentionStrategy
	watermarkRetention *time.Duration
	batch              *int
	onError            func(ctx context.Context, err error)
}

// WithMaxPerRecipient replaces [DefaultMaxPerRecipient]. The bound applies to
// every recipient alike. It must be positive.
func WithMaxPerRecipient(n int) PruneOption {
	return func(c *pruneConfig) { c.maxPerRecipient = &n }
}

// WithoutMaxPerRecipient removes the count bound. It cannot be combined with
// [WithMaxPerRecipient], or with [WithoutMaxAge].
func WithoutMaxPerRecipient() PruneOption {
	return func(c *pruneConfig) { c.withoutCount = true }
}

// WithMaxAge replaces [DefaultMaxAge]. Age is measured from when a
// notification stopped being ACTIVE, and never deletes an ACTIVE one. It must
// be positive.
func WithMaxAge(age time.Duration) PruneOption {
	return func(c *pruneConfig) { c.maxAge = &age }
}

// WithoutMaxAge removes the age bound. It cannot be combined with
// [WithMaxAge], or with [WithoutMaxPerRecipient].
func WithoutMaxAge() PruneOption {
	return func(c *pruneConfig) { c.withoutAge = true }
}

// WithRetentionStrategy replaces the default strategy, [EvictOldestActive].
// [RetainActive] never deletes an unread notification, at the cost of a
// recipient's ACTIVE notifications being unbounded.
func WithRetentionStrategy(strategy RetentionStrategy) PruneOption {
	return func(c *pruneConfig) { c.strategy = &strategy }
}

// WithWatermarkRetention replaces [DefaultWatermarkRetention]. A source
// redelivered after its subject's close record expired can create
// notifications again. It must be positive.
func WithWatermarkRetention(retention time.Duration) PruneOption {
	return func(c *pruneConfig) { c.watermarkRetention = &retention }
}

// WithPruneBatch replaces [DefaultPruneBatch]. It must be positive.
func WithPruneBatch(n int) PruneOption {
	return func(c *pruneConfig) { c.batch = &n }
}

// WithPruneErrorHandler receives the error of each pass [Pruner.Run] runs. The
// default handler does nothing, which is safe but silent; a host should supply
// one that logs. A nil handler is ignored.
func WithPruneErrorHandler(handler func(ctx context.Context, err error)) PruneOption {
	return func(c *pruneConfig) {
		if handler != nil {
			c.onError = handler
		}
	}
}

// NewPruner builds a pruner over a service's store.
//
// With no options it keeps [DefaultMaxPerRecipient] notifications per
// recipient under [EvictOldestActive], deletes inactive notifications after
// [DefaultMaxAge], expires close records after [DefaultWatermarkRetention], and
// deletes [DefaultPruneBatch] rows at a time.
//
// A nil service, a bound or batch that is not positive, a bound both set and
// removed, both bounds removed, or an unknown strategy is a
// [ConfigurationError].
func NewPruner(svc *Service, opts ...PruneOption) (*Pruner, error) {
	if svc == nil {
		return nil, &ConfigurationError{Detail: "a pruner needs a service"}
	}

	cfg := pruneConfig{onError: func(context.Context, error) {}}

	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	request := PruneRequest{
		MaxPerRecipient:    DefaultMaxPerRecipient,
		MaxAge:             DefaultMaxAge,
		Strategy:           EvictOldestActive,
		WatermarkRetention: DefaultWatermarkRetention,
		Batch:              DefaultPruneBatch,
	}

	switch {
	case cfg.withoutCount:
		request.MaxPerRecipient = 0
	case cfg.maxPerRecipient != nil:
		request.MaxPerRecipient = *cfg.maxPerRecipient
	}

	switch {
	case cfg.withoutAge:
		request.MaxAge = 0
	case cfg.maxAge != nil:
		request.MaxAge = *cfg.maxAge
	}

	if cfg.strategy != nil {
		request.Strategy = *cfg.strategy
	}

	if cfg.watermarkRetention != nil {
		request.WatermarkRetention = *cfg.watermarkRetention
	}

	if cfg.batch != nil {
		request.Batch = *cfg.batch
	}

	return &Pruner{service: svc, request: request, onError: cfg.onError}, nil
}

// validate reports a contradictory or meaningless configuration.
func (c pruneConfig) validate() error {
	refuse := func(detail string) error { return &ConfigurationError{Detail: detail} }

	switch {
	case c.maxPerRecipient != nil && c.withoutCount:
		return refuse("the count bound is both set and removed; choose WithMaxPerRecipient or WithoutMaxPerRecipient")
	case c.maxPerRecipient != nil && *c.maxPerRecipient <= 0:
		return refuse("the count bound must be positive; use WithoutMaxPerRecipient to remove it")
	case c.maxAge != nil && c.withoutAge:
		return refuse("the age bound is both set and removed; choose WithMaxAge or WithoutMaxAge")
	case c.maxAge != nil && *c.maxAge <= 0:
		return refuse("the age bound must be positive; use WithoutMaxAge to remove it")
	case c.withoutCount && c.withoutAge:
		return refuse("a pruner with both bounds removed could never prune anything")
	case c.strategy != nil && !c.strategy.Valid():
		return refuse("the retention strategy " + string(*c.strategy) + " is not EvictOldestActive or RetainActive")
	case c.watermarkRetention != nil && *c.watermarkRetention <= 0:
		return refuse("the watermark retention must be positive")
	case c.batch != nil && *c.batch <= 0:
		return refuse("the prune batch must be positive")
	default:
		return nil
	}
}

// Prune runs one pass and signals every recipient who had an ACTIVE
// notification evicted.
func (p *Pruner) Prune(ctx context.Context) (PruneResult, error) {
	request := p.request
	request.Now = normalizeTime(p.service.clock.Now())

	result, err := p.service.store.Prune(ctx, request)
	if err != nil {
		return result, err
	}

	p.service.signal(ctx, signalsFor(result.Recipients, ChangePruned, request.Now))

	return result, nil
}

// Run prunes every interval until ctx is cancelled, starting with a pass at
// once, and returns ctx's error.
//
// It blocks. The host decides whether that is a goroutine of its own, a
// scheduled job or a command; the library starts nothing by itself. A failed
// pass is reported to the prune error handler and the loop carries on. A
// non-positive interval is a [ConfigurationError].
func (p *Pruner) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return &ConfigurationError{Detail: "a prune interval must be positive"}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := p.Prune(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			p.onError(ctx, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
