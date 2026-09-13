package nats_test

import (
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmntsknats "github.com/kartaladev/hmntsk/delivery/nats"
)

// TestTheDocumentedNATSContractMatchesTheImplementation keeps the NATS section
// of docs/delivery.md from drifting away from the sinks.
//
// A consumer is written against the document, and a host chooses a mode from
// it. So every header a message carries, the schema, the subject layout, both
// sink names, every constructor and option, and the error a forgotten stream
// answers with must all be in that section. Values and names are taken from the
// code, so renaming one fails this test until the document follows.
func TestTheDocumentedNATSContractMatchesTheImplementation(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../docs/delivery.md")
	require.NoError(t, err, "docs/delivery.md is where the NATS contract is documented")

	section := docSection(t, string(raw), "Publishing to NATS")

	// Quoted as code, so that "nats" is not satisfied by any sentence that
	// mentions NATS.
	for _, value := range []string{
		hmntsknats.HeaderEventID,
		hmntsknats.HeaderDeliveryID,
		hmntsknats.HeaderAttempt,
		hmntsknats.HeaderEventType,
		hmntsknats.HeaderTaskID,
		hmntsknats.HeaderTaskType,
		hmntsknats.HeaderOwnerType,
		hmntsknats.HeaderOwnerRef,
		hmntsknats.HeaderActivityKey,
		hmntsknats.HeaderSchema,
		hmntsknats.Schema,
		hmntsknats.ContentType,
		hmntsknats.DefaultSubjectPrefix,
		hmntsknats.DefaultName,
		hmntsknats.DefaultJetStreamName,
	} {
		assert.Containsf(t, section, "`"+value+"`", "the section must name %s", value)
	}

	for _, fn := range []any{
		hmntsknats.NewSink,
		hmntsknats.NewJetStreamSink,
		hmntsknats.WithName,
		hmntsknats.WithSubjectPrefix,
		hmntsknats.WithTimeout,
		hmntsknats.WithExpectStream,
	} {
		name := funcName(fn)
		assert.Containsf(t, section, name, "every constructor and option must be documented: %s", name)
	}

	assert.Contains(t, section, jetstream.ErrNoStreamResponse.Error(),
		"the error a forgotten stream answers with must be quoted, so an operator searching for it finds why")
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
