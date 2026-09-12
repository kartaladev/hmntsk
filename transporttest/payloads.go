package transporttest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// runPayloadCases pins that a payload crosses the API and comes back intact.
func runPayloadCases(t *testing.T, mount Mount) {
	t.Helper()

	type testCase struct {
		name    string
		payload string
		assert  func(t *testing.T, stored string)
	}

	cases := []testCase{
		{
			name:    "an integer beyond exact float range survives",
			payload: `{"amount":9007199254740993,"justification":"x"}`,
			assert: func(t *testing.T, stored string) {
				assert.Contains(t, stored, "9007199254740993",
					"decoding the payload through a float64 anywhere on the path would "+
						"return 9007199254740992 and nobody would notice for months")
			},
		},
		{
			name:    "a very large identifier survives",
			payload: `{"amount":1,"justification":"x","reference":18446744073709551615}`,
			assert: func(t *testing.T, stored string) {
				assert.Contains(t, stored, "18446744073709551615")
			},
		},
		{
			name:    "fields the schema does not describe survive",
			payload: `{"amount":1,"justification":"x","attachments":[{"id":"a"},{"id":"b"}],"draft":null}`,
			assert: func(t *testing.T, stored string) {
				assert.Contains(t, stored, `"attachments"`)
				assert.Contains(t, stored, `"draft":null`)
			},
		},
		{
			name:    "field order is not rewritten",
			payload: `{"zulu":1,"amount":1,"justification":"x","alpha":2}`,
			assert: func(t *testing.T, stored string) {
				//nolint:testifylint // byte-exact comparison is the property under test.
				assert.Equal(t, `{"zulu":1,"amount":1,"justification":"x","alpha":2}`, stored,
					"key order must survive the whole round trip; a semantic comparison "+
						"would pass on a payload whose keys had been sorted")
			},
		},
		{
			name:    "number formatting is not normalised",
			payload: `{"amount":1.500,"justification":"x","exp":1e3}`,
			assert: func(t *testing.T, stored string) {
				assert.Contains(t, stored, "1.500")
				assert.Contains(t, stored, "1e3")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewClient(t, mount)

			task := client.CreateApproval(t, func(body map[string]any) {
				body["input"] = json.RawMessage(tc.payload)
			})

			read := client.Do(t, http.MethodGet, "/tasks/"+task.ID.String(), Alice, nil)
			require.Equal(t, transportcore.StatusOK, read.Status)

			tc.assert(t, string(read.Task(t).Input))
		})
	}

	t.Run("an output payload survives completion and a read back", func(t *testing.T) {
		client, _ := NewClient(t, mount)

		task := client.CreateApproval(t, func(body map[string]any) {
			body["candidates"] = map[string]any{"users": []string{Alice}}
		})
		id := task.ID.String()

		require.Equal(t, transportcore.StatusOK,
			client.Do(t, http.MethodPost, "/tasks/"+id+"/start", Alice, nil).Status)

		output := `{"approved":false,"note":"over budget","ledger":9007199254740993}`

		completed := client.Do(t, http.MethodPost, "/tasks/"+id+"/complete", Alice,
			map[string]any{"output": json.RawMessage(output)})
		require.Equalf(t, transportcore.StatusOK, completed.Status, "%s", completed.Body)

		read := client.Do(t, http.MethodGet, "/tasks/"+id, Alice, nil)
		assert.Equal(t, output, string(read.Task(t).Output))
	})
}
