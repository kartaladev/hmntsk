package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/relay"
)

// Sink is a [relay.Sink]; the relay takes it as one and nothing else.
var _ relay.Sink = (*Sink)(nil)

// DefaultName is the name a sink reports to the relay unless the host renames
// it. It is written to the database as part of per-sink acceptance, so renaming
// a sink that has already delivered events re-delivers every one of them.
const DefaultName = "webhook"

// DefaultTimeout bounds one delivery attempt, from dial to the last byte of the
// response. It exists so that one unresponsive receiver cannot hold a relay
// pass open while the rest of the batch waits behind it.
const DefaultTimeout = 10 * time.Second

// UserAgent identifies this sink to a receiver.
const UserAgent = "hmntsk-webhook/1"

// maxResponseSnippet is how much of a failing response is kept in the error.
// The text is written by the receiver and ends up in a database column, so it
// is deliberately short.
const maxResponseSnippet = 256

// maxResponseDrain is how much of a response body is read to let the connection
// be reused. Beyond it the connection is closed instead, because a receiver
// that answers a webhook with a megabyte is not one to keep reading.
const maxResponseDrain = 1 << 20

// Sink delivers events to the callback address the task carried, and is the
// [relay.Sink] a host configures to notify whoever asked for the task.
//
// Everything it needs is on the event: the address, the reference parameters to
// echo and the correlation data to carry. An event whose task had no callback
// address is this sink's business too — it is delivered by having nothing to
// do, rather than being an error the relay has to know about.
//
// A Sink is safe for concurrent use, and holds one connection pool: the host
// constructs one and gives it to the relay.
type Sink struct {
	name    string
	secret  []byte
	timeout time.Duration
	policy  DestinationPolicy
	clock   hmntsk.Clock
	client  *http.Client
}

// Option configures a [Sink] at construction.
type Option func(*Sink)

// WithName renames the sink.
//
// The name is written to the database as part of per-sink acceptance, so it
// must be stable across restarts: renaming a sink makes the relay treat it as
// one that has never accepted anything, and every undelivered event is offered
// to it again.
func WithName(name string) Option {
	return func(s *Sink) { s.name = name }
}

// WithTimeout bounds one delivery attempt. The default is [DefaultTimeout].
func WithTimeout(timeout time.Duration) Option {
	return func(s *Sink) { s.timeout = timeout }
}

// WithDestinationPolicy supplies the policy consulted at dial time, replacing
// [DefaultPolicy].
//
// Supplying one is how a host reaches a receiver the default refuses — a
// sidecar on localhost, a service inside the cluster's private range. It is the
// one knob that can make this sink dangerous, so a policy should say yes to
// named destinations rather than no to known-bad ones.
func WithDestinationPolicy(policy DestinationPolicy) Option {
	return func(s *Sink) {
		if policy != nil {
			s.policy = policy
		}
	}
}

// WithClock supplies the sink's source of time, which is what a delivery is
// timestamped and signed with.
func WithClock(clock hmntsk.Clock) Option {
	return func(s *Sink) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// New builds a sink that signs every delivery with secret.
//
// The secret is required rather than optional. Signing is what tells a receiver
// that a delivery came from this engine, and a sink that could be constructed
// without one would let a deployment discover, in production, that nothing has
// been signing anything.
//
// The returned sink owns an HTTP client whose dialler enforces the destination
// policy, and there is deliberately no option to supply a client of your own:
// a client from outside would dial without the policy, which is the one thing
// this sink must not do.
func New(secret []byte, opts ...Option) (*Sink, error) {
	sink := &Sink{
		name:    DefaultName,
		secret:  append([]byte(nil), secret...),
		timeout: DefaultTimeout,
		policy:  DefaultPolicy{},
		clock:   hmntsk.SystemClock{},
	}

	for _, opt := range opts {
		opt(sink)
	}

	if len(sink.secret) == 0 {
		return nil, &ConfigurationError{Detail: "a signing secret is required; deliveries are always signed"}
	}

	sink.name = strings.TrimSpace(sink.name)
	if sink.name == "" {
		return nil, &ConfigurationError{Detail: "a sink name is required, and is written to the database"}
	}

	if sink.timeout <= 0 {
		return nil, &ConfigurationError{Detail: "the per-attempt timeout must be positive"}
	}

	sink.client = sink.newClient()

	return sink, nil
}

// hostKey carries the callback address's hostname down to the dialler, which is
// given only a resolved address.
type hostKey struct{}

// newClient builds the client every delivery goes through.
//
// Three of its settings are load-bearing rather than tuning. The dialler's
// ControlContext hook runs the destination policy against the address the name
// actually resolved to, which is the only place that check can be made
// truthfully. Proxy is nil, not [http.ProxyFromEnvironment], because a proxy
// would have the sink connect to the proxy — a destination the policy would
// judge instead of the real one, while the address that matters travels in the
// request line. And CheckRedirect refuses every redirect: a redirect is a fresh
// address nobody supplied and nothing has judged.
func (s *Sink) newClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   s.timeout,
		KeepAlive: 30 * time.Second,
		ControlContext: func(ctx context.Context, network, address string, _ syscall.RawConn) error {
			return s.allow(ctx, network, address)
		},
	}

	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("%w: %s answered with a redirect to %s",
				ErrRedirect, via[len(via)-1].URL.Redacted(), req.URL.Redacted())
		},
	}
}

// allow runs the destination policy against one resolved address, and is called
// by the dialler after resolution and before the socket is connected.
func (s *Sink) allow(ctx context.Context, network, address string) error {
	resolved, err := netip.ParseAddrPort(address)
	if err != nil {
		return &DestinationError{
			Network: network,
			Reason:  fmt.Sprintf("the dialler was given %q, which is not an address and port", address),
		}
	}

	host, _ := ctx.Value(hostKey{}).(string)

	destination := Destination{
		Network: network,
		Host:    host,
		IP:      resolved.Addr(),
		Port:    resolved.Port(),
	}

	err = s.policy.Allow(ctx, destination)
	if err == nil {
		return nil
	}

	// Whatever a host's policy returns, a refusal is a refusal: it is wrapped
	// so that it reaches the relay as a permanent failure. Left as the host
	// wrote it, an error saying "not on the allow list" would be
	// indistinguishable from a connection reset, and the relay would spend the
	// event's whole attempt budget re-asking a question already answered.
	if errors.Is(err, ErrDestinationRefused) {
		return err
	}

	return &DestinationError{
		Network: network,
		IP:      destination.IP,
		Port:    destination.Port,
		Reason:  err.Error(),
		Cause:   err,
	}
}

// Name implements [relay.Sink].
func (s *Sink) Name() string { return s.name }

// Deliver implements [relay.Sink]: it POSTs one event to the callback address
// the task carried, and classifies what came back.
//
// An event whose task carried no callback address is delivered without a
// request being made. That is a success, not a skip the relay has to model: the
// event has reached everywhere this sink was ever going to send it.
func (s *Sink) Deliver(ctx context.Context, attempt relay.Attempt) relay.Outcome {
	event := attempt.Event

	if event.Callback == nil || event.Callback.Address == "" {
		return relay.Delivered()
	}

	address := event.Callback.Address

	target, err := parseAddress(address)
	if err != nil {
		return relay.Permanent(err)
	}

	now := s.clock.Now().UTC()
	deliveryID := attempt.DeliveryID

	body, err := newPayload(event, deliveryID, now).MarshalJSON()
	if err != nil {
		// The body cannot be rendered, and rendering it again would fail the
		// same way.
		return relay.Permanent(err)
	}

	request, err := s.newRequest(ctx, target, event, deliveryID, now, body)
	if err != nil {
		return relay.Permanent(err)
	}

	return s.send(request, target)
}

// parseAddress reads a callback address, and refuses one this sink cannot dial.
func parseAddress(address string) (*url.URL, error) {
	target, err := url.Parse(address)
	if err != nil {
		return nil, &AddressError{Address: address, Detail: "it is not a URL", Cause: err}
	}

	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, &AddressError{
			Address: address,
			Detail:  fmt.Sprintf("this sink speaks http and https, not %q", target.Scheme),
		}
	}

	if target.Host == "" {
		return nil, &AddressError{Address: address, Detail: "it names no host"}
	}

	return target, nil
}

// newRequest builds the signed request for one attempt.
func (s *Sink) newRequest(
	ctx context.Context,
	target *url.URL,
	event hmntsk.Event,
	deliveryID string,
	now time.Time,
	body []byte,
) (*http.Request, error) {
	// The hostname travels in the context because the dialler is given only a
	// resolved address, and a policy that keeps a list of permitted names needs
	// to know which name it is judging. It is context for the policy and never
	// the thing judged.
	ctx = context.WithValue(ctx, hostKey{}, target.Hostname())

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &AddressError{Address: target.Redacted(), Detail: "no request can be made to it", Cause: err}
	}

	request.Header.Set("Content-Type", ContentType)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", UserAgent)

	setHeader(request.Header, HeaderEventID, event.ID)
	setHeader(request.Header, HeaderDeliveryID, deliveryID)
	setHeader(request.Header, HeaderEventType, string(event.Type))
	setHeader(request.Header, HeaderTaskID, string(event.TaskID))
	setHeader(request.Header, HeaderTaskType, event.TaskType)
	setHeader(request.Header, HeaderOwnerType, event.Correlation.OwnerType)
	setHeader(request.Header, HeaderOwnerRef, event.Correlation.OwnerRef)
	setHeader(request.Header, HeaderActivityKey, event.Correlation.ActivityKey)

	request.Header.Set(HeaderTimestamp, FormatTimestamp(now))
	request.Header.Set(HeaderSignature, Sign(s.secret, now, body))

	return request, nil
}

// setHeader sets a header when the value can be carried in one.
//
// Correlation data and identifiers are the host's text, and a header cannot
// carry a newline: setting one would either fail the delivery at write time or,
// in a less careful client, smuggle a second header into the request. The body
// carries all of this too, so dropping an unrepresentable one loses nothing a
// receiver cannot recover.
func setHeader(header http.Header, name, value string) {
	if value == "" || !validHeaderValue(value) {
		return
	}

	header.Set(name, value)
}

// validHeaderValue reports whether value can appear as a header field value.
func validHeaderValue(value string) bool {
	for index := range len(value) {
		char := value[index]
		if char == '\t' {
			continue
		}

		if char < ' ' || char == 0x7f {
			return false
		}
	}

	return true
}

// send makes one attempt, bounded by the per-attempt timeout, and classifies
// the result.
func (s *Sink) send(request *http.Request, target *url.URL) relay.Outcome {
	ctx, cancel := context.WithTimeout(request.Context(), s.timeout)
	defer cancel()

	response, err := s.client.Do(request.WithContext(ctx))
	if err != nil {
		return classifyError(target, err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		drain(response.Body)

		return relay.Delivered()
	}

	failure := &StatusError{
		StatusCode: response.StatusCode,
		Address:    target.Redacted(),
		Body:       snippet(response.Body),
	}

	if retryableStatus(response.StatusCode) {
		return relay.Retryable(failure)
	}

	return relay.Permanent(failure)
}

// retryableStatus reports whether a status is worth another attempt.
//
// A 4xx says the request was wrong, and the next attempt would send the same
// request: permanent. The two exceptions say the opposite — 408 is the receiver
// saying it ran out of patience, and 429 is the receiver asking for the same
// request more slowly. Everything else, 5xx included, is the receiver having a
// bad day.
func retryableStatus(status int) bool {
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests {
		return true
	}

	return status < http.StatusBadRequest || status >= http.StatusInternalServerError
}

// classifyError turns a transport failure into an outcome.
func classifyError(target *url.URL, err error) relay.Outcome {
	failure := fmt.Errorf("webhook: post to %s: %w", target.Redacted(), err)

	// A refusal and a redirect are verdicts, not weather: the policy will say
	// the same thing in a minute, and the receiver will answer with the same
	// redirect.
	// A policy refusal is the dialler's error and a refused redirect is the
	// client's, so both arrive wrapped — in a [url.Error] and, for the
	// refusal, a [net.OpError] as well.
	switch {
	case errors.Is(err, ErrDestinationRefused), errors.Is(err, ErrRedirect):
		return relay.Permanent(failure)
	default:
		return relay.Retryable(failure)
	}
}

// drain reads what is left of a response so that the connection can be reused,
// and gives up on one long enough to suggest the receiver is not answering a
// webhook in good faith.
func drain(body io.Reader) { _, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseDrain)) }

// snippet reads the beginning of a failing response, for the error the relay
// records. It is the receiver's own text, so it is truncated and stripped of
// anything that would break a log line.
func snippet(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, maxResponseSnippet))
	drain(body)

	cleaned := strings.Map(func(char rune) rune {
		if char < ' ' || char == 0x7f {
			return ' '
		}

		return char
	}, string(bytes.TrimSpace(raw)))

	return strings.TrimSpace(cleaned)
}
