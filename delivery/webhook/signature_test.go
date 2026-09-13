package webhook_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/delivery/webhook"
)

// signedAt is the instant every signature test signs at.
var signedAt = time.Date(2026, 9, 13, 10, 30, 0, 0, time.UTC)

func TestSign(t *testing.T) {
	t.Parallel()

	body := []byte(`{"deliveryId":"d-1"}`)

	type testCase struct {
		name   string
		assert func(t *testing.T, signature string)
	}

	cases := []testCase{
		{
			name: "the same inputs sign the same way",
			assert: func(t *testing.T, signature string) {
				assert.Equal(t, webhook.Sign(testSecret, signedAt, body), signature)
			},
		},
		{
			name: "the signature is versioned",
			assert: func(t *testing.T, signature string) {
				assert.Regexp(t, `^v1=[0-9a-f]{64}$`, signature)
			},
		},
		{
			name: "a different timestamp signs differently",
			assert: func(t *testing.T, signature string) {
				assert.NotEqual(t, webhook.Sign(testSecret, signedAt.Add(time.Second), body), signature)
			},
		},
		{
			name: "a different body signs differently",
			assert: func(t *testing.T, signature string) {
				assert.NotEqual(t, webhook.Sign(testSecret, signedAt, []byte(`{"deliveryId":"d-2"}`)), signature)
			},
		},
		{
			name: "a different secret signs differently",
			assert: func(t *testing.T, signature string) {
				assert.NotEqual(t, webhook.Sign([]byte("another-secret"), signedAt, body), signature)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, webhook.Sign(testSecret, signedAt, body))
		})
	}
}

func TestVerifierVerify(t *testing.T) {
	t.Parallel()

	body := []byte(`{"deliveryId":"d-1","event":{"id":"e-1"}}`)

	// delivered builds the headers a receiver would see for body, signed at
	// signedAt with the shared secret.
	delivered := func() http.Header {
		header := http.Header{}
		header.Set(webhook.HeaderTimestamp, webhook.FormatTimestamp(signedAt))
		header.Set(webhook.HeaderSignature, webhook.Sign(testSecret, signedAt, body))

		return header
	}

	type testCase struct {
		name   string
		header func() http.Header
		body   []byte
		now    time.Time
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name:   "a delivery verifies",
			header: delivered,
			body:   body,
			now:    signedAt.Add(time.Second),
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name:   "a tampered body does not verify",
			header: delivered,
			body:   []byte(`{"deliveryId":"d-1","event":{"id":"e-2"}}`),
			now:    signedAt,
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMismatch)
			},
		},
		{
			name: "a tampered timestamp does not verify, because the timestamp is signed",
			header: func() http.Header {
				header := delivered()
				header.Set(webhook.HeaderTimestamp, webhook.FormatTimestamp(signedAt.Add(time.Minute)))

				return header
			},
			body: body,
			now:  signedAt.Add(time.Minute),
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMismatch)
			},
		},
		{
			name: "another key does not verify",
			header: func() http.Header {
				header := http.Header{}
				header.Set(webhook.HeaderTimestamp, webhook.FormatTimestamp(signedAt))
				header.Set(webhook.HeaderSignature, webhook.Sign([]byte("another-secret"), signedAt, body))

				return header
			},
			body: body,
			now:  signedAt,
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMismatch)
			},
		},
		{
			name:   "a replayed delivery is stale once the window has passed",
			header: delivered,
			body:   body,
			now:    signedAt.Add(10 * time.Minute),
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureStale)
			},
		},
		{
			name:   "a delivery from the future beyond the window is stale",
			header: delivered,
			body:   body,
			now:    signedAt.Add(-10 * time.Minute),
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureStale)
			},
		},
		{
			name: "a missing signature is refused",
			header: func() http.Header {
				header := delivered()
				header.Del(webhook.HeaderSignature)

				return header
			},
			body: body,
			now:  signedAt,
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMissing)
			},
		},
		{
			name: "a missing timestamp is refused",
			header: func() http.Header {
				header := delivered()
				header.Del(webhook.HeaderTimestamp)

				return header
			},
			body: body,
			now:  signedAt,
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMissing)
			},
		},
		{
			name: "an unreadable timestamp is refused",
			header: func() http.Header {
				header := delivered()
				header.Set(webhook.HeaderTimestamp, "the day before yesterday")

				return header
			},
			body: body,
			now:  signedAt,
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMissing)
			},
		},
		{
			name: "a signature in an unknown version is refused",
			header: func() http.Header {
				header := delivered()
				header.Set(webhook.HeaderSignature, "v9=deadbeef")

				return header
			},
			body: body,
			now:  signedAt,
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMismatch)
			},
		},
		{
			name: "a signature that is not hexadecimal is refused",
			header: func() http.Header {
				header := delivered()
				header.Set(webhook.HeaderSignature, "v1=not-hexadecimal")

				return header
			},
			body: body,
			now:  signedAt,
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrSignatureMismatch)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			verifier, err := webhook.NewVerifier(
				testSecret,
				webhook.WithTolerance(5*time.Minute),
				webhook.WithVerifierClock(hmntsk.ClockFunc(func() time.Time { return tc.now })),
			)
			require.NoError(t, err)

			tc.assert(t, verifier.Verify(tc.header(), tc.body))
		})
	}
}

func TestNewVerifier(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		secret []byte
		opts   []webhook.VerifierOption
		assert func(t *testing.T, verifier *webhook.Verifier, err error)
	}

	cases := []testCase{
		{
			name:   "a secret is enough",
			secret: testSecret,
			assert: func(t *testing.T, verifier *webhook.Verifier, err error) {
				require.NoError(t, err)
				assert.NotNil(t, verifier)
			},
		},
		{
			name:   "an empty secret is refused",
			secret: nil,
			assert: func(t *testing.T, verifier *webhook.Verifier, err error) {
				assert.Nil(t, verifier)
				assert.ErrorIs(t, err, webhook.ErrConfiguration)
			},
		},
		{
			name:   "a non-positive tolerance is refused",
			secret: testSecret,
			opts:   []webhook.VerifierOption{webhook.WithTolerance(0)},
			assert: func(t *testing.T, verifier *webhook.Verifier, err error) {
				assert.Nil(t, verifier)
				assert.ErrorIs(t, err, webhook.ErrConfiguration)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			verifier, err := webhook.NewVerifier(tc.secret, tc.opts...)
			tc.assert(t, verifier, err)
		})
	}
}
