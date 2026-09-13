package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// SignatureVersion labels the scheme the signature header carries, so that a
// second scheme can be introduced without a receiver having to guess which one
// it is looking at. It is the only version this package produces.
const SignatureVersion = "v1"

// DefaultTolerance is how far a delivery's timestamp may be from the receiver's
// clock before [Verifier] calls it stale. It is a compromise between clock
// skew, which is real, and how long a captured delivery stays replayable, which
// is exactly this long.
const DefaultTolerance = 5 * time.Minute

// Sign returns the value of the signature header for one delivery: the scheme
// version, then a lowercase hexadecimal HMAC-SHA256 over the timestamp and the
// body, keyed with the shared secret.
//
// The timestamp is inside the signed material, not merely beside it. Signing
// the body alone would make every captured delivery replayable forever, because
// nothing in a valid capture would ever go out of date; with the timestamp
// signed, a receiver enforcing a freshness window can reject a capture and an
// attacker cannot move the timestamp forward without invalidating the
// signature.
//
// The signed material is the decimal Unix second, a single ".", then the body
// bytes — the separator so that a timestamp and a body cannot be re-split at a
// different point to produce the same bytes.
func Sign(secret []byte, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(FormatTimestamp(timestamp)))
	mac.Write([]byte{'.'})
	mac.Write(body)

	return SignatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

// FormatTimestamp renders an instant the way the timestamp header carries it:
// seconds since the Unix epoch, in decimal. Seconds rather than a formatted
// date because a receiver has to feed exactly these bytes back into its own
// HMAC, and there is only one way to write this one.
func FormatTimestamp(timestamp time.Time) string {
	return strconv.FormatInt(timestamp.Unix(), 10)
}

// ParseTimestamp reads a timestamp header value.
func ParseTimestamp(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q is not a unix timestamp", ErrSignatureMissing, value)
	}

	return time.Unix(seconds, 0).UTC(), nil
}

// Verifier checks a delivery on the receiving side: that it was signed with the
// shared secret, that the body is the one that was signed, and that it is not a
// replay of a delivery captured earlier.
//
// It is the half of the contract this package does not itself run, written here
// because a receiver that implements it by hand tends to compare signatures
// with == and skip the freshness window — the first leaks the expected
// signature a byte at a time, and the second makes any capture valid forever.
//
// A Verifier is safe for concurrent use.
type Verifier struct {
	secret    []byte
	tolerance time.Duration
	clock     hmntsk.Clock
}

// VerifierOption configures a [Verifier] at construction.
type VerifierOption func(*Verifier)

// WithTolerance sets the freshness window: how far a delivery's timestamp may
// be from the receiver's clock, in either direction. The default is
// [DefaultTolerance].
func WithTolerance(tolerance time.Duration) VerifierOption {
	return func(v *Verifier) { v.tolerance = tolerance }
}

// WithVerifierClock supplies the verifier's source of time, which is what makes
// a freshness test deterministic.
func WithVerifierClock(clock hmntsk.Clock) VerifierOption {
	return func(v *Verifier) {
		if clock != nil {
			v.clock = clock
		}
	}
}

// NewVerifier builds a verifier over the secret the sink signs with.
//
// It refuses an empty secret rather than defaulting to one, because a verifier
// keyed with nothing accepts deliveries from anybody who can reach the
// receiver, and would do so silently.
func NewVerifier(secret []byte, opts ...VerifierOption) (*Verifier, error) {
	verifier := &Verifier{
		secret:    append([]byte(nil), secret...),
		tolerance: DefaultTolerance,
		clock:     hmntsk.SystemClock{},
	}

	for _, opt := range opts {
		opt(verifier)
	}

	if len(verifier.secret) == 0 {
		return nil, &ConfigurationError{Detail: "a signing secret is required to verify a delivery"}
	}

	if verifier.tolerance <= 0 {
		return nil, &ConfigurationError{Detail: "the freshness window must be positive"}
	}

	return verifier, nil
}

// Verify reports whether a delivery is authentic and fresh, given the request's
// headers and the body exactly as it arrived — the raw bytes, before any
// decoding, because the signature covers those and not their meaning.
//
// It returns nil when the delivery verifies, and otherwise an error matching
// [ErrSignatureMissing], [ErrSignatureMismatch] or [ErrSignatureStale].
func (v *Verifier) Verify(header http.Header, body []byte) error {
	presented := header.Get(HeaderSignature)
	if presented == "" {
		return fmt.Errorf("%w: no %s header", ErrSignatureMissing, HeaderSignature)
	}

	raw := header.Get(HeaderTimestamp)
	if raw == "" {
		return fmt.Errorf("%w: no %s header", ErrSignatureMissing, HeaderTimestamp)
	}

	timestamp, err := ParseTimestamp(raw)
	if err != nil {
		return err
	}

	if !equalSignature(presented, Sign(v.secret, timestamp, body)) {
		return ErrSignatureMismatch
	}

	if skew := v.clock.Now().Sub(timestamp).Abs(); skew > v.tolerance {
		return fmt.Errorf("%w: signed %s ago, window is %s", ErrSignatureStale, skew, v.tolerance)
	}

	return nil
}

// equalSignature compares two signature header values in constant time.
//
// The comparison is on the decoded digests rather than their hexadecimal text,
// so that a signature written in upper case is judged on what it says and one
// that is not hexadecimal at all is judged false rather than parsed twice. A
// plain == would leak the expected signature: it stops at the first differing
// byte, and the time it takes says where that byte was.
func equalSignature(presented, expected string) bool {
	presentedDigest, ok := decodeSignature(presented)
	if !ok {
		return false
	}

	expectedDigest, ok := decodeSignature(expected)
	if !ok {
		return false
	}

	return hmac.Equal(presentedDigest, expectedDigest)
}

// decodeSignature splits a signature header value into its digest, and reports
// false for a value that is not this package's scheme.
func decodeSignature(value string) ([]byte, bool) {
	encoded, ok := strings.CutPrefix(value, SignatureVersion+"=")
	if !ok {
		return nil, false
	}

	digest, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, false
	}

	return digest, true
}
