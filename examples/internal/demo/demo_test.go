package demo_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/notify"
)

func TestSections(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		section func(w io.Writer, title string)
		assert  func(t *testing.T, printed string)
	}

	cases := []testCase{
		{
			name:    "default section",
			section: demo.Default,
			assert: func(t *testing.T, printed string) {
				assert.Equal(t, "\n== default: self-only queries\n", printed)
			},
		},
		{
			name:    "override section",
			section: demo.Override,
			assert: func(t *testing.T, printed string) {
				assert.Equal(t, "\n== override: self-only queries\n", printed)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			tc.section(&buf, "self-only queries")
			tc.assert(t, buf.String())
		})
	}
}

func TestClock(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	clock := demo.NewClock(start)

	var (
		_ hmntsk.Clock = clock
		_ notify.Clock = clock
	)

	assert.Equal(t, start, clock.Now())

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { clock.Advance(time.Hour) })
	}

	wg.Wait()

	assert.Equal(t, start.Add(10*time.Hour), clock.Now(), "advancing is safe from several goroutines")
}

func TestNamesMask(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		setup  func(names *demo.Names)
		input  string
		assert func(t *testing.T, masked string)
	}

	cases := []testCase{
		{
			name:  "a named identifier is replaced everywhere",
			setup: func(names *demo.Names) { names.Name("0199-abc", "task-1") },
			input: "/v1/tasks/0199-abc and /invoices/INV-42?task=0199-abc",
			assert: func(t *testing.T, masked string) {
				assert.Equal(t, "/v1/tasks/<task-1> and /invoices/INV-42?task=<task-1>", masked)
			},
		},
		{
			name: "several identifiers keep their own names",
			setup: func(names *demo.Names) {
				names.Name("id-a", "offer-alice")
				names.Name("id-b", "offer-bob")
			},
			input: "id-b id-a",
			assert: func(t *testing.T, masked string) {
				assert.Equal(t, "<offer-bob> <offer-alice>", masked)
			},
		},
		{
			name:  "text without identifiers is unchanged",
			setup: func(*demo.Names) {},
			input: "nothing to hide",
			assert: func(t *testing.T, masked string) {
				assert.Equal(t, "nothing to hide", masked)
			},
		},
		{
			name:  "an empty identifier is ignored rather than masking every gap",
			setup: func(names *demo.Names) { names.Name("", "empty") },
			input: "abc",
			assert: func(t *testing.T, masked string) {
				assert.Equal(t, "abc", masked)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			names := demo.NewNames()
			tc.setup(names)
			tc.assert(t, names.Mask(tc.input))
		})
	}
}

func TestServe(t *testing.T) {
	t.Parallel()

	base, stop, err := demo.Serve(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(base, "http://127.0.0.1:"), "serves on loopback, got %q", base)

	status := func() (int, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/", http.NoBody)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()

		return resp.StatusCode, nil
	}

	code, err := status()
	require.NoError(t, err)
	assert.Equal(t, http.StatusTeapot, code)

	stop()

	_, err = status()
	assert.Error(t, err, "nothing is served once stopped")
}

func TestNamesAreSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	names := demo.NewNames()

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			names.Name(string(rune('a'+i)), "x")
			_ = names.Mask("abc")
		})
	}

	wg.Wait()
	require.Equal(t, "<x><x><x>", names.Mask("abc"))
}
