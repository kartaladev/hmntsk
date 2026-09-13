package transportcore_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	transportcore "github.com/kartaladev/hmntsk/transport/core"
)

// inboxGuidePath is the inbox guide, relative to this module.
const inboxGuidePath = "../../docs/inbox.md"

// TestInboxGuideNamesWhatTheCodeDefines keeps docs/inbox.md in step with the
// code it tells a host to use.
//
// Every value it looks for is read from the code, and every name is referenced
// below, so renaming a policy, an ordering or a metadata key breaks this test
// before the guide can go on describing the old one.
func TestInboxGuideNamesWhatTheCodeDefines(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(inboxGuidePath)
	require.NoError(t, err, "the inbox guide must exist")

	guide := string(raw)

	// Referenced, not only named: a rename fails to compile here.
	_ = []any{
		transportcore.SelfOnly, transportcore.AllowAll, transportcore.WithQueryAuthorizer,
		transportcore.QueryAuthorizerFunc(nil), hmntsk.ExpandRoute,
		(*hmntsk.Service).Count, (*hmntsk.Service).CountBuckets,
		transportcore.ParticipantsOnly, transportcore.WithTaskReadAuthorizer,
		transportcore.TaskReadAuthorizerFunc(nil), transportcore.TaskRead.Eligible,
	}

	for _, mention := range []struct {
		what string
		text string
	}{
		{what: "the default query policy", text: "SelfOnly"},
		{what: "the explicit opt-out", text: "AllowAll"},
		{what: "the option that replaces the policy", text: "WithQueryAuthorizer"},
		{what: "the adapter a host writes a policy with", text: "QueryAuthorizerFunc"},
		{what: "the value naming the acting user", text: "`" + transportcore.Me + "`"},
		{what: "counting one query", text: "Count"},
		{what: "counting a set of buckets", text: "CountBuckets"},
		{what: "the bucket limit", text: "MaxCountBuckets"},
		{what: "the bucket limit's value", text: strconv.Itoa(hmntsk.MaxCountBuckets)},
		{what: "creation order", text: "`created`"},
		{what: "priority order", text: "`" + string(hmntsk.OrderPriority) + "`"},
		{what: "due-date order", text: "`" + string(hmntsk.OrderDue) + "`"},
		{what: "urgency order", text: "`" + string(hmntsk.OrderUrgency) + "`"},
		{what: "the form key", text: hmntsk.MetadataFormKey},
		{what: "the route key", text: hmntsk.MetadataRoute},
		{what: "the route helper", text: "ExpandRoute"},
		{what: "the default read policy", text: "ParticipantsOnly"},
		{what: "the option that replaces the read policy", text: "WithTaskReadAuthorizer"},
		{what: "the adapter a host writes a read policy with", text: "TaskReadAuthorizerFunc"},
		{what: "the lazy eligibility check a read policy calls", text: "read.Eligible"},
		{what: "the section on reading one task", text: "### Who may read a task"},
	} {
		t.Run(mention.what, func(t *testing.T) {
			t.Parallel()

			assert.Containsf(t, guide, mention.text, "the inbox guide never mentions %s (%s)",
				mention.what, mention.text)
		})
	}
}
