package webhook

import (
	"errors"
	"fmt"
	"net/netip"
)

// The sink's error taxonomy. Callers match on these with [errors.Is]; the
// concrete types below carry the detail and satisfy the matching sentinel.
//
// Every one of them is recorded as the outbox entry's last error, so their text
// is read by whoever asks the database why an event never arrived. It names the
// destination and the reason, and never the signing key.
var (
	// ErrConfiguration reports a wiring mistake detected at construction, such
	// as a missing signing secret.
	ErrConfiguration = errors.New("webhook: invalid configuration")

	// ErrAddress reports a callback address this sink cannot deliver to: one
	// that is not a URL, or whose scheme is not http or https.
	ErrAddress = errors.New("webhook: unusable callback address")

	// ErrDestinationRefused reports an address the destination policy refused.
	// It is permanent: another attempt resolves the same name to the same
	// verdict.
	ErrDestinationRefused = errors.New("webhook: destination refused by policy")

	// ErrRedirect reports a receiver that answered with a redirect. Redirects
	// are not followed, because the hop is a fresh address the caller never
	// supplied and the first hop's policy verdict says nothing about it.
	ErrRedirect = errors.New("webhook: redirect not followed")

	// ErrReferenceParameters reports reference parameters that are not JSON,
	// and so cannot be echoed inside a JSON body.
	ErrReferenceParameters = errors.New("webhook: reference parameters are not valid json")

	// ErrStatus reports a response whose status is not a success.
	ErrStatus = errors.New("webhook: unexpected response status")

	// ErrSignatureMissing reports a delivery carrying no signature or no
	// timestamp. It is returned by [Verifier.Verify], never by the sink.
	ErrSignatureMissing = errors.New("webhook: signature or timestamp is missing")

	// ErrSignatureMismatch reports a signature that does not match the body and
	// timestamp presented with it. It is returned by [Verifier.Verify], never
	// by the sink.
	ErrSignatureMismatch = errors.New("webhook: signature does not match")

	// ErrSignatureStale reports a delivery whose timestamp falls outside the
	// verifier's freshness window, which is what makes a captured delivery
	// unusable later. It is returned by [Verifier.Verify], never by the sink.
	ErrSignatureStale = errors.New("webhook: signature timestamp is outside the freshness window")
)

// ConfigurationError reports a sink that cannot be built as asked.
type ConfigurationError struct {
	// Detail says what is wrong, in terms the operator who wired it can act on.
	Detail string
}

// Error implements the error interface.
func (e *ConfigurationError) Error() string {
	return "webhook: invalid configuration: " + e.Detail
}

// Unwrap makes the error match [ErrConfiguration].
func (e *ConfigurationError) Unwrap() error { return ErrConfiguration }

// AddressError reports a callback address the sink cannot deliver to.
type AddressError struct {
	// Address is the address as the task carried it.
	Address string
	// Detail says what is wrong with it.
	Detail string
	// Cause is the parse error, when there was one.
	Cause error
}

// Error implements the error interface.
func (e *AddressError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("webhook: callback address %q is unusable: %s: %v", e.Address, e.Detail, e.Cause)
	}

	return fmt.Sprintf("webhook: callback address %q is unusable: %s", e.Address, e.Detail)
}

// Unwrap makes the error match [ErrAddress] and the underlying parse error.
func (e *AddressError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrAddress}
	}

	return []error{ErrAddress, e.Cause}
}

// DestinationError reports an address the destination policy refused. It names
// the resolved IP rather than only the hostname, because the hostname is the
// part an attacker controls and the IP is what was actually going to be dialled.
type DestinationError struct {
	// Network is the network the connection would have used, "tcp4" or "tcp6".
	Network string
	// IP is the address the destination resolved to.
	IP netip.Addr
	// Port is the port the connection would have used.
	Port uint16
	// Reason says which rule refused it.
	Reason string
	// Cause is a host policy's own error, when the refusal came from one. It is
	// unwrapped alongside [ErrDestinationRefused], so a host can still match on
	// whatever it returned.
	Cause error
}

// Error implements the error interface.
func (e *DestinationError) Error() string {
	return fmt.Sprintf("webhook: destination %s refused by policy: %s", netip.AddrPortFrom(e.IP, e.Port), e.Reason)
}

// Unwrap makes the error match [ErrDestinationRefused], and a host policy's own
// error as well.
func (e *DestinationError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrDestinationRefused}
	}

	return []error{ErrDestinationRefused, e.Cause}
}

// StatusError reports a response the receiver answered with that is not a
// success.
type StatusError struct {
	// StatusCode is the response status.
	StatusCode int
	// Address is the address that answered it.
	Address string
	// Body is the first part of the response body, kept short because it goes
	// into a database column and is written by the receiver, not by us.
	Body string
}

// Error implements the error interface.
func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("webhook: %s answered %d", e.Address, e.StatusCode)
	}

	return fmt.Sprintf("webhook: %s answered %d: %s", e.Address, e.StatusCode, e.Body)
}

// Unwrap makes the error match [ErrStatus].
func (e *StatusError) Unwrap() error { return ErrStatus }
