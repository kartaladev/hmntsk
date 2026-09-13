package webhook_test

import (
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/delivery/webhook"
)

// deliveryDoc reads the published delivery contract.
func deliveryDoc(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile("../../docs/delivery.md")
	require.NoError(t, err, "docs/delivery.md is the contract a receiver implements against")

	return string(raw)
}

// TestTheDocumentedContractMatchesWhatIsSent keeps docs/delivery.md from
// drifting away from the headers and the signature scheme this package
// actually produces.
//
// A receiver is written against the document, not against the Go source, so a
// document that is wrong is a receiver that rejects every delivery.
func TestTheDocumentedContractMatchesWhatIsSent(t *testing.T) {
	t.Parallel()

	doc := deliveryDoc(t)

	headers := []string{
		webhook.HeaderEventID,
		webhook.HeaderDeliveryID,
		webhook.HeaderEventType,
		webhook.HeaderTaskID,
		webhook.HeaderTaskType,
		webhook.HeaderOwnerType,
		webhook.HeaderOwnerRef,
		webhook.HeaderActivityKey,
		webhook.HeaderTimestamp,
		webhook.HeaderSignature,
	}

	for _, header := range headers {
		assert.Containsf(t, doc, header,
			"every header a delivery carries must be documented: %s", header)
	}

	assert.Contains(t, doc, webhook.SignatureVersion,
		"the signature scheme version must be documented")
	assert.Contains(t, doc, webhook.UserAgent,
		"the user agent must be documented, so a receiver can filter on it")
	assert.Contains(t, doc, webhook.ContentType,
		"the content type must be documented")
}

// TestTheDocumentedDefaultPolicyMatchesTheImplementation is the verification
// task 7.4 asks for: every range the document says the default refuses is a
// range the default actually refuses, checked against the code rather than
// against the prose.
func TestTheDocumentedDefaultPolicyMatchesTheImplementation(t *testing.T) {
	t.Parallel()

	doc := deliveryDoc(t)

	// Each entry is a range the document names and one address inside it. The
	// document is searched for the notation; the policy is asked about the
	// address. Neither can move without the other.
	refused := map[string]string{
		"127.0.0.1":       "127.0.0.1",
		"169.254.169.254": "169.254.169.254",
		"10.0.0.0/8":      "10.1.2.3",
		"172.16.0.0/12":   "172.16.0.1",
		"192.168.0.0/16":  "192.168.1.1",
		"100.64.0.0/10":   "100.64.0.1",
		"0.0.0.0/8":       "0.1.2.3",
		"192.0.0.0/24":    "192.0.0.1",
		"198.18.0.0/15":   "198.18.0.1",
		"255.255.255.255": "255.255.255.255",
		"fc00::/7":        "fd00::1",
		"fe80::/10":       "fe80::1",
		"::1":             "::1",
		"::ffff:":         "::ffff:127.0.0.1",
		"64:ff9b::/96":    "64:ff9b::7f00:1",
		"2002::/16":       "2002:7f00:0001::",
	}

	policy := webhook.DefaultPolicy{}

	for notation, address := range refused {
		t.Run(notation, func(t *testing.T) {
			t.Parallel()

			assert.Containsf(t, doc, notation,
				"the document must say plainly that %s is refused", notation)

			addr, err := netip.ParseAddr(address)
			require.NoError(t, err)

			err = policy.Allow(t.Context(), webhook.Destination{
				Network: "tcp4",
				Host:    "receiver.example",
				IP:      addr,
				Port:    443,
			})
			assert.ErrorIsf(t, err, webhook.ErrDestinationRefused,
				"the default policy must refuse %s, which the document says it does", address)
		})
	}

	t.Run("a public address is allowed", func(t *testing.T) {
		t.Parallel()

		assert.NoError(t, policy.Allow(t.Context(), webhook.Destination{
			Network: "tcp4",
			Host:    "receiver.example",
			IP:      netip.MustParseAddr("93.184.216.34"),
			Port:    443,
		}), "the default refuses internal addresses, not the internet")
	})

	t.Run("the override is documented", func(t *testing.T) {
		t.Parallel()

		assert.Contains(t, doc, "WithDestinationPolicy",
			"a host must be told how to supply its own policy")
		assert.Contains(t, doc, "AllowLoopback",
			"the helper that makes a loopback receiver testable must be documented")
	})
}

// TestTheDocumentedStatusMappingIsComplete asserts the document states what
// every response class means, because that mapping is what decides whether a
// receiver's reply causes a retry or a dead letter.
func TestTheDocumentedStatusMappingIsComplete(t *testing.T) {
	t.Parallel()

	doc := deliveryDoc(t)
	lower := strings.ToLower(doc)

	for _, fragment := range []string{"2xx", "408", "429", "4xx", "5xx"} {
		assert.Containsf(t, doc, fragment, "the status mapping must document %s", fragment)
	}

	for _, verdict := range []string{"delivered", "retryable", "permanent"} {
		assert.Containsf(t, lower, verdict, "the status mapping must name the %s verdict", verdict)
	}
}
