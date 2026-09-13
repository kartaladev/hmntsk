package notify_test

import (
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
)

// funcName is an exported function's bare name, taken from the code so that a
// renamed option fails the docs test until the document follows.
func funcName(fn any) string {
	full := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()

	return full[strings.LastIndex(full, ".")+1:]
}

// docSection returns the body of a level-two section of a markdown document,
// failing the test when the section is missing.
func docSection(t *testing.T, document, heading string) string {
	t.Helper()

	marker := "\n## " + heading + "\n"

	start := strings.Index(document, marker)
	require.GreaterOrEqualf(t, start, 0, "docs/notifications.md has a %q section", heading)

	body := document[start+len(marker):]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}

	return body
}

// days renders a duration in whole days, as the document writes it.
func days(d time.Duration) string {
	return strconv.Itoa(int(d/(24*time.Hour))) + " days"
}

// TestTheDocumentMatchesTheImplementation keeps docs/notifications.md from
// drifting away from the code. A host configures notifications from the
// document, so every default it states must be the value the code applies,
// every option it names must exist, and every stated limit must be there.
func TestTheDocumentMatchesTheImplementation(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("docs/notifications.md")
	require.NoError(t, err, "docs/notifications.md is where notifications are documented")

	document := "\n" + string(raw)

	type testCase struct {
		name    string
		section string
		needles []string
	}

	cases := []testCase{
		{
			name:    "the service's defaults and options",
			section: "Defaults and overrides",
			needles: []string{
				funcName(notify.New), funcName(notify.NewMemoryStore), funcName(notify.WithClock),
				funcName(notify.WithIDGenerator), funcName(notify.WithBroadcaster), funcName(notify.WithSignalErrorHandler),
				"InProcessBroadcaster", "UUIDv7",
			},
		},
		{
			name:    "publishing and closing semantics",
			section: "Publishing and closing",
			needles: []string{"SourceID", "watermark", "Coalesce", "Successor", "SuccessorSkip", "Except"},
		},
		{
			name:    "retention defaults, options and strategies",
			section: "Retention",
			needles: []string{
				strconv.Itoa(notify.DefaultMaxPerRecipient), days(notify.DefaultMaxAge),
				days(notify.DefaultWatermarkRetention), strconv.Itoa(notify.DefaultPruneBatch),
				funcName(notify.NewPruner), funcName(notify.WithMaxPerRecipient), funcName(notify.WithoutMaxPerRecipient),
				funcName(notify.WithMaxAge), funcName(notify.WithoutMaxAge), funcName(notify.WithRetentionStrategy),
				funcName(notify.WithWatermarkRetention), funcName(notify.WithPruneBatch),
				funcName(notify.WithPruneErrorHandler), "EvictOldestActive", "RetainActive", "approximate",
			},
		},
		{
			name:    "realtime defaults, policies and limits",
			section: "Realtime",
			needles: []string{
				notify.DefaultHeartbeat.String(), notify.DefaultWriteTimeout.String(),
				strconv.Itoa(notify.DefaultMaxStreamsPerRecipient),
				funcName(notify.NewHub), funcName(notify.WithHeartbeat), funcName(notify.WithWriteTimeout),
				funcName(notify.WithMaxStreamsPerRecipient), "SelfOnly", "AllowAll", "unread-changed",
				"single instance", "re-read",
			},
		},
		{
			name:    "the HTTP contract and its mounting",
			section: "HTTP and mounting",
			needles: []string{
				notify.DefaultBasePath, strconv.Itoa(notify.DefaultListLimit), strconv.Itoa(notify.MaxListLimit),
				funcName(notify.NewHandler), funcName(notify.WithActor), funcName(notify.WithBasePath),
				funcName(notify.WithSubscriptionAuthorizer), funcName(notify.WriteError),
				"gin.WrapH", "adaptor.HTTPHandler", "X-Accel-Buffering",
			},
		},
		{
			name:    "the stated limits",
			section: "Stated limits",
			needles: []string{"does not join", "single instance", "approximate", "re-read"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			section := docSection(t, document, tc.section)

			for _, needle := range tc.needles {
				assert.Containsf(t, section, needle, "the %q section mentions %s", tc.section, needle)
			}
		})
	}
}
