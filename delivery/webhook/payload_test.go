package webhook_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/delivery/webhook"
)

func TestPayloadMarshalJSON(t *testing.T) {
	t.Parallel()

	base := webhook.Payload{
		DeliveryID:  "01920000-0000-7000-8000-00000000000d",
		DeliveredAt: signedAt,
		Event: webhook.PayloadEvent{
			ID:         "01920000-0000-7000-8000-000000000001",
			Type:       hmntsk.EventTypeCompleted,
			TaskID:     "task-1",
			TaskType:   "approval",
			Status:     hmntsk.StatusCompleted,
			Version:    3,
			OccurredAt: signedAt.Add(-time.Minute),
		},
	}

	type testCase struct {
		name       string
		parameters string
		assert     func(t *testing.T, body []byte, err error)
	}

	// renders asserts that the body was produced at all, is valid JSON and
	// decodes again, then hands it to the case's own assertion.
	renders := func(check func(t *testing.T, body []byte)) func(t *testing.T, body []byte, err error) {
		return func(t *testing.T, body []byte, err error) {
			t.Helper()

			require.NoError(t, err)
			require.True(t, json.Valid(body), "the body must be valid json: %s", body)

			var decoded webhook.Payload
			require.NoError(t, json.Unmarshal(body, &decoded), "a receiver must be able to decode it")
			assert.Equal(t, base.DeliveryID, decoded.DeliveryID)
			assert.Equal(t, base.Event.ID, decoded.Event.ID)

			check(t, body)
		}
	}

	cases := []testCase{
		{
			name:       "no reference parameters at all",
			parameters: "",
			assert: renders(func(t *testing.T, body []byte) {
				assert.NotContains(t, string(body), "referenceParameters")
			}),
		},
		{
			name:       "key order is not touched",
			parameters: `{"zeta":"last","alpha":"first"}`,
			assert: renders(func(t *testing.T, body []byte) {
				assertVerbatim(t, `{"zeta":"last","alpha":"first"}`, referenceParameters(t, body))
			}),
		},
		{
			name:       "an integer too large for a float64 keeps every digit",
			parameters: `{"ledgerEntry":9007199254740993}`,
			assert: renders(func(t *testing.T, body []byte) {
				assertVerbatim(t, `{"ledgerEntry":9007199254740993}`, referenceParameters(t, body))
			}),
		},
		{
			name:       "names the engine has never heard of arrive unchanged",
			parameters: `{"wsa:To":"urn:acme:orders","x-acme-tenant":{"id":7,"shard":"eu-2"}}`,
			assert: renders(func(t *testing.T, body []byte) {
				assertVerbatim(t,
					`{"wsa:To":"urn:acme:orders","x-acme-tenant":{"id":7,"shard":"eu-2"}}`,
					referenceParameters(t, body))
			}),
		},
		{
			name:       "parameters that are not an object are echoed as they are",
			parameters: `["one",2,null]`,
			assert: renders(func(t *testing.T, body []byte) {
				assertVerbatim(t, `["one",2,null]`, referenceParameters(t, body))
			}),
		},
		{
			name:       "whitespace inside the parameters is not reformatted",
			parameters: "{\n  \"b\": 1,\n  \"a\": 2\n}",
			assert: renders(func(t *testing.T, body []byte) {
				assertVerbatim(t, "{\n  \"b\": 1,\n  \"a\": 2\n}", referenceParameters(t, body))
			}),
		},
		{
			name:       "parameters that are not json at all are refused rather than spliced",
			parameters: `{"unterminated": `,
			assert: func(t *testing.T, body []byte, err error) {
				assert.Nil(t, body)
				assert.ErrorIs(t, err, webhook.ErrReferenceParameters)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := base
			if tc.parameters != "" {
				payload.ReferenceParameters = json.RawMessage(tc.parameters)
			}

			// Deliberately not json.Marshal: it compacts whatever a Marshaler
			// returns, which is the one transformation this test exists to
			// catch. The sink renders the body the same way, for the same
			// reason.
			body, err := payload.MarshalJSON()
			tc.assert(t, body, err)
		})
	}
}

// referenceParameters pulls the raw reference parameters back out of a body,
// without decoding them: the bytes are the property under test.
func referenceParameters(t *testing.T, body []byte) []byte {
	t.Helper()

	var envelope struct {
		ReferenceParameters json.RawMessage `json:"referenceParameters"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))

	return envelope.ReferenceParameters
}
