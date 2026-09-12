package hmntsk_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
)

func TestCallbackTargetJSONRoundTrip(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		target hmntsk.CallbackTarget
		assert func(t *testing.T, encoded []byte, decoded hmntsk.CallbackTarget)
	}

	cases := []testCase{
		{
			name: "reference parameters survive verbatim",
			target: hmntsk.CallbackTarget{
				Address:             "https://host.example/callbacks/tasks",
				ReferenceParameters: json.RawMessage(`{"zeta":"last","alpha":"first","seq":9007199254740993}`),
			},
			assert: func(t *testing.T, _ []byte, decoded hmntsk.CallbackTarget) {
				assertVerbatim(t,
					`{"zeta":"last","alpha":"first","seq":9007199254740993}`,
					decoded.ReferenceParameters,
					"names, values, order and number literals must all be preserved")
			},
		},
		{
			name: "a nested structure is not flattened or reordered",
			target: hmntsk.CallbackTarget{
				Address:             "queue://tasks",
				ReferenceParameters: json.RawMessage(`{"b":{"d":[1,2,{"e":null}],"c":true},"a":"x"}`),
			},
			assert: func(t *testing.T, _ []byte, decoded hmntsk.CallbackTarget) {
				assert.JSONEq(t,
					`{"b":{"d":[1,2,{"e":null}],"c":true},"a":"x"}`,
					string(decoded.ReferenceParameters))
				assertVerbatim(t,
					`{"b":{"d":[1,2,{"e":null}],"c":true},"a":"x"}`,
					decoded.ReferenceParameters)
			},
		},
		{
			name:   "an absent callback target is omitted, not invented",
			target: hmntsk.CallbackTarget{},
			assert: func(t *testing.T, encoded []byte, decoded hmntsk.CallbackTarget) {
				assert.True(t, decoded.IsZero())
				assert.NotContains(t, string(encoded), "referenceParameters")
			},
		},
		{
			name: "an address without parameters round-trips",
			target: hmntsk.CallbackTarget{
				Address: "https://host.example/hook",
			},
			assert: func(t *testing.T, _ []byte, decoded hmntsk.CallbackTarget) {
				assert.Equal(t, "https://host.example/hook", decoded.Address)
				assert.Nil(t, decoded.ReferenceParameters)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := json.Marshal(tc.target)
			require.NoError(t, err)

			var decoded hmntsk.CallbackTarget

			require.NoError(t, json.Unmarshal(encoded, &decoded))

			tc.assert(t, encoded, decoded)
		})
	}
}

func TestCallbackTargetCloneDoesNotAlias(t *testing.T) {
	t.Parallel()

	original := hmntsk.CallbackTarget{
		Address:             "https://host.example/hook",
		ReferenceParameters: json.RawMessage(`{"a":1}`),
	}

	clone := original.Clone()
	clone.ReferenceParameters[0] = '['

	assertVerbatim(t, `{"a":1}`, original.ReferenceParameters,
		"mutating a clone must not reach the original's bytes")
}

func TestCorrelationDataJSONRoundTrip(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name        string
		correlation hmntsk.CorrelationData
		assert      func(t *testing.T, encoded []byte, decoded hmntsk.CorrelationData)
	}

	cases := []testCase{
		{
			name: "every field survives",
			correlation: hmntsk.CorrelationData{
				OwnerType:   "process",
				OwnerRef:    "proc-42",
				ActivityKey: "approve-invoice",
				Extra:       map[string]string{"tenant": "acme"},
			},
			assert: func(t *testing.T, _ []byte, decoded hmntsk.CorrelationData) {
				assert.Equal(t, "process", decoded.OwnerType)
				assert.Equal(t, "proc-42", decoded.OwnerRef)
				assert.Equal(t, "approve-invoice", decoded.ActivityKey)
				assert.Equal(t, map[string]string{"tenant": "acme"}, decoded.Extra)
			},
		},
		{
			name:        "an empty correlation encodes to an empty object",
			correlation: hmntsk.CorrelationData{},
			assert: func(t *testing.T, encoded []byte, decoded hmntsk.CorrelationData) {
				assert.JSONEq(t, `{}`, string(encoded))
				assert.True(t, decoded.IsZero())
			},
		},
		{
			name: "a caller-specific owner type is stored, not interpreted",
			correlation: hmntsk.CorrelationData{
				OwnerType: "order",
				OwnerRef:  "ORD-0001",
			},
			assert: func(t *testing.T, _ []byte, decoded hmntsk.CorrelationData) {
				assert.Equal(t, "order", decoded.OwnerType)
				assert.False(t, decoded.IsZero())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := json.Marshal(tc.correlation)
			require.NoError(t, err)

			var decoded hmntsk.CorrelationData

			require.NoError(t, json.Unmarshal(encoded, &decoded))

			tc.assert(t, encoded, decoded)
		})
	}
}

func TestCorrelationDataCloneDoesNotAlias(t *testing.T) {
	t.Parallel()

	original := hmntsk.CorrelationData{Extra: map[string]string{"tenant": "acme"}}

	clone := original.Clone()
	clone.Extra["tenant"] = "other"

	assert.Equal(t, "acme", original.Extra["tenant"])
}
