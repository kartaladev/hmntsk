package redis_test

import (
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmntskredis "github.com/kartaladev/hmntsk/delivery/redis"
)

// TestTheDocumentedRetentionConstraintsMatchTheImplementation keeps the stream
// retention section of docs/delivery.md from drifting away from the sink.
//
// An operator configures retention from the document, not from the Go source,
// and every constraint in it is one that loses data or stops publishing when it
// is missed. So the modes an operator sets, the option names they type, the
// version a trim mode needs, and the error an older broker answers with must
// all be in that section — the error string being the same one
// TestATrimModeFailsRetryably gets from a real Redis 7. Names are taken from the
// code, so renaming an option fails this test until the document follows.
func TestTheDocumentedRetentionConstraintsMatchTheImplementation(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../docs/delivery.md")
	require.NoError(t, err, "docs/delivery.md is where the retention constraints are documented")

	section := docSection(t, string(raw), "Bounding the Redis stream")

	for _, mode := range []hmntskredis.TrimMode{
		hmntskredis.TrimKeepRef,
		hmntskredis.TrimDelRef,
		hmntskredis.TrimAcked,
	} {
		assert.Containsf(t, section, string(mode), "every trim mode must be documented: %s", mode)
	}

	for _, option := range []any{
		hmntskredis.WithMaxLen,
		hmntskredis.WithMaxAge,
		hmntskredis.WithTrimMode,
		hmntskredis.WithClock,
	} {
		name := funcName(option)
		assert.Containsf(t, section, name, "every retention option must be documented: %s", name)
	}

	assert.Contains(t, section, "8.2", "the Redis version trim modes need must be documented")
	assert.Contains(t, section, redis7TrimModeError,
		"the error an older broker answers a trim mode with must be quoted, so an operator searching for it finds why")
	assert.Contains(t, section, "stream-node-max-entries",
		"what approximate trimming means depends on stream-node-max-entries, and must say so")
}

// docSection returns a top-level section of a Markdown document, from its
// heading to the next top-level heading, so that a check cannot be satisfied by
// the same words elsewhere in the document.
func docSection(t *testing.T, doc, heading string) string {
	t.Helper()

	marker := "\n# " + heading + "\n"

	start := strings.Index(doc, marker)
	require.GreaterOrEqualf(t, start, 0, "docs/delivery.md must have a %q section", heading)

	section := doc[start+len(marker):]
	if end := strings.Index(section, "\n# "); end >= 0 {
		section = section[:end]
	}

	return section
}

// funcName is the unqualified name of a package-level function.
func funcName(fn any) string {
	full := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()

	return full[strings.LastIndex(full, ".")+1:]
}
